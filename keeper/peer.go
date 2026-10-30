// peer.go
// Author: Kennt Kim
// Created: 2026-10-26 · Last updated: 2026-10-29
//
// Purpose: one connection to a keeper (Docs/KEEPER.md §2): who it says it is, whether it can watch,
// the size it asked for, and the frames waiting to be written to it, written by a goroutine of its
// own.
//
// Notes:
// - Each connection is written from its own queue (2026-10-26, joining): with one client, the
//   keeper wrote under its lock and a slow client slowed only itself, but a teammate watching over
//   a slow link would then hold up the shell of whoever types. The holder still holds the program
//   back: the keeper reads no more output while more than holderQueued bytes wait for it, as the
//   socket's buffer did before. A watcher never does: one with more than watcherQueued bytes
//   waiting is dropped, and joins again with a replay.
// - Frames are encoded once and shared by every queue; nothing changes them after.
// - closeAfter writes what is queued, within closeWithin, then closes: a connection taken over gets
//   its TakenOver, and every connection the program's Exit, before the keeper ends.
// - A write that fails — the client went, leaving output unread — ends only the writing: the frames
//   the client sent before it went are still read, and the session's reader closes the connection
//   at their end. Closing it at once lost them: a client that hung up and closed while output
//   flowed to it left its shell running, detached (2026-10-29, found by a test whose client read
//   nothing: the keeper described itself unattached and never ended).

//go:build unix

package keeper

import (
	"net"
	"sync"
	"time"
)

const (
	// holderQueued is how much may wait for the holder before the keeper stops reading output.
	holderQueued = 1 << 20
	// watcherQueued is how much may wait for a watcher before it is dropped: a replay (at most
	// keptBytes and the modes before it) and a few MiB of output behind it.
	watcherQueued = keptBytes + 4<<20
	// closeWithin is how long a connection closed after its queue has to take it.
	closeWithin = time.Second
)

// peer is one connection to the keeper. Its fields other than the queue's are guarded by the
// keeper's out lock.
type peer struct {
	conn net.Conn
	// who is how the connection named itself (Who); zero until it does.
	who Who
	// aware says it can watch: it joined, or said Who. One that is not is TakenOver when it loses
	// the keyboard, as every client was before joining.
	aware bool
	// cols and rows are the size it last asked for (Resize); 0 before it asked.
	cols, rows int
	// lastInfo is the Info it was last sent.
	lastInfo *Info
	// leaseUntil is when its lease of being in front runs out (Front, observe.go); zero: none.
	leaseUntil time.Time

	qmu     sync.Mutex
	qcond   *sync.Cond
	queue   [][]byte
	queued  int  // bytes in queue and being written
	closing bool // close once the queue is written
	closed  bool
}

func newPeer(conn net.Conn, aware bool) *peer {
	p := &peer{conn: conn, aware: aware}
	p.qcond = sync.NewCond(&p.qmu)
	go p.write()
	return p
}

// push queues frames; a closed connection drops them.
func (p *peer) push(frames ...[]byte) {
	p.qmu.Lock()
	defer p.qmu.Unlock()
	if p.closed || p.closing {
		return
	}
	for _, f := range frames {
		p.queue = append(p.queue, f)
		p.queued += len(f)
	}
	p.qcond.Broadcast()
}

// waiting is how many bytes wait to be written.
func (p *peer) waiting() int {
	p.qmu.Lock()
	defer p.qmu.Unlock()
	return p.queued
}

// waitBelow returns once fewer than limit bytes wait, or the connection is closed.
func (p *peer) waitBelow(limit int) {
	p.qmu.Lock()
	defer p.qmu.Unlock()
	for p.queued > limit && !p.closed {
		p.qcond.Wait()
	}
}

// closeAfter closes the connection once what is queued is written, within closeWithin.
func (p *peer) closeAfter() {
	p.qmu.Lock()
	defer p.qmu.Unlock()
	p.closing = true
	p.qcond.Broadcast()
}

// close closes the connection now, dropping what is queued.
func (p *peer) close() {
	p.qmu.Lock()
	p.closed = true
	p.queue = nil
	p.qcond.Broadcast()
	p.qmu.Unlock()
	p.conn.Close()
}

// waitClosed returns once the connection is closed, or by deadline.
func (p *peer) waitClosed(deadline time.Time) {
	timer := time.AfterFunc(time.Until(deadline), func() {
		p.qmu.Lock()
		p.qcond.Broadcast()
		p.qmu.Unlock()
	})
	defer timer.Stop()
	p.qmu.Lock()
	defer p.qmu.Unlock()
	for !p.closed && time.Now().Before(deadline) {
		p.qcond.Wait()
	}
}

// shutWrite ends the writing half of conn, which the session's reader goes on reading.
func shutWrite(conn net.Conn) {
	if c, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = c.CloseWrite()
	}
}

// write writes the queue to the connection until it is closed.
func (p *peer) write() {
	for {
		p.qmu.Lock()
		for len(p.queue) == 0 && !p.closing && !p.closed {
			p.qcond.Wait()
		}
		if p.closed || (p.closing && len(p.queue) == 0) {
			p.closed = true
			p.qcond.Broadcast()
			p.qmu.Unlock()
			p.conn.Close()
			return
		}
		batch, closing := p.queue, p.closing
		p.queue = nil
		p.qmu.Unlock()

		if closing {
			_ = p.conn.SetWriteDeadline(time.Now().Add(closeWithin))
		}
		size := 0
		for _, f := range batch {
			size += len(f)
		}
		buffers := net.Buffers(batch)
		_, err := buffers.WriteTo(p.conn)

		p.qmu.Lock()
		p.queued -= size
		if err != nil {
			p.closed = true
			p.queue = nil
		}
		p.qcond.Broadcast()
		p.qmu.Unlock()
		if err != nil {
			if closing {
				p.conn.Close()
			} else {
				// Only the writing ends: what the client sent before it went — a Hangup — is still to
				// be read, and the session's reader closes the connection at its end (dropConn).
				shutWrite(p.conn)
			}
			return
		}
	}
}

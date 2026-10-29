// pacer.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-30
//
// Purpose: when a terminal's frames go to its views (Docs/design/01-DESIGN.md §3.1): at most one
// frame per frameInterval, at most maxUnacked frames the views have not applied, and frames held
// while the program holds synchronized output, up to syncHold (SiliconDesk's PROTOCOL.md §7.4).
//
// Notes:
// - Taken from SiliconDesk's host/internal/session/terminal.go (maybeFlush, flushAfter and their
//   constants) on 2026-10-23. There it ran on the session's event loop; here a Pacer has its own
//   lock and timer, since the engine has no loop of its own.
// - Typing echoes at once (a change after a quiet period is sent immediately); a flood costs one
//   frame per round trip of the views; lines that scroll into the history are always sent.
// - Lock order: Pacer.mu, then the terminal's mu (Frame and SyncHeld take it). The terminal never
//   calls OnChange with its mu held, so Changed may take Pacer.mu from there.
// - send runs with Pacer.mu held and must not block: the engine's user copies the frame and
//   hands it on.

//go:build unix

package termhost

import (
	"sync"
	"time"

	pb "github.com/kennss/spectashell-keeper/gen/terminal/v1"
)

const (
	maxUnacked    = 2
	frameInterval = 16 * time.Millisecond
	syncHold      = 150 * time.Millisecond
)

// Pacer sends one terminal's frames to its views.
type Pacer struct {
	mu        sync.Mutex
	t         *Terminal
	id        uint64
	send      func(*pb.TerminalFrame)
	sync      Sync
	dirty     bool
	scheduled bool
	stopped   bool
	lastFrame time.Time
	heldSince time.Time
}

// NewPacer paces terminal t's frames, named id, to send. The first frame starts over.
func NewPacer(t *Terminal, id uint64, send func(*pb.TerminalFrame)) *Pacer {
	p := &Pacer{t: t, id: id, send: send, dirty: true}
	p.mu.Lock()
	p.flush()
	p.mu.Unlock()
	return p
}

// Changed says the terminal changed (Config.OnChange).
func (p *Pacer) Changed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dirty = true
	p.flush()
}

// Ack says the views applied every frame up to seq (TerminalAck).
func (p *Pacer) Ack(seq uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if seq > p.sync.Acked && seq <= p.sync.Seq {
		p.sync.Acked = seq
	}
	p.flush()
}

// StartOver forgets what the views have, so the next frame carries the whole screen again: for
// views that were rebuilt and hold nothing.
func (p *Pacer) StartOver() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sync = Sync{}
	p.dirty = true
	p.flush()
}

// Stop sends no more frames.
func (p *Pacer) Stop() {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
}

// flush sends the next frame now, or arranges for it, within the pacing rules; mu is held.
func (p *Pacer) flush() {
	if p.stopped || !p.dirty || p.scheduled {
		return
	}
	if p.sync.Seq-p.sync.Acked >= maxUnacked {
		return // the ack calls back
	}
	now := time.Now()
	if p.t.SyncHeld() {
		if p.heldSince.IsZero() {
			p.heldSince = now
		}
		if wait := syncHold - now.Sub(p.heldSince); wait > 0 {
			p.flushAfter(wait)
			return
		}
	} else {
		p.heldSince = time.Time{}
	}
	if wait := frameInterval - now.Sub(p.lastFrame); wait > 0 {
		p.flushAfter(wait)
		return
	}
	f, more := p.t.Frame(&p.sync)
	p.dirty = more
	if f == nil {
		return
	}
	p.sync.Seq++
	f.Seq = p.sync.Seq
	f.TerminalId = p.id
	p.lastFrame = now
	p.send(f)
}

func (p *Pacer) flushAfter(wait time.Duration) {
	p.scheduled = true
	time.AfterFunc(wait, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.scheduled = false
		p.flush()
	})
}

// listen.go
// Author: Kennt Kim
// Created: 2026-10-25 · Last updated: 2026-10-30
//
// Purpose: a device's socket for page commands (Docs/design/05-WINDOWS.md §3), as the core on the
// Mac and a relay on a host listen on it: made at its path, mode 0600, in place of whatever was
// left there; made again when it was removed, as a keeper makes its own again (Docs/KEEPER.md §4);
// and removed when it closes, unless another took its path meanwhile.
//
// Notes:
// - Newest wins: a device with two connections to one machine (two addresses of it) runs two
//   relays there, and the second takes the path. The first then leaves the path alone while the
//   second's socket is there, and takes it again when that one closes and removes it, so the
//   device stays reachable while either connection lasts.
// - Each connection is answered on a goroutine of its own: a `wait` may take its time.
// - The listener replaced on a check is closed without unlinking the path (SetUnlinkOnClose), which
//   another socket's may be by then.
// - Moved here from core/internal/pages on 2026-10-29, with the keeper program
//   (Docs/design/01-DESIGN.md §3.2).

//go:build unix

package pages

import (
	"errors"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// checkEvery is how often a listener looks for its socket; a variable, so a test looks sooner.
var checkEvery = 10 * time.Second

// Listener is a device's socket, answering each request with its handler.
type Listener struct {
	path   string
	handle Handler
	every  time.Duration

	mu     sync.Mutex
	ln     *net.UnixListener
	inode  uint64
	closed bool
	done   chan struct{}
}

// Listen listens at path, replacing what was there, and answers every request with handle.
func Listen(path string, handle Handler) (*Listener, error) {
	l := &Listener{path: path, handle: handle, every: checkEvery, done: make(chan struct{})}
	if err := l.listen(); err != nil {
		return nil, err
	}
	go l.keep()
	return l, nil
}

// Path is where it listens.
func (l *Listener) Path() string { return l.path }

// Close stops listening and removes the socket, unless another took its path.
func (l *Listener) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	close(l.done)
	if l.ln != nil {
		l.ln.SetUnlinkOnClose(false)
		l.ln.Close()
	}
	if inode, ok := inodeOf(l.path); ok && inode == l.inode {
		_ = os.Remove(l.path)
	}
}

// listen makes the socket at the path, in place of what was there.
func (l *Listener) listen() error {
	_ = os.Remove(l.path)
	addr := &net.UnixAddr{Name: l.path, Net: "unix"}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		return err
	}
	if err := os.Chmod(l.path, 0o600); err != nil {
		ln.Close()
		return err
	}
	inode, ok := inodeOf(l.path)
	if !ok {
		ln.Close()
		return errors.New("the socket could not be read back")
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		ln.Close()
		return errors.New("closed")
	}
	if l.ln != nil {
		l.ln.SetUnlinkOnClose(false)
		l.ln.Close()
	}
	l.ln, l.inode = ln, inode
	l.mu.Unlock()
	go l.accept(ln)
	return nil
}

func (l *Listener) accept(ln *net.UnixListener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go serveConn(conn, l.handle)
	}
}

// keep makes the socket again whenever its path is empty.
func (l *Listener) keep() {
	tick := time.NewTicker(l.every)
	defer tick.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-tick.C:
			if _, err := os.Lstat(l.path); errors.Is(err, os.ErrNotExist) {
				_ = l.listen()
			}
		}
	}
}

// inodeOf is the inode of the file at path.
func inodeOf(path string) (uint64, bool) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Ino), true
}

// bridge.go
// Author: Kennt Kim
// Created: 2026-10-27 · Last updated: 2026-10-30
//
// Purpose: a console as a program a keeper runs (Docs/design/02-CONNECTIONS.md §6):
// spectashell-keeper's serial and telnet commands put a serial port or a telnet session where a
// shell would be, and this copies between it and the keeper's terminal — so a board's console stays
// open with the app closed, its output is replayed to whoever opens it, and another device can open
// or watch it, as any kept shell.
//
// Notes:
//   - The keeper's terminal is put in raw mode by the command, so Control-C, a carriage return and
//     every other byte reach the board as typed; the console's own end of line is the device's.
//   - The app controls the console in band, by a private OSC in what it types — ESC ] 7503 ; word
//     BEL, word release, reopen or break — sent whole in one write by its buttons (Release Port,
//     Reopen Port, Send Break). A sequence is looked for within one read only: never waiting for
//     more input, so an Escape typed alone reaches the board at once. Nothing typed by a person
//     sends it: a terminal's keys produce no OSC.
//   - Released, the port is closed and what is typed is dropped, so a flashing tool can open it; the
//     session stays, with its output, until Reopen Port. Releasing a telnet session is not offered.
//   - The console ending — the adapter unplugged, the server closing the session — says so in the
//     terminal and ends the program, with 1 for a failure and 0 for a session the server closed.
// - Moved here from core/internal/console on 2026-10-29, with the keeper program
//   (Docs/design/01-DESIGN.md §3.2).

package console

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Remote is what a console talks to: an open serial port, a telnet connection.
type Remote interface {
	io.ReadWriteCloser
}

// Translator stands between the terminal and the remote: a serial port passes bytes as they are
// (Plain), telnet speaks its protocol.
type Translator interface {
	// FromRemote takes the remote's bytes: what the terminal shows, and what goes back.
	FromRemote(b []byte) (toTerm, toRemote []byte)
	// FromTerm makes what is typed ready to send.
	FromTerm(b []byte) []byte
	// Resize returns what tells the remote the window's new size; nil when it does not hear it.
	Resize(cols, rows int) []byte
}

// Plain is the translator of a serial port: bytes as they are, no size.
type Plain struct{}

func (Plain) FromRemote(b []byte) ([]byte, []byte) { return b, nil }
func (Plain) FromTerm(b []byte) []byte             { return b }
func (Plain) Resize(int, int) []byte               { return nil }

// The control words the app types (ControlSequence).
const (
	Release = "release"
	Reopen  = "reopen"
	Break   = "break"
)

// ControlSequence is what the app types to control a console: ESC ] 7503 ; word BEL.
func ControlSequence(word string) []byte { return []byte("\x1b]7503;" + word + "\a") }

var controlPrefix = []byte("\x1b]7503;")

// Bridge copies between a keeper's terminal and a remote.
type Bridge struct {
	// In and Out are the terminal: what is typed, and where output goes.
	In  io.Reader
	Out io.Writer
	// Name says what the console is, in its notes: "/dev/cu.usbserial-110 · 115200 8N1".
	Name string
	// Title is the terminal's title, set as the console starts (OSC 2), so its tab and its card in
	// Running name the port or the server, not the program: "cu.usbserial-110"; empty: none set.
	Title string
	// Open opens the remote: at the start, and again at Reopen Port.
	Open func() (Remote, Translator, error)
	// SendBreak sends a break on the remote; nil: Send Break does nothing.
	SendBreak func(Remote) error
	// Releasable: Release Port and Reopen Port act (a serial port).
	Releasable bool
	// Ended says what the remote's end of input means: a failure (a port unplugged) or the
	// session's end (a telnet server closing it).
	EOFIsExit bool

	outMu    sync.Mutex
	mu       sync.Mutex
	remote   Remote
	tr       Translator
	released bool
	// Told once per release that typing goes nowhere.
	toldReleased bool
}

// ended is a remote's reader stopping: which remote, and why.
type ended struct {
	remote Remote
	err    error
}

// Run copies until the console ends or the terminal closes; it returns the program's exit code.
// resize brings the window's size as it changes.
func (b *Bridge) Run(resize <-chan [2]int) int {
	if b.Title != "" {
		b.write([]byte("\x1b]2;" + b.Title + "\a"))
	}
	remote, tr, err := b.Open()
	if err != nil {
		b.Note("%s", err)
		return 1
	}
	b.Note("%s", b.Name)
	ends := make(chan ended, 2)
	b.attach(remote, tr, ends)
	typed := make(chan []byte)
	closed := make(chan struct{})
	go b.readTerminal(typed, closed)
	for {
		select {
		case chunk := <-typed:
			b.typed(chunk, ends)
		case size := <-resize:
			b.mu.Lock()
			if b.remote != nil {
				if msg := b.tr.Resize(size[0], size[1]); msg != nil {
					_, _ = b.remote.Write(msg)
				}
			}
			b.mu.Unlock()
		case e := <-ends:
			b.mu.Lock()
			current := e.remote == b.remote
			if current {
				b.remote = nil
			}
			b.mu.Unlock()
			if !current {
				continue // a port released or reopened: its reader's end is expected
			}
			e.remote.Close()
			if errors.Is(e.err, io.EOF) && b.EOFIsExit {
				b.Note("The session was closed")
				return 0
			}
			if errors.Is(e.err, io.EOF) {
				b.Note("%s ended: the adapter was unplugged", b.Name)
			} else {
				b.Note("%s ended: %s", b.Name, e.err)
			}
			return 1
		case <-closed:
			b.mu.Lock()
			if b.remote != nil {
				b.remote.Close()
			}
			b.mu.Unlock()
			return 0
		}
	}
}

// attach makes remote the console's, and reads it.
func (b *Bridge) attach(remote Remote, tr Translator, ends chan<- ended) {
	b.mu.Lock()
	b.remote, b.tr, b.released, b.toldReleased = remote, tr, false, false
	b.mu.Unlock()
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := remote.Read(buf)
			if n > 0 {
				b.mu.Lock()
				var show []byte
				if remote == b.remote {
					var back []byte
					show, back = b.tr.FromRemote(buf[:n])
					if len(back) > 0 {
						_, _ = remote.Write(back)
					}
				}
				b.mu.Unlock()
				if len(show) > 0 {
					b.write(show)
				}
			}
			if err != nil {
				ends <- ended{remote, err}
				return
			}
		}
	}()
}

func (b *Bridge) readTerminal(typed chan<- []byte, closed chan<- struct{}) {
	buf := make([]byte, 32*1024)
	for {
		n, err := b.In.Read(buf)
		if n > 0 {
			typed <- bytes.Clone(buf[:n])
		}
		if err != nil {
			close(closed)
			return
		}
	}
}

// typed handles a chunk of typing: the app's control sequences acted on, the rest sent.
func (b *Bridge) typed(chunk []byte, ends chan<- ended) {
	for len(chunk) > 0 {
		at := bytes.Index(chunk, controlPrefix)
		if at < 0 {
			b.send(chunk)
			return
		}
		b.send(chunk[:at])
		rest := chunk[at+len(controlPrefix):]
		bell := bytes.IndexByte(rest, '\a')
		if bell < 0 {
			b.send(chunk[at:]) // not the app's: passed on as typed
			return
		}
		b.control(string(rest[:bell]), ends)
		chunk = rest[bell+1:]
	}
}

func (b *Bridge) send(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	b.mu.Lock()
	remote, tr, released := b.remote, b.tr, b.released
	tell := released && !b.toldReleased
	if tell {
		b.toldReleased = true
	}
	if remote != nil && !released {
		_, _ = remote.Write(tr.FromTerm(chunk))
	}
	b.mu.Unlock()
	if tell {
		b.Note("The port is released: what you type goes nowhere until Reopen Port")
	}
}

func (b *Bridge) control(word string, ends chan<- ended) {
	switch word {
	case Release:
		if !b.Releasable {
			return
		}
		b.mu.Lock()
		remote := b.remote
		already := b.released
		b.remote, b.released, b.toldReleased = nil, true, false
		b.mu.Unlock()
		if already || remote == nil {
			return
		}
		remote.Close()
		b.Note("Released %s: another program may open it now. Reopen Port takes it back", b.Name)
	case Reopen:
		b.mu.Lock()
		released := b.released
		b.mu.Unlock()
		if !b.Releasable || !released {
			return
		}
		remote, tr, err := b.Open()
		if err != nil {
			b.Note("Could not take the port back: %s", err)
			return
		}
		b.attach(remote, tr, ends)
		b.Note("%s", b.Name)
	case Break:
		b.mu.Lock()
		remote := b.remote
		b.mu.Unlock()
		if remote == nil || b.SendBreak == nil {
			return
		}
		if err := b.SendBreak(remote); err != nil {
			b.Note("Could not send a break: %s", err)
		}
	}
}

// Note writes a line of the console's own, dimmed, on a line of its own.
func (b *Bridge) Note(format string, args ...any) {
	b.write([]byte("\r\n\x1b[2m[" + fmt.Sprintf(format, args...) + "]\x1b[0m\r\n"))
}

func (b *Bridge) write(p []byte) {
	b.outMu.Lock()
	defer b.outMu.Unlock()
	_, _ = b.Out.Write(p)
}

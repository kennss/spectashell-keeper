// link.go
// Author: Kennt Kim
// Created: 2026-10-27 · Last updated: 2026-10-30
//
// Purpose: a telnet connection as a terminal's link (termhost.Link): the server's output to the
// terminal, its typing to the server, its size told by NAWS — over a TCP connection the app makes
// itself, with nothing on a host (Docs/design/02-CONNECTIONS.md §6).
//
// Notes:
//   - Nothing keeps a telnet session: the link has no replay (ReplayEnd comes first), and a
//     connection that drops loses the terminal.
//   - The server closing the connection is how a telnet session ends (logout, exit): it is the
//     program's exit, code 0. A read that fails otherwise (reset, unreachable) is the link lost.
//   - The codec is shared by the reader and the typing, under mu; the server's answers and the
//     typing are written under it too, so a reply never lands inside a sequence being typed.

package telnet

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/kennss/spectashell-keeper/keeper"
)

// Link is a telnet session as a terminal's link.
type Link struct {
	conn   net.Conn
	mu     sync.Mutex
	codec  *Codec
	closed sync.Once
}

// Dial connects to address ("host:port") and opens the session for a window of cols × rows.
func Dial(ctx context.Context, address string, cols, rows int) (*Link, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	l, err := Open(conn, cols, rows)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return l, nil
}

// Open starts a session on conn, which the link then owns.
func Open(conn net.Conn, cols, rows int) (*Link, error) {
	codec, hello := NewCodec(cols, rows)
	l := &Link{conn: conn, codec: codec}
	if _, err := conn.Write(hello); err != nil {
		return nil, err
	}
	return l, nil
}

// ReadEvents delivers the server's output until the session ends: the server closing it (the
// program's exit), or the connection failing (returned).
func (l *Link) ReadEvents(ev keeper.Events) error {
	ev.ReplayEnd() // nothing kept: the session is new
	buf := make([]byte, 32*1024)
	for {
		n, err := l.conn.Read(buf)
		if n > 0 {
			l.mu.Lock()
			output, reply := l.codec.Receive(buf[:n])
			var werr error
			if len(reply) > 0 {
				_, werr = l.conn.Write(reply)
			}
			l.mu.Unlock()
			if len(output) > 0 {
				ev.Output(output)
			}
			if werr != nil {
				return werr
			}
		}
		if errors.Is(err, io.EOF) {
			ev.Exit(keeper.Exit{Code: 0})
			return io.EOF
		}
		if err != nil {
			return err
		}
	}
}

// Input sends what the terminal types.
func (l *Link) Input(b []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := l.conn.Write(l.codec.Send(b))
	return err
}

// Resize tells the server the window's new size, once it agreed to hear it.
func (l *Link) Resize(cols, rows int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if msg := l.codec.Resize(cols, rows); msg != nil {
		_, err := l.conn.Write(msg)
		return err
	}
	return nil
}

// SetMeta does nothing: no keeper holds the session.
func (l *Link) SetMeta(keeper.Meta) error { return nil }

// Hangup ends the session: telnet has no signal for it, so the connection closes.
func (l *Link) Hangup(keeper.Who) { l.Close() }

// Close ends the connection.
func (l *Link) Close() {
	l.closed.Do(func() { l.conn.Close() })
}

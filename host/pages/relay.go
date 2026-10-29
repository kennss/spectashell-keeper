// relay.go
// Author: Kennt Kim
// Created: 2026-10-25 · Last updated: 2026-10-30
//
// Purpose: page commands from a host to the device that holds a shell there (Docs/WINDOWS.md
// §3). The core runs `spectashell-keeper pages-relay <device>` over an exec channel of each
// connection to a host whose shells are kept; the relay listens at the device's socket there and
// passes each request through the channel's standard output, and each reply back through its
// standard input, to the core, which answers it (ServeRelay). No port is opened on either side.
//
// Notes:
// - The channel carries JSON envelopes one after another: first the relay's {"ready": <socket>}
//   or {"error": …}, then {"id", "request"} from the relay and {"id", "reply"} from the core.
//   Requests are answered in any order; the id pairs them.
// - The relay ends when its standard input does — the connection ended — and its pending askers
//   are told so; its socket goes with it (Listener.Close), unless another relay took the path.
// - A reply not come within replyTimeout is answered as not come: a `wait` lasts at most 30
//   seconds, and the app answers every other command at once.
// - Moved here from core/internal/pages on 2026-10-29, with the keeper program
//   (Docs/DESIGN.md §3.2).

//go:build unix

package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// replyTimeout is how long a relay waits for the app's reply to a request.
const replyTimeout = 90 * time.Second

// envelope is one message of a relay's channel.
type envelope struct {
	ID      uint64   `json:"id,omitempty"`
	Request *Request `json:"request,omitempty"`
	Reply   *Reply   `json:"reply,omitempty"`
	Ready   string   `json:"ready,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Relay is the relay program: it listens at device name's socket in keepers' pages folder and
// passes requests to out and replies from in, until in ends.
func Relay(keepers, name string, in io.Reader, out io.Writer) error {
	var outMu sync.Mutex
	send := func(e envelope) error {
		outMu.Lock()
		defer outMu.Unlock()
		return json.NewEncoder(out).Encode(e)
	}
	fail := func(err error) error {
		_ = send(envelope{Error: err.Error()})
		return err
	}
	if !ValidName(name) {
		return fail(fmt.Errorf("%q is not a device's name", name))
	}
	dir, err := Dir(keepers)
	if err != nil {
		return fail(err)
	}

	var mu sync.Mutex
	pending := map[uint64]chan Reply{}
	var next uint64
	ended := false
	handle := func(req Request) Reply {
		mu.Lock()
		if ended {
			mu.Unlock()
			return Reply{Error: "The connection to SpectaShell ended."}
		}
		next++
		id, wait := next, make(chan Reply, 1)
		pending[id] = wait
		mu.Unlock()
		defer func() {
			mu.Lock()
			delete(pending, id)
			mu.Unlock()
		}()
		if err := send(envelope{ID: id, Request: &req}); err != nil {
			return Reply{Error: "The connection to SpectaShell ended."}
		}
		select {
		case reply := <-wait:
			return reply
		case <-time.After(replyTimeout):
			return Reply{Error: "SpectaShell did not answer."}
		}
	}

	ln, err := Listen(SocketNamed(dir, name), handle)
	if err != nil {
		return fail(err)
	}
	defer ln.Close()
	if err := send(envelope{Ready: ln.Path()}); err != nil {
		return err
	}

	dec := json.NewDecoder(in)
	var readErr error
	for {
		var e envelope
		if readErr = dec.Decode(&e); readErr != nil {
			break
		}
		if e.Reply == nil {
			continue
		}
		mu.Lock()
		wait := pending[e.ID]
		mu.Unlock()
		if wait != nil {
			wait <- *e.Reply
		}
	}
	mu.Lock()
	ended = true
	for _, wait := range pending {
		select {
		case wait <- Reply{Error: "The connection to SpectaShell ended."}:
		default:
		}
	}
	mu.Unlock()
	if errors.Is(readErr, io.EOF) {
		return nil
	}
	return readErr
}

// ServeRelay is the core's side of a relay's channel: it reads the relay's envelopes from r,
// answers each request with handle and writes the replies to w. ready is called with the relay's
// socket once it listens. It returns when r ends, or with the relay's error.
func ServeRelay(r io.Reader, w io.Writer, handle Handler, ready func(socket string)) error {
	dec := json.NewDecoder(r)
	var first envelope
	if err := dec.Decode(&first); err != nil {
		return fmt.Errorf("the relay did not start: %w", err)
	}
	if first.Error != "" {
		return errors.New(first.Error)
	}
	if first.Ready == "" {
		return errors.New("the relay said nothing of its socket")
	}
	if ready != nil {
		ready(first.Ready)
	}
	// Replies are written one at a time, and none once this returns: the caller closes w then.
	var wMu sync.Mutex
	ended := false
	defer func() {
		wMu.Lock()
		ended = true
		wMu.Unlock()
	}()
	for {
		var e envelope
		if err := dec.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if e.Request == nil {
			continue
		}
		go func(id uint64, req Request) {
			reply := handle(req)
			wMu.Lock()
			defer wMu.Unlock()
			if !ended {
				_ = json.NewEncoder(w).Encode(envelope{ID: id, Reply: &reply})
			}
		}(e.ID, *e.Request)
	}
}

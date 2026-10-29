// events.go
// Author: Kennt Kim
// Created: 2026-10-29 · Last updated: 2026-10-29
//
// Purpose: what a keeper can say about its own end when nothing else can (Docs/KEEPER.md §7): the
// signal that ended it, in its End, and a short log of what happened to it beside its socket, which
// a listing that finds the keeper killed outright keeps in the End it writes for it.
//
// Notes:
// - Found 2026-10-29: a local shell's keeper ended at the moment the app quit and left no End, so a
//   listing could only write "lost"; no crash report and no system log line named it, and it did not
//   happen again in the same steps. A keeper caught no signal: SIGTERM, SIGINT, SIGQUIT or SIGHUP
//   ended it at once, wordless. Now each ends it as before, through finish, with the signal in its
//   End (EndSignalled). SIGKILL cannot be caught; the log's last lines say what the keeper last did.
// - The log is `<id>.log` beside the socket, mode 0600: one line per event — started, a client
//   attached, watching, taken over or gone, a signal, the end — each with its time. It is cut when it
//   passes maxEventLog, keeping the newest half, so a keeper running for months stays small. A keeper
//   that ends by itself removes it (its End says how); a listing that finds a keeper gone without an
//   End copies the log's last lines into the End it writes (End.Events) and removes the log.

//go:build unix

package keeper

import (
	"bytes"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// maxEventLog is the size past which the event log keeps only its newest half.
	maxEventLog = 64 << 10
	// lostEvents is how many of the log's last lines a listing keeps for a keeper found gone.
	lostEvents = 12
)

// EventsPath is a keeper's event log beside its socket.
func EventsPath(socket string) string { return strings.TrimSuffix(socket, ".sock") + ".log" }

// eventLog appends a keeper's events to its log.
type eventLog struct {
	mu   sync.Mutex
	path string
	// done: the keeper's End was written and the log removed; what follows — its program's end,
	// its clients going — is not noted, or the log would be made again.
	done bool
}

// note appends one line, with its time, cutting the log to its newest half when it grew too long.
func (l *eventLog) note(format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return
	}
	line := time.Now().Format("2006-01-02 15:04:05.000") + " " + fmt.Sprintf(format, args...) + "\n"
	if info, err := os.Stat(l.path); err == nil && info.Size()+int64(len(line)) > maxEventLog {
		if b, err := os.ReadFile(l.path); err == nil {
			keep := b[len(b)/2:]
			if i := bytes.IndexByte(keep, '\n'); i >= 0 {
				keep = keep[i+1:]
			}
			_ = os.WriteFile(l.path, keep, 0o600)
		}
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

// remove deletes the log: the keeper's End says how it ended.
func (l *eventLog) remove() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.done = true
	_ = os.Remove(l.path)
}

// lastEvents is the last n lines of the event log at path; nil when there is none.
func lastEvents(path string, n int) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// endSignals are the signals that end a keeper through finish, its End naming them.
var endSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGHUP}

// endOnSignals ends the keeper on the first of endSignals, noting it and naming it in the End.
func (k *keeper) endOnSignals() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, endSignals...)
	sig := (<-ch).(syscall.Signal)
	name := unix.SignalName(sig)
	k.events.note("signal %s: ending", name)
	k.finishWith(End{How: EndSignalled, Signal: name})
}

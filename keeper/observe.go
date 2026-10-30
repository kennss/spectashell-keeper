// observe.go
// Author: Kennt Kim
// Created: 2026-10-30 · Last updated: 2026-10-31
//
// Purpose: what a keeper notices for a user who is away, and who is looking (KEEPER.md §2.2, §4;
// SpectaShell's Docs/design/08-PUSH.md §4): the keeper's own emulator, which reads all the output as
// it comes, reports a status becoming blocked or done (OSC 7501), a command that ended after a time
// (OSC 133, timed by the keeper's clock) and a program's own notification (OSC 9, 777, 99) to an
// Observer the keeper program gives; and a connection says it is in front with a lease (Front), so
// whoever sends a notice can first ask whether anyone is looking.
//
// Notes:
// - The keeper knows nothing of where a notice goes: the observer is the keeper program's
//   (host/push), passed to MainObserved; this package imports nothing of it.
// - Observer.Notice is called with the output lock held, from the output's own path: it must not
//   block. View's methods take the lock: never call them from Notice.
// - A lease runs out by itself: a suspended app, or a device that lost its network, cannot keep a
//   notice back past it. A connection that never sent one is not looking (a client from before
//   Front).

package keeper

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kennss/spectashell-keeper/paneinfo"
	"github.com/kennss/spectashell-keeper/term"
)

// Notice is something a user who is away would want to know.
type Notice struct {
	// Kind: "blocked" (Status says why), "done", "notify" (Title, Body), "command" (Command,
	// Duration).
	Kind     string
	Status   term.Status
	Title    string
	Body     string
	Command  string
	Duration time.Duration
	At       time.Time
}

// Observer hears a keeper's notices.
type Observer interface {
	Notice(Notice)
}

// View is what an observer may ask its keeper, before it sends a notice it held.
type View interface {
	KeeperID() string
	// StartedBy is the client that started the program; nil for one that did not say.
	StartedBy() *Who
	// Title is the last title a client saw (Meta.Title).
	Title() string
	// Program names what runs in front of the terminal now ("python3", "claude"; the shell's name at
	// its prompt), read beside it; empty when it cannot be read.
	Program() string
	// Looking says whether a connection from one of owners' client ids (any, when owners is empty)
	// holds an unexpired lease of being in front.
	Looking(owners []string, now time.Time) bool
	// Status is what the program says it is doing now.
	Status() term.Status
}

// leaseLimit is the longest lease a connection may ask for.
const leaseLimit = 5 * time.Minute

// front is the payload of Front.
type front struct {
	For int `json:"for"` // seconds; 0 says the device went away
}

// MainObserved runs a keeper as Main does, telling the observer observe makes what it notices.
func MainObserved(observe func(View) Observer) int {
	return run(observe)
}

// screenOptions are the options of the emulator that reads all the output as it comes (the
// preview's): with the keeper's clock, and the callbacks an observer needs.
func (k *keeper) screenOptions(cols, rows int) term.Options {
	opts := modesOptions(cols, rows)
	opts.Now = time.Now
	opts.StatusChanged = k.statusChanged
	opts.CommandFinished = k.commandFinished
	opts.Notify = k.notified
	return opts
}

// statusChanged notices a status becoming blocked — or blocked for another reason — or done.
// Called by the screen under the output lock.
func (k *keeper) statusChanged() {
	st := k.screen.Status()
	was := k.noticed
	k.noticed = st
	if k.observer == nil || (st.State == was.State && st.Kind == was.Kind) {
		return
	}
	if st.State == "blocked" || st.State == "done" {
		k.observer.Notice(Notice{Kind: st.State, Status: st, At: time.Now()})
	}
}

func (k *keeper) commandFinished(c term.CommandFinished) {
	// Only a command whose length is known: with the keeper's clock, every one whose start the
	// shell marked, or whose prompt ended (fish).
	if k.observer != nil && c.HasDuration {
		k.observer.Notice(Notice{Kind: "command", Command: c.Command, Duration: c.Duration, At: time.Now()})
	}
}

func (k *keeper) notified(title, body string) {
	if k.observer != nil {
		k.observer.Notice(Notice{Kind: "notify", Title: title, Body: body, Status: k.screen.Status(), At: time.Now()})
	}
}

// lease records a connection's Front.
func (k *keeper) lease(p *peer, payload []byte) {
	var f front
	if json.Unmarshal(payload, &f) != nil || f.For < 0 {
		return
	}
	d := min(time.Duration(f.For)*time.Second, leaseLimit)
	k.out.Lock()
	if d == 0 {
		p.leaseUntil = time.Time{}
	} else {
		p.leaseUntil = time.Now().Add(d)
	}
	k.out.Unlock()
}

// view is a keeper as its observer sees it.
type view struct{ k *keeper }

func (v view) KeeperID() string { return v.k.cfg.KeeperID }

func (v view) StartedBy() *Who {
	v.k.out.Lock()
	defer v.k.out.Unlock()
	if v.k.meta.StartedBy == nil {
		return nil
	}
	who := *v.k.meta.StartedBy
	return &who
}

func (v view) Title() string {
	v.k.out.Lock()
	defer v.k.out.Unlock()
	return v.k.meta.Title
}

func (v view) Program() string {
	v.k.out.Lock()
	directory := v.k.meta.Directory
	v.k.out.Unlock()
	read, err := paneinfo.NewReader().Read(v.k.cmd.Process.Pid, directory)
	if err != nil {
		return ""
	}
	// The command line's first word, as a name: "python3", not its path and arguments.
	fields := strings.Fields(read.Program)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

func (v view) Status() term.Status {
	v.k.out.Lock()
	defer v.k.out.Unlock()
	return v.k.screen.Status()
}

func (v view) Looking(owners []string, now time.Time) bool {
	k := v.k
	k.out.Lock()
	defer k.out.Unlock()
	for _, p := range append([]*peer{k.holder}, k.watchers...) {
		if p == nil || !p.leaseUntil.After(now) {
			continue
		}
		id := p.who.ClientID
		if id == "" && p == k.holder {
			id = k.meta.ClientID
		}
		if len(owners) == 0 || slices.Contains(owners, id) {
			return true
		}
	}
	return false
}

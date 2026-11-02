// observe_test.go
// Author: Kennt Kim
// Created: 2026-10-30 · Last updated: 2026-11-02
//
// Purpose: what a keeper notices for a user who is away (observe.go): a status becoming blocked —
// with why — or done, not again while it stays; a command that ran, timed by the keeper's own clock
// with no client there; a program's own notification; and whether anyone is looking, by the lease
// a connection holds (Front) — none without one, none once it said it went away or once it ran out,
// and only an owner's. The description says the keeper pushes.

package keeper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fileObserver is the helper keeper's observer: each notice, with whether anyone was looking then,
// as a JSON line in path, in the order they came.
type fileObserver struct {
	once    sync.Once
	notices chan Notice
	path    string
	view    View
}

type noticeLine struct {
	Kind, State, StatusKind, Title, Body, Command string
	Duration                                      time.Duration
	Looking, OwnerLooking                         bool
}

func (o *fileObserver) Notice(n Notice) {
	// Written by one goroutine, in order: Notice runs under the keeper's lock, which View's methods
	// take, so it only hands the notice on.
	o.once.Do(func() {
		o.notices = make(chan Notice, 64)
		go o.write()
	})
	o.notices <- n
}

func (o *fileObserver) write() {
	for n := range o.notices {
		now := time.Now()
		line := noticeLine{Kind: n.Kind, State: n.Status.State, StatusKind: n.Status.Kind, Title: n.Title,
			Body: n.Body, Command: n.Command, Duration: n.Duration, Looking: o.view.Looking(nil, now),
			OwnerLooking: o.view.Looking([]string{"owner-device"}, now)}
		b, _ := json.Marshal(line)
		f, err := os.OpenFile(o.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
}

func readNotices(path string) []noticeLine {
	b, _ := os.ReadFile(path)
	var out []noticeLine
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var n noticeLine
		if json.Unmarshal([]byte(line), &n) == nil {
			out = append(out, n)
		}
	}
	return out
}

func observedKeeper(t *testing.T, id string) (*Conn, string, string) {
	t.Helper()
	dir := shortDir(t)
	notices := filepath.Join(dir, "notices")
	t.Setenv("SPECTASHELL_KEEPER_NOTICES", notices)
	c := startKeeper(t, dir, id, []string{"/bin/sh"})
	r := record(c)
	r.wait(t, "the replay", func(_ string, replayEnd int, _ *Exit) bool { return replayEnd >= 0 })
	return c, dir, notices
}

func TestAKeeperNoticesWhatAUserAwayWouldWant(t *testing.T) {
	c, dir, notices := observedKeeper(t, "o1")
	if found := List(dir); len(found) != 1 || !found[0].Push {
		t.Fatalf("the description does not say it pushes: %+v", found)
	}
	if !c.Hello.Push {
		t.Fatal("the hello does not say it pushes")
	}
	_ = c.Input([]byte("printf '\\033]7501;state=blocked:id=main:kind=permission\\007'\n"))
	waitFor(t, "a blocked notice", func() bool { return len(readNotices(notices)) >= 1 })
	// The same status again is not news.
	_ = c.Input([]byte("printf '\\033]7501;state=blocked:id=main:kind=permission\\007'\n"))
	_ = c.Input([]byte("printf '\\033]7501;state=done:id=main\\007'\n"))
	_ = c.Input([]byte("printf '\\033]9;Build finished\\007'\n"))
	// A command the shell marks (OSC 133 C, then D after a second), timed by the keeper itself.
	_ = c.Input([]byte("printf '\\033]133;C\\007'; sleep 1.2; printf '\\033]133;D;0\\007'\n"))
	waitFor(t, "four notices", func() bool { return len(readNotices(notices)) >= 4 })
	got := readNotices(notices)
	kinds := []string{}
	for _, n := range got {
		kinds = append(kinds, n.Kind)
	}
	if strings.Join(kinds, " ") != "blocked done notify command" {
		t.Fatalf("notices: %s", strings.Join(kinds, " "))
	}
	if got[0].StatusKind != "permission" || got[2].Body != "Build finished" || got[3].Duration < time.Second {
		t.Fatalf("notices: %+v", got)
	}
	c.Hangup(Who{})
}

func TestALeaseSaysSomeoneIsLooking(t *testing.T) {
	c, _, notices := observedKeeper(t, "o2")
	blocked := func(kind string) {
		_ = c.Input([]byte("printf '\\033]7501;state=blocked:id=main:kind=" + kind + "\\007'\n"))
	}
	count := 0
	next := func(what string) noticeLine {
		t.Helper()
		count++
		waitFor(t, what, func() bool { return len(readNotices(notices)) >= count })
		return readNotices(notices)[count-1]
	}
	// Frames of one connection are taken in order: a Front sent before the input is in effect when
	// the input's notice comes.
	// No lease: a connection alone is not looking (a client from before Front, a suspended app).
	blocked("permission")
	if n := next("a notice without a lease"); n.Looking {
		t.Fatal("looking without a lease")
	}
	// A lease from a device of the owner's.
	_ = c.SayWho(Who{Client: "iPhone", ClientID: "owner-device"})
	_ = c.Front(90 * time.Second)
	blocked("question")
	if n := next("a notice with a lease"); !n.Looking || !n.OwnerLooking {
		t.Fatalf("not looking with a lease: %+v", n)
	}
	// Gone away.
	_ = c.Front(0)
	blocked("auth")
	if n := next("a notice after going away"); n.Looking {
		t.Fatal("still looking after going away")
	}
	// A teammate's lease is someone looking, but not an owner.
	_ = c.SayWho(Who{Client: "Sam's iPad", ClientID: "teammate"})
	_ = c.Front(90 * time.Second)
	blocked("permission")
	if n := next("a notice with a teammate's lease"); !n.Looking || n.OwnerLooking {
		t.Fatalf("a teammate's lease: %+v", n)
	}
	// A lease runs out by itself: a second's, after more than a second (the one wait on the clock,
	// since running out is the clock's own doing).
	_ = c.Front(time.Second)
	time.Sleep(1500 * time.Millisecond)
	blocked("question")
	if n := next("a notice after the lease ran out"); n.Looking {
		t.Fatal("looking after the lease ran out")
	}
	c.Hangup(Who{})
}

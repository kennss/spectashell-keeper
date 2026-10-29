// join_test.go
// Author: Kennt Kim
// Created: 2026-10-26 · Last updated: 2026-10-30
//
// Purpose: joining a keeper to watch (Docs/KEEPER.md §2, Docs/TEAMS.md §1): a client that joins
// sees the shell without taking it over, and cannot type into it or end it; Take moves the
// keyboard, and the terminal's size, to it, and the one that held it watches — or is taken over,
// when it cannot watch; a client taking the keeper over leaves the watchers watching; a keeper
// from before joining cannot be joined, and is not taken over by the attempt; and a watcher that
// stops reading never holds the shell back.

//go:build unix

package keeper

import (
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// watching collects what a connection delivers, with its Present and its sizes.
type watching struct {
	mu      sync.Mutex
	output  strings.Builder
	present *Present
	sizes   [][2]int // after the replay
	replay  bool     // ReplayEnd came
	err     error
	done    chan struct{}
}

func watch(c *Conn) *watching {
	w := &watching{done: make(chan struct{})}
	go func() {
		defer close(w.done)
		err := c.ReadEvents(Events{
			Output:    func(b []byte) { w.mu.Lock(); w.output.Write(b); w.mu.Unlock() },
			ReplayEnd: func() { w.mu.Lock(); w.replay = true; w.mu.Unlock() },
			Exit:      func(Exit) {},
			Size: func(cols, rows int) {
				w.mu.Lock()
				if w.replay {
					w.sizes = append(w.sizes, [2]int{cols, rows})
				}
				w.mu.Unlock()
			},
			Present: func(p Present) { w.mu.Lock(); w.present = &p; w.mu.Unlock() },
		})
		w.mu.Lock()
		w.err = err
		w.mu.Unlock()
	}()
	return w
}

func (w *watching) until(t *testing.T, what string, ok func(w *watching) bool) {
	t.Helper()
	waitFor(t, what, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return ok(w)
	})
}

func (w *watching) has(text string) func(*watching) bool {
	return func(w *watching) bool { return strings.Contains(w.output.String(), text) }
}

// text is the output so far.
func (w *watching) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.String()
}

func (w *watching) ended() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

// echoShell answers each line typed with "got:<line>".
var echoShell = []string{"/bin/sh", "-c", "echo up; while read x; do echo got:$x; done"}

func TestAJoinerWatchesWithoutTakingOver(t *testing.T) {
	dir := shortDir(t)
	holder := startKeeper(t, dir, "j1", echoShell)
	h := watch(holder)
	h.until(t, "the shell", h.has("up"))

	joiner, err := DialJoin(SocketPath(dir, "j1"))
	if err != nil {
		t.Fatal(err)
	}
	j := watch(joiner)
	_ = joiner.SayWho(Who{Client: "Teammate's iPad", ClientID: "t1"})
	j.until(t, "the replay", j.has("up"))
	j.until(t, "who holds the keyboard", func(w *watching) bool {
		return w.present != nil && !w.present.Holding && w.present.Holder != nil &&
			len(w.present.Watchers) == 1 && w.present.Watchers[0].Client == "Teammate's iPad"
	})
	waitFor(t, "the watcher described", func() bool {
		found := List(dir)
		return len(found) == 1 && found[0].Attached && len(found[0].Watchers) == 1 && found[0].Watchers[0].ClientID == "t1"
	})

	// The holder's typing reaches the program, and both see its answer; the watcher's does not.
	_ = joiner.Input([]byte("from-watcher\n"))
	_ = holder.Input([]byte("from-holder\n"))
	h.until(t, "the holder's answer", h.has("got:from-holder"))
	j.until(t, "the holder's answer, watched", j.has("got:from-holder"))
	if strings.Contains(h.text(), "from-watcher") {
		t.Fatal("a watcher typed into the shell")
	}
	if h.ended() {
		t.Fatalf("the holder was taken over by a joiner: %v", h.err)
	}

	// A watcher hanging up only leaves.
	joiner.Hangup(Who{})
	waitFor(t, "the watcher gone", func() bool {
		found := List(dir)
		return len(found) == 1 && len(found[0].Watchers) == 0
	})
	_ = holder.Input([]byte("still\n"))
	h.until(t, "the shell after the watcher left", h.has("got:still"))
	holder.Hangup(Who{})
}

func TestTakeMovesTheKeyboardAndTheSize(t *testing.T) {
	dir := shortDir(t)
	holder := startKeeper(t, dir, "j2", echoShell)
	h := watch(holder)
	_ = holder.SayWho(Who{Client: "Sam's Mac", ClientID: "m1"})
	h.until(t, "the shell", h.has("up"))

	joiner, err := DialJoin(SocketPath(dir, "j2"))
	if err != nil {
		t.Fatal(err)
	}
	j := watch(joiner)
	_ = joiner.SayWho(Who{Client: "Teammate's iPad", ClientID: "t1"})
	_ = joiner.Resize(50, 20) // remembered while it watches
	j.until(t, "watching", func(w *watching) bool { return w.replay && w.present != nil && !w.present.Holding })
	_ = joiner.Take()

	j.until(t, "holding", func(w *watching) bool { return w.present != nil && w.present.Holding })
	h.until(t, "watching now", func(w *watching) bool {
		return w.present != nil && !w.present.Holding && w.present.Holder != nil && w.present.Holder.ClientID == "t1"
	})
	h.until(t, "the new size", func(w *watching) bool {
		return len(w.sizes) > 0 && w.sizes[len(w.sizes)-1] == [2]int{50, 20}
	})
	if h.ended() {
		t.Fatalf("a holder that can watch was closed: %v", h.err)
	}
	_ = holder.Input([]byte("from-old\n"))
	_ = joiner.Input([]byte("from-new\n"))
	h.until(t, "the new holder's answer", h.has("got:from-new"))
	if strings.Contains(j.text(), "got:from-old") {
		t.Fatal("the old holder still typed into the shell")
	}

	// And back: the Mac takes it again, at its own size once it asks.
	_ = holder.Resize(120, 40)
	_ = holder.Take()
	j.until(t, "watching again, at the Mac's size", func(w *watching) bool {
		return w.present != nil && !w.present.Holding && len(w.sizes) > 0 && w.sizes[len(w.sizes)-1] == [2]int{120, 40}
	})
	holder.Hangup(Who{})
}

func TestTakeFromAClientThatCannotWatchTakesItOver(t *testing.T) {
	dir := shortDir(t)
	holder := startKeeper(t, dir, "j3", echoShell) // an earlier build: it never says Who
	h := watch(holder)
	h.until(t, "the shell", h.has("up"))
	joiner, err := DialJoin(SocketPath(dir, "j3"))
	if err != nil {
		t.Fatal(err)
	}
	j := watch(joiner)
	j.until(t, "the replay", func(w *watching) bool { return w.replay })
	_ = joiner.Take()
	<-h.done
	if !errors.Is(h.err, ErrTakenOver) {
		t.Fatalf("the holder ended with %v, want taken over", h.err)
	}
	_ = joiner.Input([]byte("mine\n"))
	j.until(t, "the joiner's answer", j.has("got:mine"))
	joiner.Hangup(Who{})
}

func TestTakingOverLeavesTheWatchers(t *testing.T) {
	dir := shortDir(t)
	holder := startKeeper(t, dir, "j4", echoShell)
	h := watch(holder)
	h.until(t, "the shell", h.has("up"))
	joiner, err := DialJoin(SocketPath(dir, "j4"))
	if err != nil {
		t.Fatal(err)
	}
	j := watch(joiner)
	j.until(t, "the replay", func(w *watching) bool { return w.replay })

	// The same person's other device opens the shell: the holder lets go, the watcher stays.
	other, err := Dial(SocketPath(dir, "j4"))
	if err != nil {
		t.Fatal(err)
	}
	o := watch(other)
	<-h.done
	if !errors.Is(h.err, ErrTakenOver) {
		t.Fatalf("the holder ended with %v, want taken over", h.err)
	}
	_ = other.Input([]byte("moved\n"))
	o.until(t, "the new device's answer", o.has("got:moved"))
	j.until(t, "the new device's answer, watched", j.has("got:moved"))
	if j.ended() {
		t.Fatalf("the watcher was dropped: %v", j.err)
	}
	other.Hangup(Who{})
}

func TestAKeeperFromBeforeJoiningCannotBeJoined(t *testing.T) {
	dir := shortDir(t)
	// An earlier keeper listens on its first socket only.
	ln, err := net.Listen("unix", SocketPath(dir, "j5"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- struct{}{}
			c.Close()
		}
	}()
	if _, err := DialJoin(SocketPath(dir, "j5")); !errors.Is(err, ErrCannotJoin) {
		t.Fatalf("joining an earlier keeper: %v, want ErrCannotJoin", err)
	}
	select {
	case <-accepted:
		t.Fatal("joining connected to the earlier keeper, which takes it over")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := DialJoin(SocketPath(dir, "none")); err == nil || errors.Is(err, ErrCannotJoin) {
		t.Fatalf("joining no keeper: %v", err)
	}
}

// A watcher that stops reading — a teammate on a slow link — is dropped when it falls behind, and
// the holder's shell runs on at its own pace.
func TestASlowWatcherDoesNotHoldTheShellBack(t *testing.T) {
	dir := shortDir(t)
	holder := startKeeper(t, dir, "j6", []string{"/bin/sh", "-c",
		"read x; head -c 12000000 /dev/zero | tr '\\0' x; echo; echo all-written; sleep 30"})
	h := watch(holder)
	joiner, err := DialJoin(SocketPath(dir, "j6")) // read by no one
	if err != nil {
		t.Fatal(err)
	}
	defer joiner.Close()
	waitFor(t, "the watcher described", func() bool {
		found := List(dir)
		return len(found) == 1 && len(found[0].Watchers) == 1
	})
	_ = holder.Input([]byte("go\n"))
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(h.text(), "all-written") {
		if time.Now().After(deadline) {
			t.Fatal("the holder's output stalled behind a watcher that does not read")
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitFor(t, "the watcher dropped", func() bool {
		found := List(dir)
		return len(found) == 1 && len(found[0].Watchers) == 0
	})
	holder.Hangup(Who{})
}

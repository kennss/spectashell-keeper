// resize_test.go
// Author: Kennt Kim
// Created: 2026-11-02 · Last updated: 2026-11-02
//
// Purpose: resizes that come one after another reach the program as two — the first and the last
// — not one per frame of an animation (termhost.go's notes, 2026-11-02: the iPad's side panel
// sliding in told a shell twelve sizes in a quarter of a second); a resize alone still goes at
// once; and one still waiting when the terminal is closed is dropped.
//
// Notes:
// - The terminal sits on a link of its own (sizeLink) that records each size it is told, and its
//   waits run on a clock of the test's own (testClock), so nothing waits on the wall clock.

//go:build unix

package termhost

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kennss/spectashell-keeper/keeper"
)

// sizeLink is a link that sends an empty replay and records the sizes it is told.
type sizeLink struct {
	mu    sync.Mutex
	sizes [][2]int
	done  chan struct{}
}

func (l *sizeLink) ReadEvents(ev keeper.Events) error {
	ev.ReplayEnd()
	<-l.done
	return nil
}
func (l *sizeLink) Input([]byte) error        { return nil }
func (l *sizeLink) SetMeta(keeper.Meta) error { return nil }
func (l *sizeLink) Hangup(keeper.Who)         { close(l.done) }
func (l *sizeLink) Close()                    { close(l.done) }
func (l *sizeLink) Resize(cols, rows int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sizes = append(l.sizes, [2]int{cols, rows})
	return nil
}

func (l *sizeLink) told() [][2]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.sizes)
}

// testClock holds the terminal's waits until the test lets them pass.
type testClock struct {
	mu      sync.Mutex
	waiting []func()
}

func (c *testClock) after(_ time.Duration, f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waiting = append(c.waiting, f)
}

// pass runs every wait begun so far, as if resizeSettle had gone by.
func (c *testClock) pass() {
	c.mu.Lock()
	waiting := c.waiting
	c.waiting = nil
	c.mu.Unlock()
	for _, f := range waiting {
		f()
	}
}

func attachSized(t *testing.T) (*Terminal, *sizeLink, *testClock) {
	t.Helper()
	link := &sizeLink{done: make(chan struct{})}
	clock := &testClock{}
	term := Attach(link, LinkInfo{Columns: 100, Rows: 32}, Config{OnChange: func() {}})
	term.after = clock.after
	return term, link, clock
}

func screenSize(term *Terminal) [2]int {
	term.mu.Lock()
	defer term.mu.Unlock()
	return [2]int{term.emu.Columns(), term.emu.Rows()}
}

func TestResizesOneAfterAnotherReachTheProgramAsTheFirstAndTheLast(t *testing.T) {
	term, link, clock := attachSized(t)
	defer term.Close()
	for _, cols := range []int{96, 92, 88, 84, 80, 76, 74} {
		term.Resize(cols, 32)
	}
	if got, want := link.told(), [][2]int{{96, 32}}; !slices.Equal(got, want) {
		t.Fatalf("while the panel moves the program was told %v, want only the first, %v", got, want)
	}
	if got := screenSize(term); got != [2]int{96, 32} {
		t.Fatalf("the screen is %v while the program is 96 × 32; they must agree", got)
	}
	clock.pass()
	if got, want := link.told(), [][2]int{{96, 32}, {74, 32}}; !slices.Equal(got, want) {
		t.Fatalf("once settled the program was told %v, want %v", got, want)
	}
	if got := screenSize(term); got != [2]int{74, 32} {
		t.Fatalf("once settled the screen is %v, want 74 × 32", got)
	}
}

func TestAResizeAloneGoesAtOnceAndOnlyOnce(t *testing.T) {
	term, link, clock := attachSized(t)
	defer term.Close()
	term.Resize(80, 24)
	if got, want := link.told(), [][2]int{{80, 24}}; !slices.Equal(got, want) {
		t.Fatalf("a split's resize told the program %v, want %v at once", got, want)
	}
	clock.pass()
	if got, want := link.told(), [][2]int{{80, 24}}; !slices.Equal(got, want) {
		t.Fatalf("after the wait the program was told %v, want nothing more than %v", got, want)
	}
	// The wait over, the next resize goes at once again.
	term.Resize(90, 24)
	if got, want := link.told(), [][2]int{{80, 24}, {90, 24}}; !slices.Equal(got, want) {
		t.Fatalf("the next resize told the program %v, want %v", got, want)
	}
}

func TestAResizeStillWaitingWhenTheTerminalClosesIsDropped(t *testing.T) {
	term, link, clock := attachSized(t)
	term.Resize(96, 32)
	term.Resize(74, 32)
	term.Detach()
	clock.pass()
	if got, want := link.told(), [][2]int{{96, 32}}; !slices.Equal(got, want) {
		t.Fatalf("after Detach the program was told %v, want only %v", got, want)
	}
}

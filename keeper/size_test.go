// size_test.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-25
//
// Purpose: the kept output remembers the terminal's size it was written at (Docs/KEEPER.md §2):
// a replay sends each stretch after its size, ending with the size now; a size that came and went
// with nothing written at it leaves no mark; and when the kept output is trimmed, the size in
// effect where it was cut becomes the first, and the modes emulator read the cut output at the
// sizes it was written at.

//go:build unix

package keeper

import (
	"bytes"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// sizedPart is a stretch of a replay as a client hears it.
type sizedPart struct {
	cols, rows int
	out        string
}

func TestAReplayCarriesTheSizesItWasWrittenAt(t *testing.T) {
	dir := shortDir(t)
	c := startKeeper(t, dir, "z1", []string{"/bin/sh", "-c", "echo before; read x; echo after; sleep 30"})
	r := record(c)
	r.wait(t, "the first line", func(out string, _ int, _ *Exit) bool { return strings.Contains(out, "before") })
	_ = c.Resize(50, 20)
	_ = c.Input([]byte("go\n"))
	r.wait(t, "the second line", func(out string, _ int, _ *Exit) bool { return strings.Contains(out, "after") })
	// Two sizes with nothing written at them: only the last is the size now.
	_ = c.Resize(60, 20)
	_ = c.Resize(70, 21)
	// The keeper has read every frame above once it answers the next connection; a frame it reads
	// in order after them is the Meta, seen in its description.
	_ = c.SetMeta(Meta{Owner: "test-owner", Persist: true, Title: "resized"})
	waitFor(t, "the meta after the resizes", func() bool {
		found := List(dir)
		return len(found) == 1 && found[0].Meta.Title == "resized"
	})
	c.Close()

	again, err := Dial(SocketPath(dir, "z1"))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Hangup(Who{})
	if again.Hello.Columns != 70 || again.Hello.Rows != 21 {
		t.Fatalf("Hello size = %d × %d", again.Hello.Columns, again.Hello.Rows)
	}
	var mu sync.Mutex
	var parts []sizedPart
	ended := make(chan struct{})
	go func() {
		_ = again.ReadEvents(Events{
			Size: func(cols, rows int) {
				mu.Lock()
				parts = append(parts, sizedPart{cols: cols, rows: rows})
				mu.Unlock()
			},
			Output: func(b []byte) {
				mu.Lock()
				if len(parts) > 0 {
					parts[len(parts)-1].out += string(b)
				}
				mu.Unlock()
			},
			ReplayEnd: func() { close(ended) },
			Exit:      func(Exit) {},
		})
	}()
	<-ended
	mu.Lock()
	defer mu.Unlock()
	if len(parts) != 3 {
		t.Fatalf("the replay came in %d parts: %+v", len(parts), parts)
	}
	got := [][2]int{{parts[0].cols, parts[0].rows}, {parts[1].cols, parts[1].rows}, {parts[2].cols, parts[2].rows}}
	if !reflect.DeepEqual(got, [][2]int{{80, 24}, {50, 20}, {70, 21}}) {
		t.Fatalf("sizes = %v", got)
	}
	if !strings.Contains(parts[0].out, "before") || strings.Contains(parts[0].out, "after") ||
		!strings.Contains(parts[1].out, "after") || parts[2].out != "" {
		t.Fatalf("parts = %+v", parts)
	}
}

func TestTrimmedOutputKeepsTheSizeWhereItWasCut(t *testing.T) {
	k := &keeper{cols: 80, rows: 24, modes: newModes(80, 24), screen: newModes(80, 24), sizes: []sizeMark{{cols: 80, rows: 24}}}
	line := bytes.Repeat([]byte("x"), 99)
	line = append(line, '\n')
	for i := 0; i < 15*1024; i++ { // 1.5 MiB at 80 × 24
		k.keep(line)
	}
	k.markSize(40, 10)
	k.markSize(80, 24) // back again, nothing written at 40 × 10
	if len(k.sizes) != 1 {
		t.Fatalf("sizes = %+v, want the first only", k.sizes)
	}
	k.markSize(40, 10)
	for i := 0; i < 10*1024; i++ { // 1 MiB more: output before the mark is cut
		k.keep(line)
	}
	const cut = (2*keptBytes/100+1)*100 - keptBytes // the first length past 2 MiB, less the 1 MiB kept
	if !reflect.DeepEqual(k.sizes, []sizeMark{{at: 0, cols: 80, rows: 24}, {at: 1_536_000 - cut, cols: 40, rows: 10}}) {
		t.Fatalf("sizes after the first cut = %+v", k.sizes)
	}
	if k.modes.Columns() != 80 {
		t.Fatalf("the modes emulator read the cut output at %d columns, want 80", k.modes.Columns())
	}
	for i := 0; i < 10*1024; i++ { // and 1 MiB more: the cut passes the mark
		k.keep(line)
	}
	if !reflect.DeepEqual(k.sizes, []sizeMark{{at: 0, cols: 40, rows: 10}}) {
		t.Fatalf("sizes after the second cut = %+v", k.sizes)
	}
	if k.modes.Columns() != 40 {
		t.Fatalf("the modes emulator read the output after the mark at %d columns, want 40", k.modes.Columns())
	}
	parts := k.keptOutput()
	if len(parts) != 1 || parts[0].cols != 40 || parts[0].rows != 10 {
		t.Fatalf("replay parts = %d, first %d × %d", len(parts), parts[0].cols, parts[0].rows)
	}
}

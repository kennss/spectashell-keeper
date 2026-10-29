// triggers_test.go
// Author: Kennt Kim
// Created: 2026-10-20 · Last updated: 2026-10-23
//
// Purpose: triggers (triggers.go): a finished line that matches is reported and its text
// highlighted, the program's colours kept; a wrapped line is matched whole, once, at its end, and
// highlighted on both rows; a progress bar rewritten in place is matched as it reads when left; a
// line left again unchanged is not matched again; nothing on the alternate screen; a wide
// character is highlighted in both its cells; a prompt's line (the command typed) is not read; no
// trigger, nothing read.
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/triggers_test.go on 2026-10-23.

package term

import (
	"regexp"
	"slices"
	"testing"
)

type triggerFixture struct {
	*fixture
	hits []Triggered
}

func newTriggerFixture(t *testing.T, cols, rows int, triggers ...Trigger) *triggerFixture {
	f := &triggerFixture{fixture: &fixture{t: t}}
	f.term = New(Options{Columns: cols, Rows: rows, Triggered: func(h Triggered) { f.hits = append(f.hits, h) }})
	f.term.SetTriggers(triggers)
	return f
}

// highlighted is the columns of screen row y drawn highlighted.
func (f *triggerFixture) highlighted(y int) []int {
	var out []int
	for x, c := range f.term.Line(y).Cells {
		if f.term.Style(c.Style).Attrs&AttrHighlight != 0 {
			out = append(out, x)
		}
	}
	return out
}

func span(from, to int) []int {
	var out []int
	for x := from; x < to; x++ {
		out = append(out, x)
	}
	return out
}

func TestTriggersMatchFinishedLines(t *testing.T) {
	oom := Trigger{ID: "oom", Pattern: regexp.MustCompile(`CUDA out of memory`), Highlight: true, Notify: true}
	quiet := Trigger{ID: "warn", Pattern: regexp.MustCompile(`(?i)warning`), Highlight: true}
	f := newTriggerFixture(t, 30, 6, oom, quiet)

	// Not finished yet: nothing. Finished: reported, highlighted, its colour kept.
	f.write("x \x1b[31mCUDA out of memory\x1b[0m")
	if len(f.hits) != 0 || len(f.highlighted(0)) != 0 {
		t.Fatalf("matched before the line ended: %v", f.hits)
	}
	f.write("\r\n")
	if !slices.Equal(f.hits, []Triggered{{ID: "oom", Line: "x CUDA out of memory"}}) {
		t.Fatalf("hits %v", f.hits)
	}
	if got := f.highlighted(0); !slices.Equal(got, span(2, 20)) {
		t.Fatalf("highlighted %v", got)
	}
	if s := f.term.Style(f.term.Line(0).Cells[2].Style); s.Foreground != IndexedColor(1) {
		t.Fatalf("the program's colour lost: %+v", s)
	}

	// A trigger that only highlights reports nothing.
	f.write("a Warning here\r\n")
	if len(f.hits) != 1 || !slices.Equal(f.highlighted(1), span(2, 9)) {
		t.Fatalf("hits %v highlighted %v", f.hits, f.highlighted(1))
	}

	// Left again unchanged (the cursor back up, a line feed): not matched again.
	f.write("\x1b[2A\n\n")
	if len(f.hits) != 1 {
		t.Fatalf("matched again: %v", f.hits)
	}

	// A progress bar rewritten in place is matched as it reads when it is left.
	f.write("loading 10%\rCUDA out of memory 0%\r\n")
	if len(f.hits) != 2 || f.hits[1].Line != "CUDA out of memory 0%" {
		t.Fatalf("hits %v", f.hits)
	}

	// Nothing on the alternate screen.
	f.write("\x1b[?1049hCUDA out of memory\r\n\x1b[?1049l")
	if len(f.hits) != 2 {
		t.Fatalf("matched on the alternate screen: %v", f.hits)
	}
}

func TestTriggersReadAWrappedLineWhole(t *testing.T) {
	f := newTriggerFixture(t, 10, 6, Trigger{ID: "tb", Pattern: regexp.MustCompile(`Traceback \(most`),
		Highlight: true, Notify: true})
	f.write("Traceback (most recent call last):")
	if len(f.hits) != 0 {
		t.Fatalf("matched at a wrap: %v", f.hits)
	}
	f.write("\r\n")
	if len(f.hits) != 1 || f.hits[0].Line != "Traceback (most recent call last):" {
		t.Fatalf("hits %v", f.hits)
	}
	// "Traceback (most" is columns 0-9 of the first row and 0-4 of the second.
	if !slices.Equal(f.highlighted(0), span(0, 10)) || !slices.Equal(f.highlighted(1), span(0, 5)) {
		t.Fatalf("highlighted %v %v", f.highlighted(0), f.highlighted(1))
	}
}

func TestTriggersHighlightWideCharacters(t *testing.T) {
	f := newTriggerFixture(t, 20, 3, Trigger{ID: "ko", Pattern: regexp.MustCompile(`오류`), Highlight: true})
	f.write("a 오류 b\r\n")
	if got := f.highlighted(0); !slices.Equal(got, span(2, 6)) {
		t.Fatalf("highlighted %v", got)
	}
}

func TestNoTriggersReadNothing(t *testing.T) {
	f := newTriggerFixture(t, 20, 3)
	f.write("CUDA out of memory\r\n")
	if len(f.hits) != 0 || f.term.Line(0).checked != 0 {
		t.Fatal("read without triggers")
	}
}

func TestTriggersSkipTheCommandTyped(t *testing.T) {
	f := newTriggerFixture(t, 40, 4, Trigger{ID: "oom", Pattern: regexp.MustCompile(`CUDA out of memory`), Notify: true})
	f.write("\x1b]133;A\x07$ \x1b]133;B\x07grep 'CUDA out of memory' log\r\n\x1b]133;C\x07log: CUDA out of memory\r\n")
	if len(f.hits) != 1 || f.hits[0].Line != "log: CUDA out of memory" {
		t.Fatalf("hits %v", f.hits)
	}
}

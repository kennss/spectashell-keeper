// triggers.go
// Author: Kennt Kim
// Created: 2026-10-20 · Last updated: 2026-10-23
//
// Purpose: triggers (Docs/SDTERM-2.md §9.2, PROTOCOL.md §7.10) — patterns looked for in each line a
// program finishes: the text they match is highlighted (AttrHighlight, drawn by the device in its
// highlight colours), and a trigger that notifies is reported (Options.Triggered) for the app to
// tell the user ("CUDA out of memory" in a training run left alone).
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/triggers.go on 2026-10-23.
// - A line is finished when the cursor leaves it by a line feed (LF, VT, FF, IND, NEL) — not by
//   wrapping, so a line the terminal wrapped is matched whole, once, at its end. A line a program
//   only rewrites in place (a progress bar's carriage returns) is matched when it is left, as it
//   then reads. A line is matched again only when it changed since (its Version).
// - A line a prompt begins on (shell integration's mark) is the command typed, not output: not read.
// - Main screen only: full-screen programs (an editor, Claude Code) redraw their screen all the
//   time, and every redraw would match again.
// - A wrapped line is read from the screen rows it covers, at most maxTriggerRows; rows already in
//   the history are not read.
// - Highlighting changes the cells' styles (the program's colours kept), so it reaches every device
//   in frames and stays in the history.

package term

import (
	"regexp"
	"strings"
)

// maxTriggerRows bounds the rows of one wrapped line that triggers read.
const maxTriggerRows = 64

// AttrHighlight marks text a trigger matched (TerminalStyleFlag HIGHLIGHT). Programs cannot set it.
const AttrHighlight Attr = 1 << 8

// Trigger is a pattern looked for in each line a program finishes.
type Trigger struct {
	// ID names it in Triggered.
	ID      string
	Pattern *regexp.Regexp
	// Highlight: the text it matches is highlighted. Notify: each line it matches is reported.
	Highlight, Notify bool
}

// Triggered is a line that matched a trigger that notifies (Options.Triggered).
type Triggered struct {
	ID string
	// The line's text, trailing blanks trimmed.
	Line string
}

// SetTriggers replaces the triggers.
func (t *Terminal) SetTriggers(triggers []Trigger) {
	t.triggers = triggers
}

// checkTriggers matches the line ending at screen row y against the triggers.
func (t *Terminal) checkTriggers(y int) {
	last := t.lines[y]
	if len(t.triggers) == 0 || t.alt || last.Wrapped || last.Version == last.checked {
		return
	}
	first := y
	for first > 0 && y-first < maxTriggerRows-1 && t.lines[first-1].Wrapped {
		first--
	}
	// A prompt's line holds the command typed, not a program's output (`grep "CUDA out of memory"`).
	if m := t.lines[first].Mark; m != nil && m.Prompt {
		last.checked = last.Version
		return
	}
	// The line's text, and for each byte of it the cell it came from.
	type at struct{ row, col int }
	var b strings.Builder
	var cells []at
	for r := first; r <= y; r++ {
		for x, c := range t.lines[r].Cells {
			if c.Flags&CellWideTail != 0 {
				continue
			}
			s := t.cellText(c)
			if s == "" {
				s = " "
			}
			b.WriteString(s)
			for range len(s) {
				cells = append(cells, at{r, x})
			}
		}
	}
	text := strings.TrimRight(b.String(), " ")
	if text == "" {
		last.checked = last.Version
		return
	}
	changed := map[int]bool{}
	for _, tr := range t.triggers {
		matches := tr.Pattern.FindAllStringIndex(text, -1)
		if len(matches) == 0 {
			continue
		}
		if tr.Highlight {
			for _, m := range matches {
				for i := m[0]; i < m[1]; i++ {
					if i > m[0] && cells[i] == cells[i-1] {
						continue // the rest of a character's bytes
					}
					p := cells[i]
					line := t.lines[p.row]
					style := t.styles.get(line.Cells[p.col].Style)
					if style.Attrs&AttrHighlight != 0 {
						continue
					}
					style.Attrs |= AttrHighlight
					id := t.styles.intern(style)
					line.Cells[p.col].Style = id
					if line.Cells[p.col].Flags&CellWide != 0 && p.col+1 < len(line.Cells) {
						line.Cells[p.col+1].Style = id
					}
					changed[p.row] = true
				}
			}
		}
		if tr.Notify && t.onTriggered != nil {
			t.onTriggered(Triggered{ID: tr.ID, Line: text})
		}
	}
	for r := range changed {
		t.touch(t.lines[r])
	}
	last.checked = last.Version
}

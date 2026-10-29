// screen.go
// Author: Kennt Kim
// Created: 2026-10-14 · Last updated: 2026-10-23
//
// Purpose: the storage of a terminal: cells, lines (with a version for change tracking) and the
// history ring.
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/screen.go on 2026-10-23.
// - Cell.Ch == 0 is a blank cell (drawn as a space); a written space is ' '. Copying text trims
//   trailing blanks of either kind.
// - A wide character occupies two cells: the first has CellWide, the second CellWideTail and
//   Ch == 0. Code that overwrites either half clears the other.
// - Line.Version changes whenever anything in the line changes (Terminal.touch). Frame building
//   compares versions instead of cells: a line that only moved (scrolling) keeps its version.
// - Lines are shared by pointer between the screen and the history, so scrolling moves pointers;
//   a line entering the history is trimmed of trailing blank default cells and never changes
//   again.
// - The history is a ring of at most `limit` lines; index 0 is the oldest kept line.
// - Line.Mark is shell integration's (prompts.go): set on the line where a prompt, a command's
//   output or its end begins; replaced, never changed in place, so the history may share it.

package term

// Cell flags.
const (
	// CellWide marks the first half of a two-column character.
	CellWide uint8 = 1 << iota
	// CellWideTail marks the second half of a two-column character.
	CellWideTail
	// CellCluster means Ch is a cluster id: the cell holds several characters.
	CellCluster
)

// Cell is one column of one line.
type Cell struct {
	// Ch is the character, 0 for a blank cell, or a cluster id when Flags&CellCluster != 0.
	Ch    rune
	Style uint32
	Flags uint8
}

// IsBlank reports whether the cell shows nothing (it may still have a background).
func (c Cell) IsBlank() bool { return c.Ch == 0 && c.Flags&CellCluster == 0 }

// Line is one row of cells.
type Line struct {
	Cells []Cell
	// Wrapped means the line continues on the next one (soft wrap).
	Wrapped bool
	Version uint64
	// Mark is what shell integration says begins or ends on the line; nil: nothing.
	Mark *Mark
	// checked is the Version the triggers last read (triggers.go).
	checked uint64
}

func newLine(cols int, blank Cell, version uint64) *Line {
	l := &Line{Cells: make([]Cell, cols), Version: version}
	if blank != (Cell{}) {
		for i := range l.Cells {
			l.Cells[i] = blank
		}
	}
	return l
}

// trimmed returns a copy of the line without trailing blank default-style cells, for the history.
func (l *Line) trimmed() *Line {
	n := len(l.Cells)
	for n > 0 && l.Cells[n-1] == (Cell{}) {
		n--
	}
	out := &Line{Wrapped: l.Wrapped, Version: l.Version, Mark: l.Mark}
	if n > 0 {
		out.Cells = make([]Cell, n)
		copy(out.Cells, l.Cells[:n])
	}
	return out
}

// history is a ring buffer of lines.
type history struct {
	lines []*Line
	start int // index in lines of the oldest line
	count int
	limit int
}

func newHistory(limit int) *history {
	return &history{limit: limit}
}

func (h *history) push(l *Line) {
	if h.limit <= 0 {
		return
	}
	if h.count < h.limit {
		if len(h.lines) < h.limit {
			h.lines = append(h.lines, l)
		} else {
			h.lines[(h.start+h.count)%h.limit] = l
		}
		h.count++
		return
	}
	h.lines[h.start] = l
	h.start = (h.start + 1) % h.limit
}

func (h *history) at(i int) *Line {
	if i < 0 || i >= h.count {
		return nil
	}
	return h.lines[(h.start+i)%len(h.lines)]
}

func (h *history) clear() {
	h.lines = nil
	h.start = 0
	h.count = 0
}

func (h *history) each(f func(*Line)) {
	for i := 0; i < h.count; i++ {
		f(h.at(i))
	}
}

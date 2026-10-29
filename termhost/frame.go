// frame.go
// Author: Kennt Kim
// Created: 2026-10-14 · Last updated: 2026-10-29
//
// Purpose: building TerminalFrames — what changed on a terminal since the client last heard
// about it (spec/PROTOCOL.md §7.3).
//
// Notes:
// - Taken from SiliconDesk's host/internal/termhost/frame.go on 2026-10-23.
// - Sync is what one client stream has of one terminal: the version of each screen row it
//   shows, the emulator's scroll count at the last frame, which history lines it holds
//   (absolute line numbers [oldest, scrolled)), cursor, modes and bells.
// - Rows are compared by line version. Scrolling shifts both the client's rows and Sync.vers by
//   the number of lines scrolled, so lines that only moved are not sent again.
// - History lines are numbered absolutely: line i of the emulator's history is number
//   Scrolled() - HistoryLen() + i.
// - A line's shell-integration mark (term.Mark) travels with it, so a mark set on a screen row
//   changes that row's version and the row is sent again.
// - The first frame (start_over) carries the newest firstHistoryChunk history lines; later frames add
//   older ones, historyChunk at a time, until the client has the whole history.

//go:build unix

package termhost

import (
	"strings"

	pb "github.com/kennss/spectashell-keeper/gen/terminal/v1"
	"github.com/kennss/spectashell-keeper/term"
)

const (
	firstHistoryChunk = 200
	historyChunk      = 1000
)

// Sync is one client's copy of a terminal, as far as the session knows it. The zero value is a
// client that has nothing (its first frame starts over).
type Sync struct {
	started  bool
	cols     int
	rows     int
	vers     []uint64
	scrolled uint64
	clears   uint64
	oldest   uint64
	cursor   term.Cursor
	mouse    term.MouseTracking
	alt      bool
	bells    uint64

	// Pacing, owned by the session (PROTOCOL.md §7.4).
	Seq   uint64
	Acked uint64
}

// Frame returns the next frame for s, or nil when the client is up to date. It updates s as if
// the frame was delivered. more reports that older history is still to be sent, so another
// frame should follow even if nothing changes.
func (t *Terminal) Frame(s *Sync) (f *pb.TerminalFrame, more bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.emu
	cols, rows := e.Columns(), e.Rows()
	b := frameBuilder{emu: e, styleIndex: map[uint32]uint32{}}
	f = &pb.TerminalFrame{Columns: uint32(cols), Rows: uint32(rows)}
	changed := false

	scrolled := e.Scrolled()
	histLen := uint64(e.HistoryLen())
	firstAbs := scrolled - histLen // absolute number of the oldest kept history line

	if !s.started {
		*s = Sync{started: true, Seq: s.Seq, Acked: s.Acked, scrolled: scrolled, clears: e.HistoryClears(),
			oldest: scrolled}
		f.StartOver = true
		f.HistoryLimit = uint32(e.HistoryLimit())
		changed = true
	}

	// History cleared: the client drops its history; lines added since are appended below.
	if c := e.HistoryClears(); c != s.clears {
		s.clears = c
		f.HistoryCleared = true
		s.oldest = scrolled
		changed = true
	}

	// Lines that scrolled into the history since the last frame.
	if k := scrolled - s.scrolled; k > 0 {
		f.Scrolled = k
		n := min(k, histLen)
		for abs := scrolled - n; abs < scrolled; abs++ {
			f.HistoryAppend = append(f.HistoryAppend, b.line(e.HistoryLine(int(abs-firstAbs))))
		}
		if f.HistoryCleared {
			s.oldest = scrolled - n
		}
		// Shift the client's rows as the client will.
		if len(s.vers) > 0 {
			shift := int(min(k, uint64(len(s.vers))))
			copy(s.vers, s.vers[shift:])
			for i := len(s.vers) - shift; i < len(s.vers); i++ {
				s.vers[i] = 0
			}
		}
		s.scrolled = scrolled
		changed = true
	}
	s.oldest = max(s.oldest, firstAbs) // lines the emulator dropped are gone for the client too

	// Older history the client does not have yet.
	if s.oldest > firstAbs {
		chunk := uint64(historyChunk)
		if f.StartOver {
			chunk = firstHistoryChunk
		}
		from := max(firstAbs, s.oldest-min(chunk, s.oldest))
		for abs := from; abs < s.oldest; abs++ {
			f.HistoryPrepend = append(f.HistoryPrepend, b.line(e.HistoryLine(int(abs-firstAbs))))
		}
		s.oldest = from
		changed = true
	}

	// Screen rows.
	if s.cols != cols || s.rows != rows || len(s.vers) != rows {
		s.cols, s.rows = cols, rows
		s.vers = make([]uint64, rows)
	}
	for y := 0; y < rows; y++ {
		l := e.Line(y)
		if s.vers[y] == l.Version {
			continue
		}
		s.vers[y] = l.Version
		f.ChangedRows = append(f.ChangedRows, &pb.TerminalRow{Index: uint32(y), Line: b.line(l)})
		changed = true
	}

	cur := e.Cursor()
	if cur != s.cursor {
		s.cursor = cur
		changed = true
	}
	f.Cursor = &pb.TerminalCursor{Column: uint32(cur.Column), Row: uint32(cur.Row), Hidden: cur.Hidden,
		Shape: cursorShape(cur.Shape), Blink: cur.Blink}

	if m, alt := e.Mouse(), e.AltScreen(); m != s.mouse || alt != s.alt {
		s.mouse, s.alt = m, alt
		changed = true
	}
	f.Modes = &pb.TerminalModes{Mouse: pb.TerminalMouseTracking(s.mouse), AlternateScreen: s.alt}

	if bells := e.Bells(); bells != s.bells {
		f.Bells = uint32(min(bells-s.bells, 1000))
		s.bells = bells
		changed = true
	}

	more = s.oldest > firstAbs
	if !changed {
		return nil, more
	}
	f.Styles = b.styles
	return f, more
}

// mark encodes a line's shell-integration mark (nil: none).
func mark(m *term.Mark) *pb.TerminalMark {
	if m == nil {
		return nil
	}
	out := &pb.TerminalMark{Prompt: m.Prompt, Output: m.Output, OutputColumn: uint32(m.OutputColumn),
		Command: m.Command, Directory: m.Directory}
	if e := m.End; e != nil {
		out.End = &pb.TerminalCommandEnd{Column: uint32(e.Column)}
		if e.HasExitCode {
			code := int32(e.ExitCode)
			out.End.ExitCode = &code
		}
		if e.HasDuration {
			ms := uint64(e.Duration.Milliseconds())
			out.End.DurationMs = &ms
		}
	}
	return out
}

func cursorShape(s term.CursorShape) pb.TerminalCursorShape {
	switch s {
	case term.CursorUnderline:
		return pb.TerminalCursorShape_TERMINAL_CURSOR_SHAPE_UNDERLINE
	case term.CursorBar:
		return pb.TerminalCursorShape_TERMINAL_CURSOR_SHAPE_BAR
	}
	return pb.TerminalCursorShape_TERMINAL_CURSOR_SHAPE_BLOCK
}

// frameBuilder converts emulator lines into protocol lines with a frame-local style table.
type frameBuilder struct {
	emu        *term.Terminal
	styles     []*pb.TerminalStyle
	styleIndex map[uint32]uint32 // emulator style id -> frame style number (1-based)
}

func (b *frameBuilder) style(id uint32) uint32 {
	if id == 0 {
		return 0
	}
	if n, ok := b.styleIndex[id]; ok {
		return n
	}
	s := b.emu.Style(id)
	if s.IsDefault() {
		b.styleIndex[id] = 0
		return 0
	}
	b.styles = append(b.styles, &pb.TerminalStyle{
		Foreground:     color(s.Foreground),
		Background:     color(s.Background),
		UnderlineColor: color(s.UnderlineColor),
		Flags:          uint32(s.Attrs),
		Underline:      pb.TerminalUnderline(s.Underline),
		Link:           s.Link,
	})
	n := uint32(len(b.styles))
	b.styleIndex[id] = n
	return n
}

func color(c term.Color) *pb.TerminalColor {
	if i, ok := c.Index(); ok {
		return &pb.TerminalColor{Kind: &pb.TerminalColor_Index{Index: uint32(i)}}
	}
	if rgb, ok := c.RGB(); ok {
		return &pb.TerminalColor{Kind: &pb.TerminalColor_Rgb{Rgb: rgb}}
	}
	return nil
}

// line encodes one line as runs. Trailing blank default cells are left out.
func (b *frameBuilder) line(l *term.Line) *pb.TerminalLine {
	if l == nil {
		return &pb.TerminalLine{}
	}
	out := &pb.TerminalLine{Wrapped: l.Wrapped, Mark: mark(l.Mark)}
	cells := l.Cells
	end := len(cells)
	for end > 0 && cells[end-1] == (term.Cell{}) {
		end--
	}
	var run *pb.TerminalRun
	var text strings.Builder
	flush := func() {
		if run != nil {
			run.Text = text.String()
			out.Runs = append(out.Runs, run)
			run = nil
			text.Reset()
		}
	}
	afterHead := false
	for x := 0; x < end; x++ {
		c := cells[x]
		if c.Flags&term.CellWideTail != 0 && afterHead {
			afterHead = false
			continue
		}
		afterHead = c.Flags&term.CellWide != 0
		style := b.style(c.Style)
		if c.Flags&term.CellCluster != 0 {
			flush()
			out.Runs = append(out.Runs, &pb.TerminalRun{Style: style, Text: b.emu.CellText(c), Wide: afterHead,
				Cluster: true})
			continue
		}
		ch := c.Ch
		if ch == 0 {
			ch = ' '
		}
		if run == nil || run.Style != style || run.Wide != afterHead {
			flush()
			run = &pb.TerminalRun{Style: style, Wide: afterHead}
		}
		text.WriteRune(ch)
	}
	flush()
	return out
}

// csi.go
// Author: Kennt Kim
// Created: 2026-10-14 · Last updated: 2026-10-23
//
// Purpose: CSI sequences — cursor movement, erasing, inserting and deleting, scrolling, the scroll
// region, SGR (styles), modes, and the reports programs ask for.
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/csi.go on 2026-10-23.
// - Dispatch is on (private marker, first intermediate, final byte).
// - Cursor positions in CUP/HVP/VPA are relative to the scroll region in origin mode (DECOM).
// - Replies identify the terminal as a VT220-class xterm-compatible terminal; programs choose
//   features by the modes they set, not by our name.

package term

import "fmt"

func (t *Terminal) csiDispatch(final byte) {
	inter := byte(0)
	if t.nInter > 0 {
		inter = t.intermediate[0]
	}
	switch t.private {
	case 0:
		if inter == 0 {
			t.csiPlain(final)
			return
		}
		switch {
		case inter == ' ' && final == 'q':
			t.setCursorStyle(t.rawParam(0))
		case inter == '!' && final == 'p':
			t.softReset()
		case inter == '$' && final == 'p':
			t.reportMode(t.rawParam(0), false)
		}
	case '?':
		switch {
		case inter == 0 && final == 'h':
			t.setPrivateModes(true)
		case inter == 0 && final == 'l':
			t.setPrivateModes(false)
		case inter == '$' && final == 'p':
			t.reportMode(t.rawParam(0), true)
		case inter == 0 && final == 'u': // kitty keyboard: query
			t.keyboardSequence('?')
		case inter == 0 && final == 'n':
			if t.rawParam(0) == 6 { // DECXCPR
				row, col := t.reportedPosition()
				t.replyf("\x1b[?%d;%dR", row, col)
			}
		}
	case '>':
		switch {
		case inter == 0 && final == 'c': // DA2
			t.replyf("\x1b[>1;10;0c")
		case inter == 0 && final == 'q': // XTVERSION
			t.replyf("\x1bP>|%s\x1b\\", t.programName())
		case inter == 0 && final == 'u': // kitty keyboard: push
			t.keyboardSequence('>')
		}
	case '<', '=':
		if inter == 0 && final == 'u' { // kitty keyboard: pop, set
			t.keyboardSequence(t.private)
		}
	}
}

func (t *Terminal) csiPlain(final byte) {
	switch final {
	case '@': // ICH
		t.withCursorLine(func(l *Line) { t.insertCells(l, t.cx, t.param(0, 1)) })
	case 'A': // CUU
		t.moveUp(t.param(0, 1))
	case 'B', 'e': // CUD, VPR
		t.moveDown(t.param(0, 1))
	case 'C', 'a': // CUF, HPR
		t.cx = min(t.cx+t.param(0, 1), t.cols-1)
		t.wrapNext = false
	case 'D': // CUB
		t.cx = max(t.cx-t.param(0, 1), 0)
		t.wrapNext = false
	case 'E': // CNL
		t.moveDown(t.param(0, 1))
		t.cx = 0
	case 'F': // CPL
		t.moveUp(t.param(0, 1))
		t.cx = 0
	case 'G', '`': // CHA, HPA
		t.cx = min(t.param(0, 1), t.cols) - 1
		t.wrapNext = false
	case 'H', 'f': // CUP, HVP
		t.moveTo(t.param(1, 1)-1, t.param(0, 1)-1)
	case 'I': // CHT
		t.tabForward(t.param(0, 1))
	case 'J': // ED
		t.eraseDisplay(t.rawParam(0))
	case 'K': // EL
		t.eraseLine(t.rawParam(0))
	case 'L': // IL
		if t.cy >= t.top && t.cy <= t.bottom {
			t.scrollDown(t.cy, t.bottom, t.param(0, 1))
			t.cx, t.wrapNext = 0, false
		}
	case 'M': // DL
		if t.cy >= t.top && t.cy <= t.bottom {
			t.scrollUp(t.cy, t.bottom, t.param(0, 1), false)
			t.cx, t.wrapNext = 0, false
		}
	case 'P': // DCH
		t.withCursorLine(func(l *Line) { t.deleteCells(l, t.cx, t.param(0, 1)) })
	case 'S': // SU
		t.scrollUp(t.top, t.bottom, t.param(0, 1), true)
	case 'T': // SD
		if t.paramCount() <= 1 {
			t.scrollDown(t.top, t.bottom, t.param(0, 1))
		}
	case 'X': // ECH
		t.withCursorLine(func(l *Line) { t.eraseCells(l, t.cx, t.cx+t.param(0, 1)) })
	case 'Z': // CBT
		t.tabBackward(t.param(0, 1))
	case 'b': // REP
		if t.lastChar != 0 {
			for n := min(t.param(0, 1), 65535); n > 0; n-- {
				t.print(t.lastChar)
			}
		}
	case 'c': // DA1
		if t.rawParam(0) == 0 {
			t.replyf("\x1b[?62;22c")
		}
	case 'd': // VPA
		t.moveTo(t.cx, t.param(0, 1)-1)
	case 'g': // TBC
		switch t.rawParam(0) {
		case 0:
			if t.cx < t.cols {
				t.tabs[t.cx] = false
			}
		case 3:
			clear(t.tabs)
		}
	case 'h', 'l': // SM, RM
		for i := 0; i < t.paramCount(); i++ {
			switch t.rawParam(i) {
			case 4:
				t.insertMode = final == 'h'
			case 20:
				t.newlineMode = final == 'h'
			}
		}
	case 'm':
		t.sgr()
	case 'n': // DSR
		switch t.rawParam(0) {
		case 5:
			t.replyf("\x1b[0n")
		case 6:
			row, col := t.reportedPosition()
			t.replyf("\x1b[%d;%dR", row, col)
		}
	case 'r': // DECSTBM
		top, bottom := t.param(0, 1)-1, t.param(1, t.rows)-1
		bottom = min(bottom, t.rows-1)
		if top < bottom {
			t.top, t.bottom = top, bottom
			t.moveTo(0, 0)
		}
	case 's': // SCOSC
		t.saveCursor()
	case 'u': // SCORC
		t.restoreCursor()
	case 't': // XTWINOPS: only the size report
		if t.rawParam(0) == 18 {
			t.replyf("\x1b[8;%d;%dt", t.rows, t.cols)
		}
	}
}

func (t *Terminal) replyf(format string, args ...any) {
	t.reply([]byte(fmt.Sprintf(format, args...)))
}

// reportedPosition is the 1-based cursor position as CPR reports it.
func (t *Terminal) reportedPosition() (row, col int) {
	row = t.cy + 1
	if t.originMode {
		row -= t.top
	}
	return row, t.cx + 1
}

func (t *Terminal) withCursorLine(f func(*Line)) {
	l := t.lines[t.cy]
	f(l)
	t.touch(l)
	t.wrapNext = false
}

// moveTo places the cursor at column x, row y (origin-mode relative), clamped.
func (t *Terminal) moveTo(x, y int) {
	if t.originMode {
		y += t.top
		y = max(t.top, min(y, t.bottom))
	}
	t.cx = max(0, min(x, t.cols-1))
	t.cy = max(0, min(y, t.rows-1))
	t.wrapNext = false
}

func (t *Terminal) moveUp(n int) {
	limit := 0
	if t.cy >= t.top {
		limit = t.top
	}
	t.cy = max(t.cy-n, limit)
	t.wrapNext = false
}

func (t *Terminal) moveDown(n int) {
	limit := t.rows - 1
	if t.cy <= t.bottom {
		limit = t.bottom
	}
	t.cy = min(t.cy+n, limit)
	t.wrapNext = false
}

func (t *Terminal) insertCells(l *Line, x, n int) {
	if x >= t.cols {
		return
	}
	n = min(n, t.cols-x)
	t.clearCell(l, x)
	copy(l.Cells[x+n:], l.Cells[x:t.cols-n])
	blank := t.eraseCell()
	for i := x; i < x+n; i++ {
		l.Cells[i] = blank
	}
	// A wide character pushed half off the end loses its first half too.
	if last := &l.Cells[t.cols-1]; last.Flags&CellWide != 0 {
		*last = blank
	}
	l.Wrapped = false
}

func (t *Terminal) deleteCells(l *Line, x, n int) {
	if x >= t.cols {
		return
	}
	n = min(n, t.cols-x)
	t.clearCell(l, x)
	t.clearCell(l, x+n-1)
	copy(l.Cells[x:], l.Cells[x+n:])
	blank := t.eraseCell()
	for i := t.cols - n; i < t.cols; i++ {
		l.Cells[i] = blank
	}
	l.Wrapped = false
}

// eraseCells blanks columns [from, to) of a line.
func (t *Terminal) eraseCells(l *Line, from, to int) {
	from, to = max(from, 0), min(to, t.cols)
	if from >= to {
		return
	}
	t.clearCell(l, from)
	t.clearCell(l, to-1)
	blank := t.eraseCell()
	for i := from; i < to; i++ {
		l.Cells[i] = blank
	}
}

func (t *Terminal) eraseLine(mode int) {
	l := t.lines[t.cy]
	switch mode {
	case 0:
		t.eraseCells(l, t.cx, t.cols)
		l.Wrapped = false
	case 1:
		t.eraseCells(l, 0, t.cx+1)
	case 2:
		t.eraseCells(l, 0, t.cols)
		l.Wrapped = false
	default:
		return
	}
	t.touch(l)
	t.wrapNext = false
}

func (t *Terminal) eraseDisplay(mode int) {
	switch mode {
	case 0:
		t.eraseLine(0)
		for y := t.cy + 1; y < t.rows; y++ {
			t.eraseWholeLine(y)
		}
	case 1:
		t.eraseLine(1)
		for y := 0; y < t.cy; y++ {
			t.eraseWholeLine(y)
		}
	case 2:
		for y := 0; y < t.rows; y++ {
			t.eraseWholeLine(y)
		}
	case 3:
		if !t.alt {
			t.hist.clear()
			t.historyClears++
		}
	}
	t.wrapNext = false
}

// eraseWholeLine blanks row y for an erase of the display, which takes its mark too: a cleared
// screen holds no prompts. Erasing in a line (EL) keeps the mark — progress bars redraw the first
// line of a command's output that way.
func (t *Terminal) eraseWholeLine(y int) {
	l := t.lines[y]
	t.eraseCells(l, 0, t.cols)
	l.Wrapped = false
	l.Mark = nil
	t.touch(l)
}

func (t *Terminal) setCursorStyle(ps int) {
	switch ps {
	case 0, 1:
		t.cursorShape, t.cursorBlink = CursorBlock, true
	case 2:
		t.cursorShape, t.cursorBlink = CursorBlock, false
	case 3:
		t.cursorShape, t.cursorBlink = CursorUnderline, true
	case 4:
		t.cursorShape, t.cursorBlink = CursorUnderline, false
	case 5:
		t.cursorShape, t.cursorBlink = CursorBar, true
	case 6:
		t.cursorShape, t.cursorBlink = CursorBar, false
	}
}

// softReset is DECSTR.
func (t *Terminal) softReset() {
	t.cursorHidden = false
	t.originMode = false
	t.autowrap = true
	t.insertMode = false
	t.cursorKeysApp = false
	t.keypadApp = false
	t.top, t.bottom = 0, t.rows-1
	t.setPen(Style{})
	t.charsets, t.gl = [2]charset{}, 0
	t.savedMain, t.savedAlt = savedCursor{}, savedCursor{}
	t.wrapNext = false
}

// ---------------------------------------------------------------------------------------------
// Modes
// ---------------------------------------------------------------------------------------------

func (t *Terminal) setPrivateModes(on bool) {
	for i := 0; i < t.paramCount(); i++ {
		switch t.rawParam(i) {
		case 1:
			t.cursorKeysApp = on
		case 6:
			t.originMode = on
			t.moveTo(0, 0)
		case 7:
			t.autowrap = on
			if !on {
				t.wrapNext = false
			}
		case 9:
			t.setMouse(MousePress, on)
		case 12:
			t.cursorBlink = on
		case 25:
			t.cursorHidden = !on
		case 47:
			t.altScreen(on, false)
		case 66:
			t.keypadApp = on
		case 1000:
			t.setMouse(MousePressRelease, on)
		case 1002:
			t.setMouse(MouseDrag, on)
		case 1003:
			t.setMouse(MouseMotion, on)
		case 1004:
			t.focusEvents = on
		case 1006:
			t.mouseSGR = on
		case 1047:
			if on {
				t.altScreen(true, false)
			} else {
				if t.alt {
					t.clearLines(t.altLines)
				}
				t.altScreen(false, false)
			}
		case 1048:
			if on {
				t.saveCursor()
			} else {
				t.restoreCursor()
			}
		case 1049:
			if on {
				if !t.alt {
					t.saveCursor()
				}
				t.altScreen(true, true)
			} else if t.alt {
				t.altScreen(false, false)
				t.restoreCursor()
			}
		case 2004:
			t.bracketedPaste = on
		case 2026:
			t.syncOutput = on
		}
	}
}

func (t *Terminal) setMouse(m MouseTracking, on bool) {
	switch {
	case on:
		t.mouse = m
	case t.mouse == m:
		t.mouse = MouseNone
	}
}

// reportMode answers DECRQM: 1 = set, 2 = reset, 0 = not recognised.
func (t *Terminal) reportMode(mode int, private bool) {
	value := 0
	b := func(on bool) int {
		if on {
			return 1
		}
		return 2
	}
	if private {
		switch mode {
		case 1:
			value = b(t.cursorKeysApp)
		case 6:
			value = b(t.originMode)
		case 7:
			value = b(t.autowrap)
		case 9:
			value = b(t.mouse == MousePress)
		case 12:
			value = b(t.cursorBlink)
		case 25:
			value = b(!t.cursorHidden)
		case 47, 1047, 1049:
			value = b(t.alt)
		case 66:
			value = b(t.keypadApp)
		case 1000:
			value = b(t.mouse == MousePressRelease)
		case 1002:
			value = b(t.mouse == MouseDrag)
		case 1003:
			value = b(t.mouse == MouseMotion)
		case 1004:
			value = b(t.focusEvents)
		case 1006:
			value = b(t.mouseSGR)
		case 2004:
			value = b(t.bracketedPaste)
		case 2026:
			value = b(t.syncOutput)
		}
		t.replyf("\x1b[?%d;%d$y", mode, value)
		return
	}
	switch mode {
	case 4:
		value = b(t.insertMode)
	case 20:
		value = b(t.newlineMode)
	}
	t.replyf("\x1b[%d;%d$y", mode, value)
}

// ---------------------------------------------------------------------------------------------
// SGR
// ---------------------------------------------------------------------------------------------

func (t *Terminal) sgr() {
	s := t.pen
	n := t.paramCount()
	if n == 0 {
		t.setPen(Style{Link: s.Link})
		return
	}
	for i := 0; i < n; i++ {
		if t.paramIsSub[i] {
			continue // a stray subparameter
		}
		p := t.rawParam(i)
		switch {
		case p == 0:
			s = Style{Link: s.Link}
		case p == 1:
			s.Attrs |= AttrBold
		case p == 2:
			s.Attrs |= AttrFaint
		case p == 3:
			s.Attrs |= AttrItalic
		case p == 4:
			s.Underline = UnderlineSingle
			if i+1 < n && t.paramIsSub[i+1] {
				if u := t.rawParam(i + 1); u <= int(UnderlineDashed) {
					s.Underline = Underline(u)
				}
			}
		case p == 5 || p == 6:
			s.Attrs |= AttrBlink
		case p == 7:
			s.Attrs |= AttrInverse
		case p == 8:
			s.Attrs |= AttrInvisible
		case p == 9:
			s.Attrs |= AttrStrikethrough
		case p == 21:
			s.Underline = UnderlineDouble
		case p == 22:
			s.Attrs &^= AttrBold | AttrFaint
		case p == 23:
			s.Attrs &^= AttrItalic
		case p == 24:
			s.Underline = UnderlineNone
		case p == 25:
			s.Attrs &^= AttrBlink
		case p == 27:
			s.Attrs &^= AttrInverse
		case p == 28:
			s.Attrs &^= AttrInvisible
		case p == 29:
			s.Attrs &^= AttrStrikethrough
		case p >= 30 && p <= 37:
			s.Foreground = IndexedColor(uint8(p - 30))
		case p == 38:
			c, used := t.extendedColor(i)
			if used > 0 {
				s.Foreground = c
			}
			i += used
		case p == 39:
			s.Foreground = DefaultColor
		case p >= 40 && p <= 47:
			s.Background = IndexedColor(uint8(p - 40))
		case p == 48:
			c, used := t.extendedColor(i)
			if used > 0 {
				s.Background = c
			}
			i += used
		case p == 49:
			s.Background = DefaultColor
		case p == 53:
			s.Attrs |= AttrOverline
		case p == 55:
			s.Attrs &^= AttrOverline
		case p == 58:
			c, used := t.extendedColor(i)
			if used > 0 {
				s.UnderlineColor = c
			}
			i += used
		case p == 59:
			s.UnderlineColor = DefaultColor
		case p >= 90 && p <= 97:
			s.Foreground = IndexedColor(uint8(p - 90 + 8))
		case p >= 100 && p <= 107:
			s.Background = IndexedColor(uint8(p - 100 + 8))
		}
	}
	t.setPen(s)
}

// extendedColor parses the colour after SGR 38/48/58 at parameter i, in either the
// semicolon form (38;5;n, 38;2;r;g;b) or the colon form (38:5:n, 38:2::r:g:b, 38:2:r:g:b).
// It returns the colour and how many parameters after i it used (0 when malformed).
func (t *Terminal) extendedColor(i int) (Color, int) {
	n := t.paramCount()
	if i+1 >= n {
		return DefaultColor, 0
	}
	colon := t.paramIsSub[i+1]
	if colon {
		// Count the subparameters that belong to this parameter.
		end := i + 1
		for end < n && t.paramIsSub[end] {
			end++
		}
		sub := make([]int, 0, end-i-1)
		for k := i + 1; k < end; k++ {
			sub = append(sub, t.rawParam(k))
		}
		used := end - i - 1
		switch {
		case len(sub) >= 2 && sub[0] == 5:
			return IndexedColor(uint8(clamp255(sub[1]))), used
		case len(sub) >= 5 && sub[0] == 2: // 2:colorspace:r:g:b
			return RGBColor(uint8(clamp255(sub[2])), uint8(clamp255(sub[3])), uint8(clamp255(sub[4]))), used
		case len(sub) == 4 && sub[0] == 2: // 2:r:g:b
			return RGBColor(uint8(clamp255(sub[1])), uint8(clamp255(sub[2])), uint8(clamp255(sub[3]))), used
		}
		return DefaultColor, used
	}
	switch t.rawParam(i + 1) {
	case 5:
		if i+2 < n {
			return IndexedColor(uint8(clamp255(t.rawParam(i + 2)))), 2
		}
	case 2:
		if i+4 < n {
			return RGBColor(uint8(clamp255(t.rawParam(i+2))), uint8(clamp255(t.rawParam(i+3))),
				uint8(clamp255(t.rawParam(i+4)))), 4
		}
	}
	return DefaultColor, n - i - 1
}

func clamp255(v int) int { return max(0, min(v, 255)) }

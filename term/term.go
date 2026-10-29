// term.go
// Author: Kennt Kim
// Created: 2026-10-14 · Last updated: 2026-10-23
//
// Purpose: an xterm-compatible terminal emulator without a display — the session's model of a
// terminal screen (spec/PROTOCOL.md §7). Output from the program goes in through Write; the
// screen, history, cursor and modes come out through the read accessors, and input from the user
// is encoded for the program by input.go.
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/term.go on 2026-10-23.
// - Not safe for concurrent use; the owner serializes access.
// - The parser is the DEC/ANSI state machine of vt100.net ("A parser for DEC's ANSI-compatible
//   video terminals", Paul Williams), with UTF-8 decoding in the ground state. 8-bit C1 controls
//   are not recognised (they are UTF-8 continuation bytes).
// - Coordinates are 0-based: cx is the column, cy the row. wrapNext is xterm's "pending wrap":
//   the last column was just written and the next printable character wraps first.
// - Replies to queries (DSR, DA, DECRQM, OSC colour queries) go to Options.Reply synchronously
//   from Write.
// - Unsupported sequences are consumed without effect.

package term

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Defaults.
const (
	DefaultColumns      = 80
	DefaultRows         = 24
	DefaultHistoryLimit = 10000
	maxParams           = 32
	maxOSC              = 64 * 1024
)

// MouseTracking is what mouse actions the program asked for.
type MouseTracking uint8

const (
	MouseNone MouseTracking = iota
	MousePress
	MousePressRelease
	MouseDrag
	MouseMotion
)

// CursorShape is how the cursor is drawn.
type CursorShape uint8

const (
	CursorBlock CursorShape = iota
	CursorUnderline
	CursorBar
)

// Cursor is the cursor state.
type Cursor struct {
	Column, Row int
	Hidden      bool
	Shape       CursorShape
	Blink       bool
}

// Options configures a Terminal.
type Options struct {
	Columns, Rows int
	HistoryLimit  int
	// Reply receives the terminal's answers to the program's queries; they must be written to
	// the program's input.
	Reply func([]byte)
	// Clipboard receives the text a program puts on the clipboard (OSC 52), for the device's
	// clipboard; nil ignores it.
	Clipboard func(text string)
	// Colors are what colour queries answer (OSC 4, 10, 11, 12); zero: DefaultColors(false).
	Colors Colors
	// Notify receives a notification the program asked for (OSC 9, 777, 99), as plain text; nil
	// ignores them (notify.go).
	Notify func(title, body string)
	// Bell is called at each BEL the program writes (Bells counts them too); nil ignores them.
	Bell func()
	// StatusChanged is called when Status() may have changed (OSC 7501, OSC 9;4, a prompt, a reset).
	StatusChanged func()
	// CommandFinished receives each command shell integration reports ended (OSC 133 D after C,
	// prompts.go); nil ignores them.
	CommandFinished func(CommandFinished)
	// Now is the time the output being written was produced, for how long commands run; nil, or a
	// zero result, when that is not known (prompts.go).
	Now func() time.Time
	// Hostname is this machine's name, to tell a local directory from another host's (OSC 7).
	Hostname string
	// Name is how the terminal names itself to a program that asks (XTVERSION); empty: "sdterm",
	// the engine's own name. The engine's user gives its application's name.
	Name string
	// Triggered receives each line that matched a trigger that notifies (triggers.go); nil
	// ignores them.
	Triggered func(Triggered)
}

type charset uint8

const (
	charsetASCII charset = iota
	charsetDECGraphics
)

type savedCursor struct {
	x, y       int
	wrapNext   bool
	pen        Style
	originMode bool
	charsets   [2]charset
	gl         int
	valid      bool
}

type parserState uint8

const (
	stGround parserState = iota
	stEscape
	stEscapeIntermediate
	stCSIEntry
	stCSIParam
	stCSIIntermediate
	stCSIIgnore
	stOSC
	stOSCEscape
	stDCS       // DCS, SOS, PM and APC strings: consumed until ST
	stDCSEscape // ESC seen inside a string
)

// Terminal is one emulated terminal.
type Terminal struct {
	cols, rows int
	reply      func([]byte)
	clipboard  func(string)
	colors     Colors

	mainLines, altLines []*Line
	lines               []*Line // the active screen: mainLines or altLines
	alt                 bool
	hist                *history

	cx, cy   int
	wrapNext bool
	pen      Style
	penID    uint32
	penValid bool
	lastChar rune // for REP

	top, bottom int // scroll region, inclusive
	tabs        []bool

	// Modes.
	originMode     bool
	autowrap       bool
	insertMode     bool
	newlineMode    bool // LNM
	cursorKeysApp  bool // DECCKM
	keypadApp      bool // DECKPAM
	cursorHidden   bool
	cursorBlink    bool
	cursorShape    CursorShape
	bracketedPaste bool
	focusEvents    bool
	mouse          MouseTracking
	mouseSGR       bool
	syncOutput     bool

	// The kitty keyboard protocol's flags, a stack per screen (keyboard.go).
	keyboardMain, keyboardAlt []int

	charsets [2]charset
	gl       int

	savedMain, savedAlt savedCursor

	title, workingDirectory string
	hostname                string
	name                    string // Options.Name
	// The last OSC 7 when it named another host (a shell reached by ssh), "" when it was local:
	// a prompt then has no directory here (Mark.Directory), and ModeSequences says it again.
	remoteDirectory string
	bells           uint64

	// What the program says (notify.go).
	onNotify      func(title, body string)
	onBell        func()
	onTriggered   func(Triggered)
	triggers      []Trigger
	onStatus      func()
	records       map[string]*record
	recordSeq     uint64
	progress      progressBar
	pendingNotes  map[string]*pendingNote
	scrolled      uint64 // lines ever added to the history
	historyClears uint64
	version       uint64

	// Shell integration (prompts.go): the shells' command stack.
	shells    []*shellEntry
	onCommand func(CommandFinished)
	clock     func() time.Time

	styles   internTable[Style]
	clusters internTable[string]

	// Parser.
	state        parserState
	params       [maxParams]int
	paramIsSub   [maxParams]bool // params[i] follows a ':' (a subparameter of params[i-1])
	paramHasVal  [maxParams]bool
	nParams      int
	private      byte // '?', '>', '<' or '=' after CSI
	intermediate [2]byte
	nInter       int
	osc          []byte
	stringKind   byte // 'P', 'X', '^' or '_' for stDCS (only consumed)
	utf8buf      [utf8.UTFMax]byte
	utf8len      int
	utf8need     int
}

// New makes a terminal of the given size with a blank screen.
func New(opts Options) *Terminal {
	if opts.Columns <= 0 {
		opts.Columns = DefaultColumns
	}
	if opts.Rows <= 0 {
		opts.Rows = DefaultRows
	}
	if opts.HistoryLimit == 0 {
		opts.HistoryLimit = DefaultHistoryLimit
	}
	t := &Terminal{
		cols:        opts.Columns,
		rows:        opts.Rows,
		reply:       opts.Reply,
		clipboard:   opts.Clipboard,
		onNotify:    opts.Notify,
		onBell:      opts.Bell,
		onTriggered: opts.Triggered,
		onStatus:    opts.StatusChanged,
		onCommand:   opts.CommandFinished,
		clock:       opts.Now,
		hostname:    strings.ToLower(opts.Hostname),
		name:        opts.Name,
		colors:      opts.Colors,
		hist:        newHistory(opts.HistoryLimit),
		styles:      newInternTable(Style{}),
		clusters:    newInternTable[string](),
	}
	if t.colors == (Colors{}) {
		t.colors = DefaultColors(false)
	}
	if t.reply == nil {
		t.reply = func([]byte) {}
	}
	t.mainLines = t.blankLines(t.rows, Cell{})
	t.altLines = t.blankLines(t.rows, Cell{})
	t.lines = t.mainLines
	t.resetState()
	return t
}

// resetState puts modes, cursor, pen, tabs and charsets in their power-on state.
func (t *Terminal) resetState() {
	t.cx, t.cy, t.wrapNext = 0, 0, false
	t.pen, t.penValid = Style{}, false
	t.top, t.bottom = 0, t.rows-1
	t.originMode = false
	t.autowrap = true
	t.insertMode = false
	t.newlineMode = false
	t.cursorKeysApp = false
	t.keypadApp = false
	t.cursorHidden = false
	t.cursorBlink = false
	t.cursorShape = CursorBlock
	t.bracketedPaste = false
	t.focusEvents = false
	t.mouse = MouseNone
	t.mouseSGR = false
	t.syncOutput = false
	t.charsets = [2]charset{}
	t.gl = 0
	t.savedMain, t.savedAlt = savedCursor{}, savedCursor{}
	t.resetTabs()
}

func (t *Terminal) resetTabs() {
	t.tabs = make([]bool, t.cols)
	for i := 8; i < t.cols; i += 8 {
		t.tabs[i] = true
	}
}

func (t *Terminal) nextVersion() uint64 {
	t.version++
	return t.version
}

func (t *Terminal) touch(l *Line) { l.Version = t.nextVersion() }

func (t *Terminal) blankLines(n int, blank Cell) []*Line {
	out := make([]*Line, n)
	for i := range out {
		out[i] = newLine(t.cols, blank, t.nextVersion())
	}
	return out
}

// penStyle returns the interned id of the current pen.
func (t *Terminal) penStyle() uint32 {
	if !t.penValid {
		t.penID = t.styles.intern(t.pen)
		t.penValid = true
	}
	return t.penID
}

func (t *Terminal) setPen(s Style) {
	if s != t.pen {
		t.pen = s
		t.penValid = false
	}
}

// eraseCell is a blank cell in the current background colour (xterm's BCE behaviour).
func (t *Terminal) eraseCell() Cell {
	if t.pen.Background == DefaultColor {
		return Cell{}
	}
	return Cell{Style: t.styles.intern(Style{Background: t.pen.Background})}
}

// ---------------------------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------------------------

// Write feeds program output to the terminal. It never fails.
func (t *Terminal) Write(p []byte) (int, error) {
	for i := 0; i < len(p); i++ {
		b := p[i]
		if t.state == stGround {
			if t.utf8need > 0 {
				t.utf8Continue(b)
				continue
			}
			// Fast path: a run of printable ASCII.
			if b >= 0x20 && b < 0x7F {
				t.print(rune(b))
				continue
			}
			if b >= 0x80 {
				t.utf8Start(b)
				continue
			}
		}
		t.step(b)
	}
	t.maybeCompact()
	return len(p), nil
}

func (t *Terminal) utf8Start(b byte) {
	switch {
	case b&0xE0 == 0xC0:
		t.utf8need = 1
	case b&0xF0 == 0xE0:
		t.utf8need = 2
	case b&0xF8 == 0xF0:
		t.utf8need = 3
	default:
		t.print(utf8.RuneError)
		return
	}
	t.utf8buf[0] = b
	t.utf8len = 1
}

func (t *Terminal) utf8Continue(b byte) {
	if b&0xC0 != 0x80 {
		// Truncated sequence: show the replacement character and reprocess this byte.
		t.utf8need = 0
		t.print(utf8.RuneError)
		if b >= 0x20 && b < 0x7F {
			t.print(rune(b))
		} else if b >= 0x80 {
			t.utf8Start(b)
		} else {
			t.step(b)
		}
		return
	}
	t.utf8buf[t.utf8len] = b
	t.utf8len++
	t.utf8need--
	if t.utf8need == 0 {
		r, _ := utf8.DecodeRune(t.utf8buf[:t.utf8len])
		t.print(r)
	}
}

// step advances the parser by one byte outside the ground-state fast paths.
func (t *Terminal) step(b byte) {
	// Transitions from anywhere (except inside strings, which handle ESC themselves).
	switch t.state {
	case stOSC, stOSCEscape, stDCS, stDCSEscape:
	default:
		switch b {
		case 0x18, 0x1A: // CAN, SUB
			t.state = stGround
			return
		case 0x1B:
			t.enterEscape()
			return
		}
	}

	switch t.state {
	case stGround:
		if b < 0x20 {
			t.execute(b)
		}
		// 0x7F (DEL) is ignored.

	case stEscape:
		switch {
		case b < 0x20:
			t.execute(b)
		case b <= 0x2F:
			t.collect(b)
			t.state = stEscapeIntermediate
		case b == '[':
			t.clearParams()
			t.state = stCSIEntry
		case b == ']':
			t.osc = t.osc[:0]
			t.state = stOSC
		case b == 'P' || b == 'X' || b == '^' || b == '_':
			t.stringKind = b
			t.state = stDCS
		case b < 0x7F:
			t.escDispatch(b)
			t.state = stGround
		}

	case stEscapeIntermediate:
		switch {
		case b < 0x20:
			t.execute(b)
		case b <= 0x2F:
			t.collect(b)
		case b < 0x7F:
			t.escDispatch(b)
			t.state = stGround
		}

	case stCSIEntry, stCSIParam:
		switch {
		case b < 0x20:
			t.execute(b)
		case b >= '0' && b <= '9':
			t.paramDigit(b)
			t.state = stCSIParam
		case b == ';' || b == ':':
			t.paramSeparator(b == ':')
			t.state = stCSIParam
		case b >= '<' && b <= '?':
			if t.state == stCSIEntry {
				t.private = b
				t.state = stCSIParam
			} else {
				t.state = stCSIIgnore
			}
		case b <= 0x2F:
			t.collect(b)
			t.state = stCSIIntermediate
		case b < 0x7F:
			t.csiDispatch(b)
			t.state = stGround
		}

	case stCSIIntermediate:
		switch {
		case b < 0x20:
			t.execute(b)
		case b <= 0x2F:
			t.collect(b)
		case b <= 0x3F:
			t.state = stCSIIgnore
		case b < 0x7F:
			t.csiDispatch(b)
			t.state = stGround
		}

	case stCSIIgnore:
		switch {
		case b < 0x20:
			t.execute(b)
		case b >= 0x40 && b < 0x7F:
			t.state = stGround
		}

	case stOSC:
		switch b {
		case 0x07:
			t.oscDispatch(true)
			t.state = stGround
		case 0x1B:
			t.state = stOSCEscape
		case 0x18, 0x1A:
			t.state = stGround
		default:
			if b >= 0x20 && len(t.osc) < maxOSC {
				t.osc = append(t.osc, b)
			}
		}

	case stOSCEscape:
		if b == '\\' {
			t.oscDispatch(false)
			t.state = stGround
			return
		}
		// ESC that does not start ST aborts the string and begins a new escape sequence.
		t.enterEscape()
		t.step(b)

	case stDCS:
		switch b {
		case 0x1B:
			t.state = stDCSEscape
		case 0x18, 0x1A:
			t.state = stGround
		}

	case stDCSEscape:
		if b == '\\' {
			t.state = stGround
			return
		}
		t.enterEscape()
		t.step(b)
	}
}

func (t *Terminal) enterEscape() {
	t.nInter = 0
	t.state = stEscape
}

func (t *Terminal) collect(b byte) {
	if t.nInter < len(t.intermediate) {
		t.intermediate[t.nInter] = b
	}
	t.nInter++
}

func (t *Terminal) clearParams() {
	t.nParams = 0
	t.private = 0
	t.nInter = 0
}

func (t *Terminal) paramDigit(b byte) {
	if t.nParams == 0 {
		t.nParams = 1
		t.params[0], t.paramIsSub[0], t.paramHasVal[0] = 0, false, false
	}
	i := t.nParams - 1
	if i >= maxParams {
		return
	}
	if t.params[i] < 100000 {
		t.params[i] = t.params[i]*10 + int(b-'0')
	}
	t.paramHasVal[i] = true
}

func (t *Terminal) paramSeparator(sub bool) {
	if t.nParams == 0 {
		t.nParams = 1
		t.params[0], t.paramIsSub[0], t.paramHasVal[0] = 0, false, false
	}
	if t.nParams < maxParams {
		t.params[t.nParams], t.paramIsSub[t.nParams], t.paramHasVal[t.nParams] = 0, sub, false
	}
	t.nParams++
}

// param returns parameter i, or def when it is absent or 0 (the usual CSI convention).
func (t *Terminal) param(i, def int) int {
	if i >= t.nParams || i >= maxParams || !t.paramHasVal[i] || t.params[i] == 0 {
		return def
	}
	return t.params[i]
}

// rawParam returns parameter i as given (0 when absent).
func (t *Terminal) rawParam(i int) int {
	if i >= t.nParams || i >= maxParams {
		return 0
	}
	return t.params[i]
}

func (t *Terminal) paramCount() int { return min(t.nParams, maxParams) }

// ---------------------------------------------------------------------------------------------
// Printing
// ---------------------------------------------------------------------------------------------

var decGraphics = map[rune]rune{
	'`': '◆', 'a': '▒', 'b': '␉', 'c': '␌', 'd': '␍', 'e': '␊', 'f': '°', 'g': '±',
	'h': '␤', 'i': '␋', 'j': '┘', 'k': '┐', 'l': '┌', 'm': '└', 'n': '┼', 'o': '⎺',
	'p': '⎻', 'q': '─', 'r': '⎼', 's': '⎽', 't': '├', 'u': '┤', 'v': '┴', 'w': '┬',
	'x': '│', 'y': '≤', 'z': '≥', '{': 'π', '|': '≠', '}': '£', '~': '·',
}

func (t *Terminal) print(r rune) {
	if t.charsets[t.gl] == charsetDECGraphics && r < 0x7F {
		if g, ok := decGraphics[r]; ok {
			r = g
		}
	}
	w := RuneWidth(r)
	if w == 0 {
		t.combine(r)
		return
	}
	t.lastChar = r

	if t.wrapNext {
		if t.autowrap {
			t.lines[t.cy].Wrapped = true
			t.touch(t.lines[t.cy])
			t.cx = 0
			t.lineFeed()
		}
		t.wrapNext = false
	}
	if w == 2 && t.cx == t.cols-1 {
		// A wide character does not fit in the last column: wrap first (or drop it).
		if !t.autowrap || t.cols < 2 {
			return
		}
		line := t.lines[t.cy]
		t.clearCell(line, t.cx)
		line.Cells[t.cx] = t.eraseCell()
		line.Wrapped = true
		t.touch(line)
		t.cx = 0
		t.lineFeed()
	}

	line := t.lines[t.cy]
	if t.insertMode {
		t.insertCells(line, t.cx, w)
	}
	style := t.penStyle()
	t.clearCell(line, t.cx)
	if w == 2 {
		t.clearCell(line, t.cx+1)
		line.Cells[t.cx] = Cell{Ch: r, Style: style, Flags: CellWide}
		line.Cells[t.cx+1] = Cell{Style: style, Flags: CellWideTail}
	} else {
		line.Cells[t.cx] = Cell{Ch: r, Style: style}
	}
	t.touch(line)

	if t.cx+w >= t.cols {
		t.cx = t.cols - 1
		t.wrapNext = true
	} else {
		t.cx += w
	}
}

// combine appends a zero-width character to the cell before the cursor.
func (t *Terminal) combine(r rune) {
	x, y := t.cx-1, t.cy
	if t.wrapNext {
		x = t.cx
	}
	if x < 0 {
		return
	}
	line := t.lines[y]
	if line.Cells[x].Flags&CellWideTail != 0 && x > 0 {
		x--
	}
	c := &line.Cells[x]
	if c.IsBlank() {
		return
	}
	text := t.cellText(*c) + string(r)
	if len(text) > 64 {
		return // a runaway sequence of combining marks
	}
	c.Ch = rune(t.clusters.intern(text))
	c.Flags |= CellCluster
	t.touch(line)
}

// cellText returns the characters of a cell ("" for a blank or a wide tail).
func (t *Terminal) cellText(c Cell) string {
	switch {
	case c.Flags&CellCluster != 0:
		return t.clusters.get(uint32(c.Ch))
	case c.Ch == 0:
		return ""
	default:
		return string(c.Ch)
	}
}

// clearCell blanks the other half of a wide character that cell x is part of, so overwriting
// either half never leaves half a character behind.
func (t *Terminal) clearCell(line *Line, x int) {
	if x < 0 || x >= len(line.Cells) {
		return
	}
	c := line.Cells[x]
	if c.Flags&CellWide != 0 && x+1 < len(line.Cells) {
		line.Cells[x+1] = Cell{Style: line.Cells[x+1].Style}
	}
	if c.Flags&CellWideTail != 0 && x > 0 {
		line.Cells[x-1] = Cell{Style: line.Cells[x-1].Style}
	}
}

// ---------------------------------------------------------------------------------------------
// C0 controls and escape sequences
// ---------------------------------------------------------------------------------------------

func (t *Terminal) execute(b byte) {
	switch b {
	case 0x07: // BEL
		t.bells++
		if t.onBell != nil {
			t.onBell()
		}
	case 0x08: // BS
		if t.wrapNext {
			t.wrapNext = false
		} else if t.cx > 0 {
			t.cx--
		}
	case 0x09: // HT
		t.tabForward(1)
	case 0x0A, 0x0B, 0x0C: // LF, VT, FF
		t.lineFeed()
		if t.newlineMode {
			t.cx = 0
		}
	case 0x0D: // CR
		t.cx = 0
		t.wrapNext = false
	case 0x0E: // SO
		t.gl = 1
	case 0x0F: // SI
		t.gl = 0
	}
}

func (t *Terminal) escDispatch(b byte) {
	if t.nInter > 0 {
		switch t.intermediate[0] {
		case '(', ')':
			g := 0
			if t.intermediate[0] == ')' {
				g = 1
			}
			if b == '0' {
				t.charsets[g] = charsetDECGraphics
			} else {
				t.charsets[g] = charsetASCII
			}
		case '#':
			if b == '8' {
				t.decaln()
			}
		}
		return
	}
	switch b {
	case '7':
		t.saveCursor()
	case '8':
		t.restoreCursor()
	case 'D':
		t.lineFeed()
	case 'E':
		t.cx = 0
		t.lineFeed()
	case 'H':
		if t.cx < t.cols {
			t.tabs[t.cx] = true
		}
	case 'M':
		t.reverseIndex()
	case 'c':
		t.fullReset()
	case '=':
		t.keypadApp = true
	case '>':
		t.keypadApp = false
	}
}

// lineFeed moves down one line, scrolling the region at its bottom. Leaving a line that does not
// wrap on, it finishes it: the triggers read it (triggers.go).
func (t *Terminal) lineFeed() {
	t.checkTriggers(t.cy)
	t.wrapNext = false
	switch {
	case t.cy == t.bottom:
		t.scrollUp(t.top, t.bottom, 1, true)
	case t.cy < t.rows-1:
		t.cy++
	}
}

func (t *Terminal) reverseIndex() {
	t.wrapNext = false
	switch {
	case t.cy == t.top:
		t.scrollDown(t.top, t.bottom, 1)
	case t.cy > 0:
		t.cy--
	}
}

// scrollUp moves lines top..bottom up by n. With keep, on the main screen and a full-screen
// region, the lines leaving the top enter the history; otherwise they are lost (DL).
func (t *Terminal) scrollUp(top, bottom, n int, keep bool) {
	n = min(n, bottom-top+1)
	if n <= 0 {
		return
	}
	toHistory := keep && !t.alt && top == 0 && bottom == t.rows-1
	blank := t.eraseCell()
	for i := 0; i < n; i++ {
		if toHistory {
			t.hist.push(t.lines[top+i].trimmed())
			t.scrolled++
		}
	}
	copy(t.lines[top:], t.lines[top+n:bottom+1])
	for i := bottom - n + 1; i <= bottom; i++ {
		t.lines[i] = newLine(t.cols, blank, t.nextVersion())
	}
}

// scrollDown moves lines top..bottom down by n; lines leaving the bottom are lost.
func (t *Terminal) scrollDown(top, bottom, n int) {
	n = min(n, bottom-top+1)
	if n <= 0 {
		return
	}
	blank := t.eraseCell()
	copy(t.lines[top+n:bottom+1], t.lines[top:bottom+1-n])
	for i := top; i < top+n; i++ {
		t.lines[i] = newLine(t.cols, blank, t.nextVersion())
	}
}

func (t *Terminal) tabForward(n int) {
	for ; n > 0 && t.cx < t.cols-1; n-- {
		t.cx++
		for t.cx < t.cols-1 && !t.tabs[t.cx] {
			t.cx++
		}
	}
	t.wrapNext = false
}

func (t *Terminal) tabBackward(n int) {
	for ; n > 0 && t.cx > 0; n-- {
		t.cx--
		for t.cx > 0 && !t.tabs[t.cx] {
			t.cx--
		}
	}
	t.wrapNext = false
}

func (t *Terminal) saveCursor() {
	s := savedCursor{x: t.cx, y: t.cy, wrapNext: t.wrapNext, pen: t.pen, originMode: t.originMode,
		charsets: t.charsets, gl: t.gl, valid: true}
	if t.alt {
		t.savedAlt = s
	} else {
		t.savedMain = s
	}
}

func (t *Terminal) restoreCursor() {
	s := t.savedMain
	if t.alt {
		s = t.savedAlt
	}
	if !s.valid {
		s = savedCursor{}
	}
	t.cx, t.cy = min(s.x, t.cols-1), min(s.y, t.rows-1)
	t.wrapNext = s.wrapNext && t.cx == t.cols-1
	t.setPen(s.pen)
	t.originMode = s.originMode
	t.charsets, t.gl = s.charsets, s.gl
}

func (t *Terminal) fullReset() {
	t.altScreen(false, false)
	t.mainLines = t.blankLines(t.rows, Cell{})
	t.altLines = t.blankLines(t.rows, Cell{})
	t.lines = t.mainLines
	t.title = ""
	t.keyboardMain, t.keyboardAlt = nil, nil
	t.pendingNotes = nil
	t.resetState()
	t.clearStatus()
}

func (t *Terminal) decaln() {
	style := t.penStyle()
	for _, l := range t.lines {
		for i := range l.Cells {
			l.Cells[i] = Cell{Ch: 'E', Style: style}
		}
		l.Wrapped = false
		t.touch(l)
	}
	t.top, t.bottom = 0, t.rows-1
	t.cx, t.cy, t.wrapNext = 0, 0, false
}

// altScreen switches between the main and alternate screens.
func (t *Terminal) altScreen(on, clear bool) {
	if on == t.alt {
		if on && clear {
			t.clearLines(t.altLines)
		}
		return
	}
	t.alt = on
	if on {
		t.lines = t.altLines
		if clear {
			t.clearLines(t.altLines)
		}
	} else {
		t.lines = t.mainLines
	}
	// Both screens show different lines, so every row's version differs from what a client
	// shows; frame building notices that through the versions.
	t.wrapNext = false
}

func (t *Terminal) clearLines(lines []*Line) {
	blank := t.eraseCell()
	for i := range lines {
		lines[i] = newLine(t.cols, blank, t.nextVersion())
	}
}

// ---------------------------------------------------------------------------------------------
// Size
// ---------------------------------------------------------------------------------------------

// Resize changes the screen size. Lines are cut or padded, not reflowed. When the screen gets
// shorter, lines above the cursor move into the history so the cursor line stays visible.
func (t *Terminal) Resize(cols, rows int) {
	cols, rows = max(cols, 1), max(rows, 1)
	if cols == t.cols && rows == t.rows {
		return
	}
	resize := func(lines []*Line, main bool) []*Line {
		cursorRow := t.cy
		if t.alt == main {
			cursorRow = 0 // not the active screen: keep the top
		}
		if rows < len(lines) {
			drop := 0
			if cursorRow >= rows {
				drop = cursorRow - rows + 1
			}
			for i := 0; i < drop; i++ {
				if main {
					t.hist.push(lines[i].trimmed())
					t.scrolled++
				}
			}
			lines = lines[drop : drop+rows]
		}
		for _, l := range lines {
			if len(l.Cells) != cols {
				cells := make([]Cell, cols)
				copy(cells, l.Cells)
				if cols < len(l.Cells) && cells[cols-1].Flags&CellWide != 0 {
					cells[cols-1] = Cell{}
				}
				l.Cells = cells
			}
			t.touch(l)
		}
		for len(lines) < rows {
			lines = append(lines, newLine(cols, Cell{}, t.nextVersion()))
		}
		return lines
	}
	t.cols = cols
	t.mainLines = resize(t.mainLines, true)
	t.altLines = resize(t.altLines, false)
	if t.alt {
		t.lines = t.altLines
	} else {
		t.lines = t.mainLines
	}
	// Lines above the cursor left the top, so the cursor is now on the last row.
	t.cy = min(t.cy, rows-1)
	t.rows = rows
	t.cx = min(t.cx, cols-1)
	t.wrapNext = false
	t.top, t.bottom = 0, rows-1
	tabs := make([]bool, cols)
	copy(tabs, t.tabs)
	for i := (len(t.tabs) + 7) / 8 * 8; i < cols; i += 8 {
		tabs[i] = true
	}
	t.tabs = tabs
	for _, s := range []*savedCursor{&t.savedMain, &t.savedAlt} {
		s.x, s.y = min(s.x, cols-1), min(s.y, rows-1)
	}
}

// ---------------------------------------------------------------------------------------------
// Read access
// ---------------------------------------------------------------------------------------------

// Columns returns the screen width in cells.
func (t *Terminal) Columns() int { return t.cols }

// Rows returns the screen height in cells.
func (t *Terminal) Rows() int { return t.rows }

// Line returns screen row y of the active screen. The caller must not modify it.
func (t *Terminal) Line(y int) *Line { return t.lines[y] }

// HistoryLen returns how many lines the history holds.
func (t *Terminal) HistoryLen() int { return t.hist.count }

// HistoryLimit returns the most lines the history keeps.
func (t *Terminal) HistoryLimit() int { return t.hist.limit }

// programName is the name the terminal gives in reports (Options.Name).
func (t *Terminal) programName() string {
	if t.name == "" {
		return "sdterm"
	}
	return t.name
}

// HistoryLine returns history line i, 0 being the oldest kept. The caller must not modify it.
func (t *Terminal) HistoryLine(i int) *Line { return t.hist.at(i) }

// Scrolled counts every line that ever entered the history (it keeps counting past the limit).
func (t *Terminal) Scrolled() uint64 { return t.scrolled }

// HistoryClears counts how often the program cleared the history.
func (t *Terminal) HistoryClears() uint64 { return t.historyClears }

// Cursor returns the cursor.
func (t *Terminal) Cursor() Cursor {
	return Cursor{Column: t.cx, Row: t.cy, Hidden: t.cursorHidden, Shape: t.cursorShape, Blink: t.cursorBlink}
}

// AltScreen reports whether the alternate screen is active.
func (t *Terminal) AltScreen() bool { return t.alt }

// Mouse returns the mouse tracking the program asked for.
func (t *Terminal) Mouse() MouseTracking { return t.mouse }

// SyncOutput reports whether the program holds synchronized output (DECSET 2026).
func (t *Terminal) SyncOutput() bool { return t.syncOutput }

// Title returns the title the program set.
func (t *Terminal) Title() string { return t.title }

// WorkingDirectory returns the directory the program reported (OSC 7).
func (t *Terminal) WorkingDirectory() string { return t.workingDirectory }

// Bells counts BEL characters.
func (t *Terminal) Bells() uint64 { return t.bells }

// Style resolves a cell's style id.
func (t *Terminal) Style(id uint32) Style { return t.styles.get(id) }

// CellText returns the characters a cell shows ("" for blanks and wide tails).
func (t *Terminal) CellText(c Cell) string { return t.cellText(c) }

// SetColors sets what colour queries answer (OSC 4, 10, 11, 12): the client's terminal colours.
func (t *Terminal) SetColors(c Colors) { t.colors = c }

// ---------------------------------------------------------------------------------------------
// Table compaction
// ---------------------------------------------------------------------------------------------

// maybeCompact rebuilds the intern tables from the cells still in use once they grow too big.
func (t *Terminal) maybeCompact() {
	if len(t.styles.values) <= maxStyles && len(t.clusters.values) <= maxClusters {
		return
	}
	styles := newInternTable(Style{})
	clusters := newInternTable[string]()
	remap := func(l *Line) {
		for i := range l.Cells {
			c := &l.Cells[i]
			c.Style = styles.intern(t.styles.get(c.Style))
			if c.Flags&CellCluster != 0 {
				c.Ch = rune(clusters.intern(t.clusters.get(uint32(c.Ch))))
			}
		}
	}
	for _, l := range t.mainLines {
		remap(l)
	}
	for _, l := range t.altLines {
		remap(l)
	}
	t.hist.each(remap)
	t.styles, t.clusters = styles, clusters
	t.penValid = false
}

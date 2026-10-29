// term_test.go
// Author: Kennt Kim
// Created: 2026-10-14 · Last updated: 2026-10-29
//
// Purpose: tests of the terminal emulator — printing (wide, combining, wrapping), cursor and
// erase sequences, scrolling into the history, the alternate screen, SGR, replies, OSC, resizing,
// line versions, input encoding, and the kitty keyboard protocol's flags and keys.
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/term_test.go on 2026-10-23.

package term

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
)

type fixture struct {
	t         *testing.T
	term      *Terminal
	replies   []string
	clipboard []string
}

func newFixture(t *testing.T, cols, rows int) *fixture {
	f := &fixture{t: t}
	f.term = New(Options{Columns: cols, Rows: rows, HistoryLimit: 100,
		Reply:     func(b []byte) { f.replies = append(f.replies, string(b)) },
		Clipboard: func(text string) { f.clipboard = append(f.clipboard, text) }})
	return f
}

func (f *fixture) write(s string) *fixture {
	f.term.Write([]byte(s))
	return f
}

// row renders screen row y as text: blanks as spaces, wide tails omitted, trailing spaces trimmed.
func (f *fixture) row(y int) string { return lineText(f.term, f.term.Line(y)) }

func lineText(t *Terminal, l *Line) string {
	var b strings.Builder
	for _, c := range l.Cells {
		switch {
		case c.Flags&CellWideTail != 0:
		case c.IsBlank():
			b.WriteByte(' ')
		default:
			b.WriteString(t.CellText(c))
		}
	}
	return strings.TrimRight(b.String(), " ")
}

func (f *fixture) screen() []string {
	out := make([]string, f.term.Rows())
	for y := range out {
		out[y] = f.row(y)
	}
	return out
}

func (f *fixture) history() []string {
	out := make([]string, f.term.HistoryLen())
	for i := range out {
		out[i] = lineText(f.term, f.term.HistoryLine(i))
	}
	return out
}

func (f *fixture) expectScreen(want ...string) {
	f.t.Helper()
	got := f.screen()
	for i := range got {
		w := ""
		if i < len(want) {
			w = want[i]
		}
		if got[i] != w {
			f.t.Fatalf("row %d = %q, want %q\nscreen: %q", i, got[i], w, got)
		}
	}
}

func (f *fixture) expectCursor(col, row int) {
	f.t.Helper()
	c := f.term.Cursor()
	if c.Column != col || c.Row != row {
		f.t.Fatalf("cursor = %d,%d, want %d,%d", c.Column, c.Row, col, row)
	}
}

func TestPrintAndNewlines(t *testing.T) {
	f := newFixture(t, 10, 3).write("hello\r\nworld")
	f.expectScreen("hello", "world")
	f.expectCursor(5, 1)
}

func TestAutowrapAndPendingWrap(t *testing.T) {
	f := newFixture(t, 5, 3).write("abcde")
	f.expectCursor(4, 0) // pending wrap: still on the last column
	f.write("f")
	f.expectScreen("abcde", "f")
	if !f.term.Line(0).Wrapped {
		t.Fatal("row 0 should be marked wrapped")
	}
	// CR right after the last column cancels the pending wrap.
	f = newFixture(t, 5, 3).write("abcde\rX")
	f.expectScreen("Xbcde")
}

func TestWideCharacters(t *testing.T) {
	f := newFixture(t, 6, 3).write("한글a")
	f.expectScreen("한글a")
	f.expectCursor(5, 0)
	l := f.term.Line(0)
	if l.Cells[0].Flags&CellWide == 0 || l.Cells[1].Flags&CellWideTail == 0 {
		t.Fatalf("cells 0/1 should be a wide pair: %+v %+v", l.Cells[0], l.Cells[1])
	}
	// A wide character that does not fit in the last column wraps whole.
	f = newFixture(t, 5, 3).write("abcd한")
	f.expectScreen("abcd", "한")
	// Overwriting the tail of a wide character clears its head.
	f = newFixture(t, 6, 3).write("한\x1b[1;2Hx")
	f.expectScreen(" x")
}

func TestCombiningAndJamo(t *testing.T) {
	f := newFixture(t, 10, 2).write("éx")
	f.expectScreen("éx")
	f.expectCursor(2, 0)
	if f.term.Line(0).Cells[0].Flags&CellCluster == 0 {
		t.Fatal("e + combining acute should be one cluster cell")
	}
	// Conjoining jamo: the initial is wide, medial and final join it.
	f = newFixture(t, 10, 2).write("한!")
	f.expectScreen("한!")
	f.expectCursor(3, 0)
}

func TestUTF8SplitAcrossWrites(t *testing.T) {
	f := newFixture(t, 10, 2)
	b := []byte("가나")
	for i := range b {
		f.term.Write(b[i : i+1])
	}
	f.expectScreen("가나")
	f.write("\xff!")
	f.expectScreen("가나�!")
}

func TestCursorMovementAndErase(t *testing.T) {
	f := newFixture(t, 10, 4).write("aaaaaaaaaa\r\nbbbbbbbbbb\r\ncccccccccc")
	f.write("\x1b[2;3H\x1b[K") // erase to end of row 2 from column 3
	f.expectScreen("aaaaaaaaaa", "bb", "cccccccccc")
	f.write("\x1b[1;5H\x1b[1K")
	f.expectScreen("     aaaaa", "bb", "cccccccccc")
	f.write("\x1b[2J")
	f.expectScreen()
	f.write("\x1b[10;10H")
	f.expectCursor(9, 3)
	f.write("\x1b[3A\x1b[2D")
	f.expectCursor(7, 0)
}

func TestInsertDeleteCharacters(t *testing.T) {
	f := newFixture(t, 8, 2).write("abcdef\x1b[1;3H\x1b[2@")
	f.expectScreen("ab  cdef")
	f.write("\x1b[3P")
	f.expectScreen("abdef")
	f.write("\x1b[1;1H\x1b[2X")
	f.expectScreen("  def")
}

func TestScrollingIntoHistory(t *testing.T) {
	f := newFixture(t, 10, 3).write("1\r\n2\r\n3\r\n4\r\n5")
	f.expectScreen("3", "4", "5")
	if got := f.history(); strings.Join(got, ",") != "1,2" {
		t.Fatalf("history = %q", got)
	}
	if f.term.Scrolled() != 2 {
		t.Fatalf("scrolled = %d", f.term.Scrolled())
	}
	// The history is a ring of HistoryLimit lines.
	f = newFixture(t, 10, 2)
	for i := 0; i < 250; i++ {
		f.write("x\r\n")
	}
	if f.term.HistoryLen() != 100 || f.term.Scrolled() != 249 {
		t.Fatalf("len %d scrolled %d", f.term.HistoryLen(), f.term.Scrolled())
	}
	// CSI 3 J clears it.
	f.write("\x1b[3J")
	if f.term.HistoryLen() != 0 || f.term.HistoryClears() != 1 {
		t.Fatal("history not cleared")
	}
}

func TestScrollRegionKeepsHistoryClean(t *testing.T) {
	f := newFixture(t, 10, 4).write("top\r\na\r\nb\r\nbottom")
	f.write("\x1b[2;3r\x1b[3;1H\nc") // region rows 2-3, scroll it once
	f.expectScreen("top", "b", "c", "bottom")
	if f.term.HistoryLen() != 0 {
		t.Fatalf("history = %q", f.history())
	}
	// Reverse index at the top of the region scrolls down.
	f.write("\x1b[2;1H\x1bMz")
	f.expectScreen("top", "z", "b", "bottom")
}

func TestInsertDeleteLines(t *testing.T) {
	f := newFixture(t, 5, 4).write("1\r\n2\r\n3\r\n4\x1b[2;1H\x1b[L")
	f.expectScreen("1", "", "2", "3")
	f.write("\x1b[2M")
	f.expectScreen("1", "3")
	if f.term.HistoryLen() != 0 {
		t.Fatal("DL must not feed the history")
	}
}

func TestAlternateScreen(t *testing.T) {
	f := newFixture(t, 10, 3).write("shell$ \x1b[?1049h")
	f.expectScreen()
	if !f.term.AltScreen() {
		t.Fatal("alt screen should be active")
	}
	f.write("\x1b[Hvim\r\n~\r\n~\r\n~\r\n~") // scrolling on the alt screen never feeds the history
	if f.term.HistoryLen() != 0 {
		t.Fatalf("history = %q", f.history())
	}
	f.write("\x1b[?1049l")
	f.expectScreen("shell$")
	f.expectCursor(7, 0)
}

func TestLineVersionsFollowContent(t *testing.T) {
	f := newFixture(t, 10, 3).write("a\r\nb\r\nc")
	vb := f.term.Line(1).Version
	f.write("\r\nd") // scroll: "b" moves to row 0 with its version
	if f.term.Line(0).Version != vb {
		t.Fatal("a line that only moved must keep its version")
	}
	vd := f.term.Line(2).Version
	f.write("e")
	if f.term.Line(2).Version == vd {
		t.Fatal("a written line must change version")
	}
}

func TestSGR(t *testing.T) {
	f := newFixture(t, 20, 2).write("\x1b[1;31mA\x1b[38;5;200mB\x1b[38;2;1;2;3mC\x1b[48:2::4:5:6mD\x1b[4:3mE\x1b[0mF")
	l := f.term.Line(0)
	st := func(x int) Style { return f.term.Style(l.Cells[x].Style) }
	if s := st(0); s.Attrs&AttrBold == 0 || s.Foreground != IndexedColor(1) {
		t.Fatalf("A: %+v", s)
	}
	if s := st(1); s.Foreground != IndexedColor(200) {
		t.Fatalf("B: %+v", s)
	}
	if s := st(2); s.Foreground != RGBColor(1, 2, 3) {
		t.Fatalf("C: %+v", s)
	}
	if s := st(3); s.Background != RGBColor(4, 5, 6) || s.Foreground != RGBColor(1, 2, 3) {
		t.Fatalf("D: %+v", s)
	}
	if s := st(4); s.Underline != UnderlineCurly {
		t.Fatalf("E: %+v", s)
	}
	if s := st(5); !s.IsDefault() {
		t.Fatalf("F: %+v", s)
	}
	// Erasing uses the current background (BCE).
	f.write("\x1b[44m\x1b[2;1H\x1b[K")
	if s := f.term.Style(f.term.Line(1).Cells[5].Style); s.Background != IndexedColor(4) {
		t.Fatalf("erased cell: %+v", s)
	}
}

// XTVERSION answers with the name the engine's user gave, and the engine's own without one.
func TestXTVersionNamesTheTerminal(t *testing.T) {
	f := newFixture(t, 10, 5).write("\x1b[>q")
	if len(f.replies) != 1 || f.replies[0] != "\x1bP>|sdterm\x1b\\" {
		t.Fatalf("unnamed reply = %q", f.replies)
	}
	var got []string
	named := New(Options{Columns: 10, Rows: 5, Name: "sdTerm 1.0",
		Reply: func(b []byte) { got = append(got, string(b)) }})
	named.Write([]byte("\x1b[>q"))
	if len(got) != 1 || got[0] != "\x1bP>|sdTerm 1.0\x1b\\" {
		t.Fatalf("named reply = %q", got)
	}
}

func TestReplies(t *testing.T) {
	f := newFixture(t, 10, 5).write("\x1b[3;4H\x1b[6n\x1b[5n\x1b[c\x1b[>c\x1b[?2004$p\x1b[18t")
	want := []string{"\x1b[3;4R", "\x1b[0n", "\x1b[?62;22c", "\x1b[>1;10;0c", "\x1b[?2004;2$y", "\x1b[8;5;10t"}
	if strings.Join(f.replies, "|") != strings.Join(want, "|") {
		t.Fatalf("replies = %q", f.replies)
	}
	f.replies = nil
	f.term.SetColors(DefaultColors(true))
	f.write("\x1b]11;?\x07\x1b]10;?\x1b\\")
	want = []string{"\x1b]11;rgb:1e1e/1e1e/1e1e\x07", "\x1b]10;rgb:e5e5/e5e5/e7e7\x1b\\"}
	if strings.Join(f.replies, "|") != strings.Join(want, "|") {
		t.Fatalf("colour replies = %q", f.replies)
	}
	// A theme of the client's own: its background, cursor and colour 1 answer; colour 200 is the cube's.
	theme := DefaultColors(false)
	theme.Background, theme.Cursor, theme.Palette[1] = 0x002B36, 0x93A1A1, 0xDC322F
	f.replies = nil
	f.term.SetColors(theme)
	f.write("\x1b]11;?\x07\x1b]12;?\x07\x1b]4;1;?;200;?\x07")
	want = []string{"\x1b]11;rgb:0000/2b2b/3636\x07", "\x1b]12;rgb:9393/a1a1/a1a1\x07",
		"\x1b]4;1;rgb:dcdc/3232/2f2f\x07", "\x1b]4;200;rgb:ffff/0000/d7d7\x07"}
	if strings.Join(f.replies, "|") != strings.Join(want, "|") {
		t.Fatalf("theme colour replies = %q", f.replies)
	}
}

func TestOSCTitleDirectoryAndLinks(t *testing.T) {
	f := newFixture(t, 20, 2).write("\x1b]0;build\x07\x1b]7;file:///home/sam/My%20Dir\x1b\\")
	if f.term.Title() != "build" || f.term.WorkingDirectory() != "/home/sam/My Dir" {
		t.Fatalf("title %q dir %q", f.term.Title(), f.term.WorkingDirectory())
	}
	f.write("\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\ text")
	l := f.term.Line(0)
	if f.term.Style(l.Cells[0].Style).Link != "https://example.com" || f.term.Style(l.Cells[5].Style).Link != "" {
		t.Fatal("hyperlink not applied to exactly the link text")
	}
	// An unterminated OSC aborted by a new escape leaves no trace.
	f = newFixture(t, 20, 2).write("\x1b]0;abc\x1b[31mX")
	f.expectScreen("X")
}

func TestOSC52PutsTextOnTheClipboardButNeverReadsIt(t *testing.T) {
	url := "https://claude.com/cai/oauth/authorize?code=true&state=Xy"
	enc := base64.StdEncoding.EncodeToString([]byte(url))
	f := newFixture(t, 20, 2).
		write("\x1b]52;c;" + enc + "\x07").                                                // BEL-terminated
		write("\x1b]52;;" + base64.RawStdEncoding.EncodeToString([]byte("한글")) + "\x1b\\") // ST, unpadded
	if len(f.clipboard) != 2 || f.clipboard[0] != url || f.clipboard[1] != "한글" {
		t.Fatalf("clipboard %q", f.clipboard)
	}
	// A query is never answered; clearing, bad base64 and invalid UTF-8 change nothing.
	f.write("\x1b]52;c;?\x07\x1b]52;c;\x07\x1b]52;c;!!!\x07\x1b]52;c;" +
		base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe}) + "\x07")
	if len(f.clipboard) != 2 || len(f.replies) != 0 {
		t.Fatalf("clipboard %q replies %q", f.clipboard, f.replies)
	}
	// Nothing reaches the screen.
	f.expectScreen("")
}

func TestDECGraphics(t *testing.T) {
	f := newFixture(t, 10, 2).write("\x1b(0lqk\x1b(Bx")
	f.expectScreen("┌─┐x")
}

func TestResize(t *testing.T) {
	f := newFixture(t, 10, 4).write("1\r\n2\r\n3\r\n4")
	f.term.Resize(6, 2) // cursor on row 3: rows 0-1 move into the history
	f.expectScreen("3", "4")
	if strings.Join(f.history(), ",") != "1,2" {
		t.Fatalf("history = %q", f.history())
	}
	f.expectCursor(1, 1)
	f.term.Resize(8, 3)
	f.expectScreen("3", "4", "")
	f.write("\x1b[1;8Hz")
	f.expectScreen("3      z", "4")
}

func TestKeyEncoding(t *testing.T) {
	f := newFixture(t, 10, 2)
	cases := []struct {
		key  Key
		mods Mod
		text string
		want string
	}{
		{KeyUp, 0, "", "\x1b[A"},
		{KeyUp, ModControl, "", "\x1b[1;5A"},
		{KeyRight, ModShift | ModAlt, "", "\x1b[1;4C"},
		{KeyDelete, 0, "", "\x1b[3~"},
		{KeyPageDown, ModControl, "", "\x1b[6;5~"},
		{KeyF1, 0, "", "\x1bOP"},
		{KeyF1 + 4, 0, "", "\x1b[15~"},
		{KeyF1 + 11, ModShift, "", "\x1b[24;2~"},
		{KeyTab, ModShift, "", "\x1b[Z"},
		{KeyBackspace, 0, "", "\x7f"},
		{KeyEnter, ModAlt, "", "\x1b\r"},
		{KeyCharacter, ModControl, "c", "\x03"},
		{KeyCharacter, ModControl, "[", "\x1b"},
		{KeyCharacter, ModControl | ModAlt, "x", "\x1b\x18"},
		{KeyCharacter, ModAlt, "b", "\x1bb"},
	}
	for _, c := range cases {
		if got := string(f.term.EncodeKey(c.key, c.mods, c.text)); got != c.want {
			t.Errorf("key %d mods %d %q = %q, want %q", c.key, c.mods, c.text, got, c.want)
		}
	}
	f.write("\x1b[?1h")
	if got := string(f.term.EncodeKey(KeyUp, 0, "")); got != "\x1bOA" {
		t.Errorf("application cursor up = %q", got)
	}
}

func TestKittyKeyboardFlags(t *testing.T) {
	f := newFixture(t, 10, 2)
	query := func() string {
		f.replies = nil
		f.write("\x1b[?u")
		return strings.Join(f.replies, "")
	}
	if got := query(); got != "\x1b[?0u" {
		t.Fatalf("initial flags = %q", got)
	}
	// Only flag 1 is offered: a push of 1|2|8 sets 1.
	f.write("\x1b[>11u")
	if got := query(); got != "\x1b[?1u" {
		t.Fatalf("after push = %q", got)
	}
	// The alternate screen has its own stack.
	f.write("\x1b[?1049h")
	if got := query(); got != "\x1b[?0u" {
		t.Fatalf("alternate screen = %q", got)
	}
	f.write("\x1b[=1u")
	if got := query(); got != "\x1b[?1u" {
		t.Fatalf("set on the alternate screen = %q", got)
	}
	f.write("\x1b[=1;3u")
	if got := query(); got != "\x1b[?0u" {
		t.Fatalf("remove = %q", got)
	}
	f.write("\x1b[?1049l")
	if got := query(); got != "\x1b[?1u" {
		t.Fatalf("main screen kept its flags = %q", got)
	}
	f.write("\x1b[>0u\x1b[<u")
	if got := query(); got != "\x1b[?1u" {
		t.Fatalf("pop returns to the pushed flags = %q", got)
	}
	f.write("\x1b[<5u") // popping more than the stack holds empties it
	if got := query(); got != "\x1b[?0u" {
		t.Fatalf("after popping everything = %q", got)
	}
	for i := 0; i < keyboardStackLimit+5; i++ {
		f.write("\x1b[>1u")
	}
	if n := len(f.term.keyboardMain); n != keyboardStackLimit {
		t.Fatalf("stack holds %d entries", n)
	}
	f.write("\x1bc") // RIS
	if got := query(); got != "\x1b[?0u" {
		t.Fatalf("after a reset = %q", got)
	}
	// CSI u without a marker still restores the cursor (SCORC).
	f.write("\x1b[2;3H\x1b[s\x1b[1;1H\x1b[u")
	if c := f.term.Cursor(); c.Column != 2 || c.Row != 1 {
		t.Fatalf("SCORC cursor = %d,%d", c.Column, c.Row)
	}
}

func TestKittyKeyEncoding(t *testing.T) {
	f := newFixture(t, 10, 2)
	if got := string(f.term.EncodeKey(KeyEnter, ModShift, "")); got != "\r" {
		t.Fatalf("Shift-Enter without the protocol = %q", got)
	}
	f.write("\x1b[>1u")
	cases := []struct {
		key  Key
		mods Mod
		text string
		want string
	}{
		{KeyEnter, 0, "", "\r"},
		{KeyEnter, ModShift, "", "\x1b[13;2u"},
		{KeyEnter, ModAlt, "", "\x1b[13;3u"},
		{KeyTab, 0, "", "\t"},
		{KeyTab, ModShift, "", "\x1b[9;2u"},
		{KeyBackspace, 0, "", "\x7f"},
		{KeyBackspace, ModControl, "", "\x1b[127;5u"},
		{KeyEscape, 0, "", "\x1b[27u"},
		{KeyEscape, ModShift, "", "\x1b[27;2u"},
		{KeyCharacter, 0, "a", "a"},
		{KeyCharacter, ModShift, "A", "A"},
		{KeyCharacter, ModControl, "c", "\x1b[99;5u"},
		{KeyCharacter, ModControl | ModShift, "a", "\x1b[97;6u"},
		{KeyCharacter, ModAlt, "b", "\x1b[98;3u"},
		{KeyCharacter, ModAlt, "B", "\x1b[98;4u"},
		{KeyCharacter, ModControl, "한", "\x1b[54620;5u"},
		{KeyUp, ModShift, "", "\x1b[1;2A"},
		{KeyF1 + 2, 0, "", "\x1b[13~"},
		{KeyF1 + 2, ModControl, "", "\x1b[13;5~"},
		{KeyF1, 0, "", "\x1bOP"},
	}
	for _, c := range cases {
		if got := string(f.term.EncodeKey(c.key, c.mods, c.text)); got != c.want {
			t.Errorf("key %d mods %d %q = %q, want %q", c.key, c.mods, c.text, got, c.want)
		}
	}
	f.write("\x1b[<u")
	if got := string(f.term.EncodeKey(KeyEscape, 0, "")); got != "\x1b" {
		t.Fatalf("Escape after the pop = %q", got)
	}
}

func TestModeSequencesRebuildTheModes(t *testing.T) {
	if got := newFixture(t, 10, 2).term.ModeSequences(); len(got) != 0 {
		t.Fatalf("a terminal in its power-on state gives %q", got)
	}
	a := newFixture(t, 10, 2)
	a.write("\x1b[?1h\x1b=\x1b[?7l\x1b[4h\x1b[20h\x1b[?25l\x1b[5 q\x1b[?2004h\x1b[?1004h\x1b[?1002h\x1b[?1006h")
	a.write("\x1b[>1u\x1b[>0u\x1b[?1049h\x1b[>1u\x1b]2;a title\x07\x1b]7;file:///home/sam/a%20b\x07")
	b := newFixture(t, 10, 2)
	b.write(string(a.term.ModeSequences()))
	report := func(f *fixture) string {
		f.replies = nil
		for _, mode := range []int{1, 7, 12, 25, 66, 1002, 1004, 1006, 1049, 2004} {
			f.write("\x1b[?" + strconv.Itoa(mode) + "$p")
		}
		f.write("\x1b[4$p\x1b[20$p\x1b[?u")
		return strings.Join(f.replies, "")
	}
	if ra, rb := report(a), report(b); ra != rb {
		t.Fatalf("modes differ:\n%q\n%q", ra, rb)
	}
	if ca, cb := a.term.Cursor(), b.term.Cursor(); ca.Shape != cb.Shape || ca.Blink != cb.Blink || ca.Hidden != cb.Hidden {
		t.Fatalf("cursor %+v, want %+v", b.term.Cursor(), a.term.Cursor())
	}
	if b.term.Title() != "a title" || b.term.WorkingDirectory() != "/home/sam/a b" {
		t.Fatalf("title %q, directory %q", b.term.Title(), b.term.WorkingDirectory())
	}
	// Both kitty stacks, with the main one under the alternate screen.
	if len(b.term.keyboardMain) != 2 || len(b.term.keyboardAlt) != 1 || b.term.keyboardMain[1] != 0 {
		t.Fatalf("stacks %v %v", b.term.keyboardMain, b.term.keyboardAlt)
	}
	// Leaving the alternate screen: its stack stays, the screen is main.
	a.write("\x1b[?1049l")
	c := newFixture(t, 10, 2)
	c.write(string(a.term.ModeSequences()))
	if c.term.AltScreen() || len(c.term.keyboardAlt) != 1 {
		t.Fatalf("alt %v, stack %v", c.term.AltScreen(), c.term.keyboardAlt)
	}
}

func TestPasteEncoding(t *testing.T) {
	f := newFixture(t, 10, 2)
	if got := string(f.term.EncodePaste("a\nb\r\nc")); got != "a\rb\rc" {
		t.Fatalf("plain paste = %q", got)
	}
	f.write("\x1b[?2004h")
	if got := string(f.term.EncodePaste("x\x1b[201~y")); got != "\x1b[200~xy\x1b[201~" {
		t.Fatalf("bracketed paste = %q", got)
	}
}

func TestMouseEncoding(t *testing.T) {
	f := newFixture(t, 300, 2)
	if f.term.EncodeMouse(MousePressAction, MouseButtonLeft, 1, 1, 0) != nil {
		t.Fatal("no report without mouse tracking")
	}
	f.write("\x1b[?1000h")
	if got := string(f.term.EncodeMouse(MousePressAction, MouseButtonLeft, 2, 3, 0)); got != "\x1b[M #$" {
		t.Fatalf("X10 press = %q", got)
	}
	if got := string(f.term.EncodeMouse(MouseReleaseAction, MouseButtonLeft, 2, 3, 0)); got != "\x1b[M##$" {
		t.Fatalf("X10 release = %q", got)
	}
	if f.term.EncodeMouse(MouseMoveAction, MouseButtonLeft, 2, 3, 0) != nil {
		t.Fatal("1000 does not report motion")
	}
	if f.term.EncodeMouse(MousePressAction, MouseButtonLeft, 250, 3, 0) != nil {
		t.Fatal("X10 cannot express column 251")
	}
	f.write("\x1b[?1002h\x1b[?1006h")
	if got := string(f.term.EncodeMouse(MouseMoveAction, MouseButtonLeft, 250, 3, ModControl)); got != "\x1b[<48;251;4M" {
		t.Fatalf("SGR drag = %q", got)
	}
	if got := string(f.term.EncodeMouse(MouseReleaseAction, MouseButtonRight, 0, 0, 0)); got != "\x1b[<2;1;1m" {
		t.Fatalf("SGR release = %q", got)
	}
	if got := string(f.term.EncodeMouse(MouseWheelDown, MouseButtonNone, 0, 0, 0)); got != "\x1b[<65;1;1M" {
		t.Fatalf("SGR wheel = %q", got)
	}
	// Alternate screen without mouse tracking: the wheel becomes arrow keys.
	g := newFixture(t, 10, 2).write("\x1b[?1049h")
	if got := string(g.term.EncodeMouse(MouseWheelUp, MouseButtonNone, 0, 0, 0)); got != "\x1b[A" {
		t.Fatalf("alt-screen wheel = %q", got)
	}
}

func TestFocusAndSyncModes(t *testing.T) {
	f := newFixture(t, 10, 2)
	if f.term.EncodeFocus(true) != nil {
		t.Fatal("focus reports only when asked")
	}
	f.write("\x1b[?1004h\x1b[?2026h")
	if string(f.term.EncodeFocus(false)) != "\x1b[O" || !f.term.SyncOutput() {
		t.Fatal("focus/sync modes not set")
	}
	f.write("\x1b[?2026l")
	if f.term.SyncOutput() {
		t.Fatal("sync output not released")
	}
}

func TestWidths(t *testing.T) {
	cases := map[rune]int{'a': 1, '한': 2, '中': 2, '😀': 2, '́': 0, '‍': 0, 'ᅡ': 0, 'é': 1, '─': 1, '­': 1}
	for r, want := range cases {
		if got := RuneWidth(r); got != want {
			t.Errorf("width(%U) = %d, want %d", r, got, want)
		}
	}
}

func TestStyleTableCompaction(t *testing.T) {
	f := newFixture(t, 4, 2)
	var b strings.Builder
	for i := 0; i < maxStyles+10; i++ {
		b.WriteString("\x1b[38;2;1;" + strconv.Itoa(i%256) + ";" + strconv.Itoa(i/256) + "mx\r")
	}
	f.write(b.String())
	if len(f.term.styles.values) > 16 {
		t.Fatalf("style table not compacted: %d", len(f.term.styles.values))
	}
	f.write("\x1b[0my")
	if s := f.term.Style(f.term.Line(0).Cells[1].Style); !s.IsDefault() {
		t.Fatalf("pen after compaction: %+v", s)
	}
}

func BenchmarkWrite(b *testing.B) {
	var buf strings.Builder
	for i := 0; i < 2000; i++ {
		buf.WriteString("\x1b[32mINFO\x1b[0m request id=" + strconv.Itoa(i) + " 한글 로그 줄입니다 path=/var/lib/x\r\n")
	}
	data := []byte(buf.String())
	t := New(Options{Columns: 160, Rows: 50})
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t.Write(data)
	}
}

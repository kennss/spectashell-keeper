// input.go
// Author: Kennt Kim
// Created: 2026-10-14 · Last updated: 2026-10-23
//
// Purpose: turning user input (keys, pastes, mouse actions, focus changes) into the bytes the
// program expects, according to the modes it set — xterm's encodings (spec/PROTOCOL.md §7.5).
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/input.go on 2026-10-23.
// - Key and Mod values equal the protocol's TerminalKeyCode and TerminalModifier values.
// - Modified special keys use xterm's "CSI 1 ; m X" / "CSI n ; m ~" forms with m = 1 + modifier
//   bits (shift 1, alt 2, control 4). modifyOtherKeys is not offered.
// - When a program sets the kitty keyboard protocol's flag 1 (keyboard.go), keys xterm encodes
//   ambiguously are sent as "CSI code ; m u": Escape, Enter, Tab and Backspace with a modifier
//   (Shift-Enter is CSI 13;2u), and characters with Control or Alt (the code is the character's,
//   a letter's in lower case). Unmodified Enter, Tab and Backspace stay as they are, so a shell
//   left in the mode by a crashed program still runs commands; F3 becomes "CSI 13 ~", as
//   "CSI 1;m R" reads as a cursor position report.
// - Alt (Option set to act as Meta) prefixes ESC, as xterm's metaSendsEscape.
// - Mouse coordinates are 0-based here and 1-based on the wire. The X10/normal encoding cannot
//   express columns or rows past 222 and drops such events.

package term

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Key is a key that is not plain text.
type Key int

const (
	KeyCharacter Key = 1
	KeyEnter     Key = 2
	KeyTab       Key = 3
	KeyBackspace Key = 4
	KeyEscape    Key = 5
	KeyUp        Key = 6
	KeyDown      Key = 7
	KeyLeft      Key = 8
	KeyRight     Key = 9
	KeyHome      Key = 10
	KeyEnd       Key = 11
	KeyPageUp    Key = 12
	KeyPageDown  Key = 13
	KeyInsert    Key = 14
	KeyDelete    Key = 15
	KeyF1        Key = 101 // F1 + n-1 = Fn, up to F20
)

// Mod is a set of modifiers.
type Mod uint32

const (
	ModShift   Mod = 1
	ModAlt     Mod = 2
	ModControl Mod = 4
)

// EncodeKey returns the bytes for a key press. text is the character of a KeyCharacter key
// without Control or Alt applied.
func (t *Terminal) EncodeKey(key Key, mods Mod, text string) []byte {
	m := 1 + int(mods&(ModShift|ModAlt|ModControl))
	modified := m > 1
	csiMod := func(final string) []byte {
		return []byte("\x1b[1;" + strconv.Itoa(m) + final)
	}
	tilde := func(n int) []byte {
		if modified {
			return []byte("\x1b[" + strconv.Itoa(n) + ";" + strconv.Itoa(m) + "~")
		}
		return []byte("\x1b[" + strconv.Itoa(n) + "~")
	}
	cursor := func(final string) []byte {
		switch {
		case modified:
			return csiMod(final)
		case t.cursorKeysApp:
			return []byte("\x1bO" + final)
		default:
			return []byte("\x1b[" + final)
		}
	}
	meta := func(b []byte) []byte {
		if mods&ModAlt != 0 {
			return append([]byte{0x1b}, b...)
		}
		return b
	}

	if t.keyboardFlags()&keyboardDisambiguate != 0 {
		if b := t.encodeKittyKey(key, mods, text); b != nil {
			return b
		}
	}

	switch key {
	case KeyUp:
		return cursor("A")
	case KeyDown:
		return cursor("B")
	case KeyRight:
		return cursor("C")
	case KeyLeft:
		return cursor("D")
	case KeyHome:
		return cursor("H")
	case KeyEnd:
		return cursor("F")
	case KeyInsert:
		return tilde(2)
	case KeyDelete:
		return tilde(3)
	case KeyPageUp:
		return tilde(5)
	case KeyPageDown:
		return tilde(6)
	case KeyEnter:
		if t.newlineMode {
			return meta([]byte("\r\n"))
		}
		return meta([]byte("\r"))
	case KeyTab:
		if mods&ModShift != 0 {
			return []byte("\x1b[Z")
		}
		return meta([]byte("\t"))
	case KeyBackspace:
		if mods&ModControl != 0 {
			return meta([]byte{0x08})
		}
		return meta([]byte{0x7f})
	case KeyEscape:
		return meta([]byte{0x1b})
	case KeyCharacter:
		if mods&ModControl != 0 {
			if b, ok := controlByte(text); ok {
				return meta([]byte{b})
			}
		}
		return meta([]byte(text))
	}
	if key >= KeyF1 && key < KeyF1+20 {
		n := int(key - KeyF1 + 1)
		if n <= 4 {
			final := string(rune('P' + n - 1))
			if modified {
				return csiMod(final)
			}
			return []byte("\x1bO" + final)
		}
		codes := [...]int{15, 17, 18, 19, 20, 21, 23, 24, 25, 26, 28, 29, 31, 32, 33, 34}
		return tilde(codes[n-5])
	}
	return nil
}

// encodeKittyKey returns a key's encoding under the kitty protocol's flag 1, or nil when it is
// the same as xterm's.
func (t *Terminal) encodeKittyKey(key Key, mods Mod, text string) []byte {
	m := 1 + int(mods&(ModShift|ModAlt|ModControl))
	csiU := func(code int) []byte {
		if m == 1 {
			return []byte("\x1b[" + strconv.Itoa(code) + "u")
		}
		return []byte("\x1b[" + strconv.Itoa(code) + ";" + strconv.Itoa(m) + "u")
	}
	switch key {
	case KeyEscape:
		return csiU(27)
	case KeyEnter, KeyTab, KeyBackspace:
		if m == 1 {
			return nil
		}
		return csiU(map[Key]int{KeyEnter: 13, KeyTab: 9, KeyBackspace: 127}[key])
	case KeyF1 + 2:
		if m == 1 {
			return []byte("\x1b[13~")
		}
		return []byte("\x1b[13;" + strconv.Itoa(m) + "~")
	case KeyCharacter:
		if mods&(ModAlt|ModControl) == 0 {
			return nil
		}
		r, size := utf8.DecodeRuneInString(text)
		if size == 0 || size != len(text) || r == utf8.RuneError {
			return nil
		}
		// The code is the key's, without Shift: an upper-case letter is its lower case with Shift
		// (clients send Meta's letters with Shift applied and only Alt held).
		if unicode.IsUpper(r) {
			r = unicode.ToLower(r)
			m = 1 + int((mods|ModShift)&(ModShift|ModAlt|ModControl))
		}
		return csiU(int(r))
	}
	return nil
}

// controlByte maps a character to its Control combination (Control-A = 0x01, Control-Space =
// 0x00, Control-[ = ESC, ...), as US keyboards produce them.
func controlByte(text string) (byte, bool) {
	if len(text) != 1 {
		return 0, false
	}
	c := text[0]
	switch {
	case c >= 'a' && c <= 'z':
		return c - 'a' + 1, true
	case c >= 'A' && c <= 'Z':
		return c - 'A' + 1, true
	}
	switch c {
	case ' ', '@', '2':
		return 0x00, true
	case '[', '3':
		return 0x1b, true
	case '\\', '4':
		return 0x1c, true
	case ']', '5':
		return 0x1d, true
	case '^', '6':
		return 0x1e, true
	case '_', '/', '-', '7':
		return 0x1f, true
	case '8', '?':
		return 0x7f, true
	}
	return 0, false
}

// EncodePaste returns pasted text as the program should receive it: line breaks as CR (as if
// typed), bracketed when the program asked for it, with any end marker inside removed so the
// text cannot end the bracket early.
func (t *Terminal) EncodePaste(text string) []byte {
	text = strings.ReplaceAll(text, "\r\n", "\r")
	text = strings.ReplaceAll(text, "\n", "\r")
	if !t.bracketedPaste {
		return []byte(text)
	}
	text = strings.ReplaceAll(text, "\x1b[201~", "")
	return []byte("\x1b[200~" + text + "\x1b[201~")
}

// EncodeFocus returns the focus report, or nil when the program did not ask for them.
func (t *Terminal) EncodeFocus(focused bool) []byte {
	if !t.focusEvents {
		return nil
	}
	if focused {
		return []byte("\x1b[I")
	}
	return []byte("\x1b[O")
}

// MouseAction is what the mouse did.
type MouseAction int

const (
	MousePressAction   MouseAction = 1
	MouseReleaseAction MouseAction = 2
	MouseMoveAction    MouseAction = 3
	MouseWheelUp       MouseAction = 4
	MouseWheelDown     MouseAction = 5
	MouseWheelLeft     MouseAction = 6
	MouseWheelRight    MouseAction = 7
)

// MouseButton is the button involved (0 = none).
type MouseButton int

const (
	MouseButtonNone   MouseButton = 0
	MouseButtonLeft   MouseButton = 1
	MouseButtonMiddle MouseButton = 2
	MouseButtonRight  MouseButton = 3
)

// EncodeMouse returns the report for a mouse action, or nil when the program's mode does not
// want it. On the alternate screen without mouse tracking, wheel actions become cursor keys
// (three lines per notch is the client's business: it sends one action per line).
func (t *Terminal) EncodeMouse(action MouseAction, button MouseButton, col, row int, mods Mod) []byte {
	wheel := action >= MouseWheelUp
	if t.mouse == MouseNone {
		if wheel && t.alt {
			switch action {
			case MouseWheelUp:
				return t.EncodeKey(KeyUp, 0, "")
			case MouseWheelDown:
				return t.EncodeKey(KeyDown, 0, "")
			}
		}
		return nil
	}
	switch action {
	case MouseReleaseAction:
		if t.mouse == MousePress {
			return nil
		}
	case MouseMoveAction:
		if t.mouse < MouseDrag || (t.mouse == MouseDrag && button == MouseButtonNone) {
			return nil
		}
	}

	var code int
	switch {
	case wheel:
		code = 64 + int(action-MouseWheelUp)
	case action == MouseReleaseAction && !t.mouseSGR:
		code = 3
	case button == MouseButtonNone:
		code = 3 // motion without a button
	default:
		code = int(button - MouseButtonLeft)
	}
	if action == MouseMoveAction {
		code += 32
	}
	if t.mouse != MousePress {
		if mods&ModShift != 0 {
			code += 4
		}
		if mods&ModAlt != 0 {
			code += 8
		}
		if mods&ModControl != 0 {
			code += 16
		}
	}

	if t.mouseSGR {
		final := "M"
		if action == MouseReleaseAction {
			final = "m"
		}
		return []byte("\x1b[<" + strconv.Itoa(code) + ";" + strconv.Itoa(col+1) + ";" + strconv.Itoa(row+1) + final)
	}
	if col > 222 || row > 222 {
		return nil
	}
	return []byte{0x1b, '[', 'M', byte(32 + code), byte(33 + col), byte(33 + row)}
}

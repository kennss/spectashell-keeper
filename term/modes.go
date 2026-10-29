// modes.go
// Author: Kennt Kim
// Created: 2026-10-16 · Last updated: 2026-10-23
//
// Purpose: a terminal's modes as the sequences that set them (ModeSequences), so that a fresh
// terminal can be brought to the state another one had. A keeper replays only the program's
// recent output to a new session (internal/keeper); modes the program set before that — bracketed
// paste, mouse reports, the kitty keyboard flags, the alternate screen, the title — are put back
// by these sequences, written before the replayed output.
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/modes.go on 2026-10-23.
// - Only what differs from the power-on state is written; a terminal in that state gives none.
// - Covered: cursor keys and keypad (DECCKM, DECKPAM), autowrap, insert mode, LNM, the cursor's
//   visibility, shape and blinking, bracketed paste, focus events, mouse tracking and its SGR
//   encoding, the alternate screen, the kitty keyboard stacks of both screens, the title, the
//   working directory, and what the program says it is doing (its OSC 7501 records and OSC 9;4
//   progress, notify.go) — so a coding agent's status survives a session change (Docs/SDTERM-2.md
//   §3.2) — and the commands running (prompts.go), so a long one's end is still reported (§4.2).
//   Not covered: what the replayed output redraws anyway — screen contents,
//   the cursor's position, the pen, the scroll region and origin mode, character sets.
// - The alternate screen is entered with mode 1047 (no saved cursor, no clear): its contents come
//   from the replay. A main-screen kitty stack is pushed before entering it, an alternate-screen
//   stack after.

package term

import (
	"net/url"
	"strconv"
	"strings"
)

// ModeSequences returns the sequences that bring a terminal in its power-on state to t's modes.
func (t *Terminal) ModeSequences() []byte {
	var b strings.Builder
	private := func(mode int, on bool) {
		b.WriteString("\x1b[?" + strconv.Itoa(mode))
		if on {
			b.WriteByte('h')
		} else {
			b.WriteByte('l')
		}
	}
	if t.cursorKeysApp {
		private(1, true)
	}
	if t.keypadApp {
		b.WriteString("\x1b=")
	}
	if !t.autowrap {
		private(7, false)
	}
	if t.insertMode {
		b.WriteString("\x1b[4h")
	}
	if t.newlineMode {
		b.WriteString("\x1b[20h")
	}
	if t.cursorHidden {
		private(25, false)
	}
	if t.cursorShape != CursorBlock || t.cursorBlink {
		ps := map[CursorShape]int{CursorBlock: 2, CursorUnderline: 4, CursorBar: 6}[t.cursorShape]
		if t.cursorBlink {
			ps--
		}
		b.WriteString("\x1b[" + strconv.Itoa(ps) + " q")
	}
	if t.bracketedPaste {
		private(2004, true)
	}
	if t.focusEvents {
		private(1004, true)
	}
	switch t.mouse {
	case MousePress:
		private(9, true)
	case MousePressRelease:
		private(1000, true)
	case MouseDrag:
		private(1002, true)
	case MouseMotion:
		private(1003, true)
	}
	if t.mouseSGR {
		private(1006, true)
	}
	push := func(stack []int) {
		for _, flags := range stack {
			b.WriteString("\x1b[>" + strconv.Itoa(flags) + "u")
		}
	}
	push(t.keyboardMain)
	if len(t.keyboardAlt) > 0 || t.alt {
		private(1047, true)
		push(t.keyboardAlt)
		if !t.alt {
			private(1047, false)
		}
	}
	if t.title != "" {
		b.WriteString("\x1b]2;" + t.title + "\x07")
	}
	if t.workingDirectory != "" {
		u := url.URL{Scheme: "file", Path: t.workingDirectory}
		b.WriteString("\x1b]7;" + u.String() + "\x07")
	}
	if t.remoteDirectory != "" {
		b.WriteString("\x1b]7;" + t.remoteDirectory + "\x07")
	}
	t.statusSequences(&b)
	t.commandSequences(&b)
	return []byte(b.String())
}

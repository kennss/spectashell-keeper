// keyboard.go
// Author: Kennt Kim
// Created: 2026-10-16 · Last updated: 2026-10-23
//
// Purpose: the kitty keyboard protocol's progressive enhancements — the flags a program sets to
// receive keys it cannot tell apart in xterm's encoding (Shift-Enter from Enter, Escape from the
// start of a sequence, Control-I from Tab). The sequences that query, push, pop and set them are
// here; how keys are encoded under them is in input.go.
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/keyboard.go on 2026-10-23.
// - Only flag 1 ("disambiguate escape codes") is offered. A program asking for more gets flag 1,
//   and a query reports what is set, which is how the protocol tells a program what it has.
// - The main and alternate screens keep separate stacks, as the protocol asks; a full reset
//   (RIS) empties both.
// - A stack holds at most keyboardStackLimit entries; pushing onto a full one drops the oldest.

package term

const (
	// keyboardDisambiguate is the kitty protocol's flag 1.
	keyboardDisambiguate = 1
	// keyboardSupported is every flag this terminal offers.
	keyboardSupported = keyboardDisambiguate
	// keyboardStackLimit bounds each screen's stack of flags.
	keyboardStackLimit = 16
)

// keyboardStack returns the stack of the active screen.
func (t *Terminal) keyboardStack() *[]int {
	if t.alt {
		return &t.keyboardAlt
	}
	return &t.keyboardMain
}

// keyboardFlags returns the active screen's enhancement flags (0: xterm's encoding).
func (t *Terminal) keyboardFlags() int {
	s := *t.keyboardStack()
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1]
}

// keyboardSequence handles CSI ? u (query), CSI > flags u (push), CSI < n u (pop) and
// CSI = flags ; mode u (set: 1 replaces, 2 adds, 3 removes).
func (t *Terminal) keyboardSequence(private byte) {
	stack := t.keyboardStack()
	switch private {
	case '?':
		t.replyf("\x1b[?%du", t.keyboardFlags())
	case '>':
		if len(*stack) >= keyboardStackLimit {
			*stack = append((*stack)[:0], (*stack)[1:]...)
		}
		*stack = append(*stack, t.rawParam(0)&keyboardSupported)
	case '<':
		n := min(t.param(0, 1), len(*stack))
		*stack = (*stack)[:len(*stack)-n]
	case '=':
		flags := t.rawParam(0) & keyboardSupported
		if len(*stack) == 0 {
			*stack = append(*stack, 0)
		}
		top := &(*stack)[len(*stack)-1]
		switch t.param(1, 1) {
		case 1:
			*top = flags
		case 2:
			*top |= flags
		case 3:
			*top &^= flags
		}
	}
}

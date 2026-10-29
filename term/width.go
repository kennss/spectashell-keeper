// width.go
// Author: Kennt Kim
// Created: 2026-10-14 · Last updated: 2026-10-23
//
// Purpose: how many terminal columns a character takes, following the C library's wcwidth
// conventions that programs on the host use to lay out text (spec/PROTOCOL.md §7.2).
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/width.go on 2026-10-23.
// - 0: combining marks (Mn, Me), format characters (Cf) except the soft hyphen, the zero-width
//   space, and Hangul medial vowels / final consonants (U+1160-U+11FF, U+D7B0-U+D7FF), which
//   join the syllable they follow.
// - 2: East Asian Wide and Fullwidth characters (CJK, Hangul syllables, emoji with emoji
//   presentation), per Unicode's EastAsianWidth as packaged by golang.org/x/text/width.
// - 1: everything else, including East Asian Ambiguous characters (as glibc does in UTF-8 locales).
// - Widths of the Basic Multilingual Plane are precomputed into a 64 Ki table at start-up (a few
//   milliseconds); other planes are looked up on demand.

package term

import (
	"unicode"

	"golang.org/x/text/width"
)

var bmpWidth [0x10000]uint8

func init() {
	for r := rune(0); r < 0x10000; r++ {
		bmpWidth[r] = uint8(computeWidth(r))
	}
}

// RuneWidth returns 0, 1 or 2 for a printable character.
func RuneWidth(r rune) int {
	if r < 0x10000 {
		if r < 0 {
			return 1
		}
		return int(bmpWidth[r])
	}
	return computeWidth(r)
}

func computeWidth(r rune) int {
	switch {
	case r < 0x300:
		// Latin and its controls: controls never reach the screen, the soft hyphen is visible.
		return 1
	case r == 0x200B,
		r >= 0x1160 && r <= 0x11FF,
		r >= 0xD7B0 && r <= 0xD7FF,
		unicode.Is(unicode.Mn, r),
		unicode.Is(unicode.Me, r),
		unicode.Is(unicode.Cf, r):
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

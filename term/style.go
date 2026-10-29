// style.go
// Author: Kennt Kim
// Created: 2026-10-14 · Last updated: 2026-10-23
//
// Purpose: cell styles (colors, attributes, underline, hyperlink) and the per-terminal tables
// that intern styles and multi-character cell contents, so a cell stays 12 bytes.
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/style.go on 2026-10-23.
// - Color packs its kind into the top byte: 0 = the theme default, colorIndexed | 0-255, or
//   colorRGB | 0xRRGGBB.
// - Attr bits and Underline values equal the protocol's TerminalStyleFlag bits and
//   TerminalUnderline values, so encoding a frame is a plain conversion.
// - Interned ids are only meaningful inside one Terminal. Style id 0 is always the default style
//   and cluster ids start at 0. The tables are rebuilt from the cells still in use when they grow
//   past their limits (Terminal.compactTables); ids then change, which is invisible outside the
//   package because readers resolve ids through the Terminal while holding it.

package term

// Color is a cell color.
type Color uint32

const (
	colorKindMask = 0xFF000000
	colorIndexed  = 1 << 24
	colorRGB      = 2 << 24
)

// DefaultColor is the theme's default foreground or background.
const DefaultColor Color = 0

// IndexedColor is one of the 256 palette colors.
func IndexedColor(i uint8) Color { return Color(colorIndexed | uint32(i)) }

// RGBColor is a direct color.
func RGBColor(r, g, b uint8) Color {
	return Color(colorRGB | uint32(r)<<16 | uint32(g)<<8 | uint32(b))
}

// Index returns the palette index of an indexed color.
func (c Color) Index() (uint8, bool) {
	return uint8(c), c&colorKindMask == colorIndexed
}

// RGB returns 0xRRGGBB of a direct color.
func (c Color) RGB() (uint32, bool) {
	return uint32(c) & 0xFFFFFF, c&colorKindMask == colorRGB
}

// Attr is a set of text attributes.
type Attr uint16

const (
	AttrBold Attr = 1 << iota
	AttrFaint
	AttrItalic
	AttrBlink
	AttrInverse
	AttrInvisible
	AttrStrikethrough
	AttrOverline
)

// Underline is an underline style.
type Underline uint8

const (
	UnderlineNone Underline = iota
	UnderlineSingle
	UnderlineDouble
	UnderlineCurly
	UnderlineDotted
	UnderlineDashed
)

// Style is how a cell looks. It is comparable, so it can key the intern table.
type Style struct {
	Foreground     Color
	Background     Color
	UnderlineColor Color
	Attrs          Attr
	Underline      Underline
	// Link is the OSC 8 hyperlink target, or empty.
	Link string
}

// IsDefault reports whether s is the default style.
func (s Style) IsDefault() bool { return s == Style{} }

// Table limits that trigger a rebuild. A program cycling through millions of RGB colors would
// otherwise grow the style table without bound.
const (
	maxStyles   = 1 << 16
	maxClusters = 1 << 14
)

// internTable maps values to dense ids and back.
type internTable[T comparable] struct {
	values []T
	ids    map[T]uint32
}

func newInternTable[T comparable](first ...T) internTable[T] {
	t := internTable[T]{ids: map[T]uint32{}}
	for _, v := range first {
		t.intern(v)
	}
	return t
}

func (t *internTable[T]) intern(v T) uint32 {
	if id, ok := t.ids[v]; ok {
		return id
	}
	id := uint32(len(t.values))
	t.values = append(t.values, v)
	t.ids[v] = id
	return id
}

func (t *internTable[T]) get(id uint32) T {
	if int(id) < len(t.values) {
		return t.values[id]
	}
	var zero T
	return zero
}

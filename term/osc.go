// osc.go
// Author: Kennt Kim
// Created: 2026-10-14 · Last updated: 2026-10-23
//
// Purpose: OSC sequences — title (0/2), working directory (7), hyperlinks (8), the clipboard (52)
// and the colour queries (4, 10, 11, 12) programs use to tell a dark background from a light one.
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/osc.go on 2026-10-23.
// - Replies end with the same terminator as the query (BEL or ST), as xterm does.
// - The clipboard (OSC 52 ; selection ; base64 text): a program may put text on the device's
//   clipboard (Options.Clipboard) — "c to copy" in a TUI, copying over SSH — whatever selection it
//   names. It may never read it: a query ("?") gets no answer, so a program cannot see what the
//   user copied elsewhere. Empty data (a request to clear) and a sequence cut at maxOSC are
//   ignored, as is text that is not valid UTF-8 or longer than maxClipboard bytes.
// - Setting colours (OSC 4/10/11/12 with a value) is accepted and ignored. Notifications (9, 777,
//   99) and the program's status (7501, 9;4) are in notify.go; shell integration's prompt marks
//   (133, and VS Code's 633) in prompts.go.
// - OSC 7 names a host: a directory on another one (`ssh` to a host whose shell reports it) is not
//   the terminal's working directory — New Tab must not try to start a shell there — and a prompt
//   after it carries no directory (Mark.Directory). Local is no host, "localhost", or
//   Options.Hostname (Ghostty's rule).
// - Colour answers are the client's terminal colours (Options.Colors / SetColors: the user's theme,
//   from ClientEnvironment.terminal_colors), so `vim` and friends pick matching themes.
//   DefaultColors are the client's Basic light and dark themes (DeskRender TerminalTheme), for a
//   client that sends none.

package term

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxClipboard bounds the text a program can put on the clipboard (OSC 52), in bytes.
const maxClipboard = 32 * 1024

// Colors are the colours a terminal is drawn with, each 0xRRGGBB: what colour queries answer.
type Colors struct {
	Foreground, Background, Cursor uint32
	// Palette holds colours 0-15; 16-255 are PaletteColor's everywhere.
	Palette [16]uint32
}

// DefaultColors are the client's Basic light or dark terminal theme (DeskRender TerminalTheme).
func DefaultColors(dark bool) Colors {
	if dark {
		return Colors{Foreground: 0xE5E5E7, Background: 0x1E1E1E, Cursor: 0xE5E5E7, Palette: [16]uint32{
			0x1E1E1E, 0xF2564D, 0x4FC94F, 0xD9C24A, 0x5C8DF2, 0xD670D6, 0x45C5D6, 0xC8C8C8,
			0x6E6E6E, 0xFF7A70, 0x76E576, 0xF2DE6A, 0x82AAFF, 0xF08CF0, 0x6FE2F0, 0xFFFFFF}}
	}
	return Colors{Foreground: 0x1D1D1F, Background: 0xFFFFFF, Cursor: 0x1D1D1F, Palette: [16]uint32{
		0x000000, 0xB21818, 0x18A218, 0x9A8A00, 0x1E3FC8, 0xA21FA2, 0x0E8A96, 0xB0B0B0,
		0x666666, 0xE01F1F, 0x22C022, 0xB8A400, 0x3B5CFF, 0xD030D0, 0x15A8B8, 0xD8D8D8}}
}

// PaletteColor returns the RGB value of palette index i: for 16-255 the 256-colour cube and grey
// ramp, the same in every theme; for 0-15 the light default (a theme's own are Colors.Palette).
func PaletteColor(i uint8) uint32 {
	switch {
	case i < 16:
		return DefaultColors(false).Palette[i]
	case i < 232:
		v := int(i) - 16
		level := func(n int) uint32 {
			if n == 0 {
				return 0
			}
			return uint32(55 + 40*n)
		}
		return level(v/36)<<16 | level(v/6%6)<<8 | level(v%6)
	default:
		g := uint32(8 + 10*(int(i)-232))
		return g<<16 | g<<8 | g
	}
}

func (t *Terminal) oscDispatch(bel bool) {
	s := string(t.osc)
	cmd, arg, _ := strings.Cut(s, ";")
	end := "\x1b\\"
	if bel {
		end = "\x07"
	}
	switch cmd {
	case "0", "2":
		t.title = sanitizeTitle(arg)
	case "7":
		// Another host's directory (a shell reached by ssh) is not a directory here, and the
		// prompts that follow have none.
		if u, err := url.Parse(arg); err == nil && u.Scheme == "file" && strings.HasPrefix(u.Path, "/") {
			if t.isLocalHost(u.Host) {
				t.workingDirectory, t.remoteDirectory = u.Path, ""
			} else {
				t.remoteDirectory = arg
			}
		}
	case "8":
		// OSC 8 ; params ; URI
		_, uri, _ := strings.Cut(arg, ";")
		s := t.pen
		s.Link = uri
		t.setPen(s)
	case "9":
		t.osc9(arg)
	case "777":
		t.osc777(arg)
	case "99":
		t.osc99(arg, end)
	case "7501":
		t.osc7501(arg, end)
	case "133":
		t.osc133(arg, false)
	case "633":
		t.osc133(arg, true)
	case "52":
		_, data, _ := strings.Cut(arg, ";")
		if t.clipboard == nil || data == "" || data == "?" || len(t.osc) >= maxOSC {
			return
		}
		text, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			text, err = base64.RawStdEncoding.DecodeString(data)
		}
		if err != nil || len(text) == 0 || len(text) > maxClipboard || !utf8.Valid(text) {
			return
		}
		t.clipboard(string(text))
	case "4":
		fields := strings.Split(arg, ";")
		for i := 0; i+1 < len(fields); i += 2 {
			if fields[i+1] != "?" {
				continue
			}
			idx, err := strconv.Atoi(fields[i])
			if err != nil || idx < 0 || idx > 255 {
				continue
			}
			c := PaletteColor(uint8(idx))
			if idx < 16 {
				c = t.colors.Palette[idx]
			}
			t.reply([]byte(fmt.Sprintf("\x1b]4;%d;%s%s", idx, xrgb(c), end)))
		}
	case "10", "11", "12":
		if arg != "?" {
			return
		}
		c := t.colors.Foreground
		switch cmd {
		case "11":
			c = t.colors.Background
		case "12":
			c = t.colors.Cursor
		}
		t.reply([]byte(fmt.Sprintf("\x1b]%s;%s%s", cmd, xrgb(c), end)))
	}
}

// isLocalHost says whether an OSC 7 host names this machine.
func (t *Terminal) isLocalHost(host string) bool {
	host = strings.ToLower(host)
	return host == "" || host == "localhost" || (t.hostname != "" && host == t.hostname)
}

// xrgb formats 0xRRGGBB as an X11 colour spec with 16-bit components.
func xrgb(c uint32) string {
	r, g, b := c>>16&0xFF, c>>8&0xFF, c&0xFF
	return fmt.Sprintf("rgb:%04x/%04x/%04x", r*0x101, g*0x101, b*0x101)
}

// sanitizeTitle drops control characters and limits the length, since the title is shown in the
// window chrome.
func sanitizeTitle(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r < 0x20 || r == 0x7F || (r >= 0x80 && r < 0xA0) {
			continue
		}
		if n == 256 {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// terminal.go
// Author: Kennt Kim
// Created: 2026-10-29 · Last updated: 2026-10-29
//
// Purpose: the keeper's terminal as a console needs it (Docs/DESIGN.md §5.8): raw, so every byte
// typed reaches the board or the server as typed; and its size, as it is and as it changes, for
// telnet's NAWS.
//
// Notes:
//   - The terminal is the keeper's pseudo-terminal, which the console is the only program on; it
//     goes when the console ends, so its settings need no restoring.
// - Moved here from core/internal/console on 2026-10-29, with the keeper program
//   (Docs/DESIGN.md §3.2).

//go:build darwin || linux

package console

import (
	"os"
	"os/signal"

	"golang.org/x/sys/unix"
)

// MakeRaw puts the terminal f in raw mode: no line editing, no echo, no signal from Control-C, no
// character translated either way.
func MakeRaw(f *os.File) error {
	fd := int(f.Fd())
	t, err := unix.IoctlGetTermios(fd, getTermios)
	if err != nil {
		return err
	}
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL |
		unix.IXON | unix.IXOFF
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB
	t.Cflag |= unix.CS8
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	return unix.IoctlSetTermios(fd, setTermiosRequest, t)
}

// Size is the terminal's size, columns and rows; zero when it cannot be read.
func Size(f *os.File) [2]int {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return [2]int{}
	}
	return [2]int{int(ws.Col), int(ws.Row)}
}

// Resizes brings the terminal's size each time it changes (SIGWINCH).
func Resizes(f *os.File) <-chan [2]int {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, unix.SIGWINCH)
	sizes := make(chan [2]int, 1)
	go func() {
		for range signals {
			sizes <- Size(f)
		}
	}()
	return sizes
}

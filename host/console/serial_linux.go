// serial_linux.go
// Author: Kennt Kim
// Created: 2026-10-27 · Last updated: 2026-10-29
//
// Purpose: a serial port's settings on Linux: termios2 (TCGETS2 and TCSETS2), whose BOTHER takes any
// speed in c_ispeed and c_ospeed — the old termios takes only the B-constants.
//
// Notes:
//   - x/sys's Termios is the kernel's termios2: it carries the two speed fields.
//   - The ports a console uses: USB adapters (ttyUSB, CDC-ACM boards as ttyACM) and a board's own
//     UARTs (ttyS, ttyAMA on a Raspberry Pi); /dev/serial/by-id names the USB ones (serial.go).
// - Moved here from core/internal/console on 2026-10-29, with the keeper program
//   (Docs/DESIGN.md §3.2).

package console

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type termFlag = uint32

const (
	getTermios        = unix.TCGETS2
	setTermiosRequest = unix.TCSETS2
)

// A Raspberry Pi's GPIO console is /dev/serial0, whichever UART it is on (ttyAMA0, or the mini
// UART ttyS0); both names are listed.
var portPatterns = []string{"/dev/ttyUSB*", "/dev/ttyACM*", "/dev/serial[0-9]", "/dev/ttyAMA*", "/dev/ttyS*"}

const permissionHint = ": add yourself to the dialout group (sudo usermod -aG dialout $USER) and log in again"

func setTermios(fd int, t *unix.Termios, speed int) error {
	t.Cflag &^= unix.CBAUD
	t.Cflag |= unix.BOTHER
	t.Ispeed, t.Ospeed = uint32(speed), uint32(speed)
	return unix.IoctlSetTermios(fd, unix.TCSETS2, t)
}

// placeholderPort says whether a ttyS port is one of the legacy UARTs the kernel lists on every PC,
// there or not: its type is 0, no UART found at its address (PORT_UNKNOWN, as setserial reads it).
// *Evidence (2026-10-29):* on the GPU box (Linux 6.x) all 32 were listed; their devices sit on the
// serial-base bus, not the platform bus pyserial's rule looks for, and every one's type is 0.
func placeholderPort(name string) bool {
	if !strings.HasPrefix(name, "ttyS") {
		return false
	}
	kind, err := os.ReadFile(filepath.Join("/sys/class/tty", name, "type"))
	return err != nil || strings.TrimSpace(string(kind)) == "0"
}

// KeepAwake does nothing on Linux: a machine a board hangs off is not one that sleeps by itself.
func KeepAwake() bool { return false }

func sendBreak(fd int) error {
	// TCSBRK with 0: a break of 0.25 to 0.5 seconds.
	return unix.IoctlSetInt(fd, unix.TCSBRK, 0)
}

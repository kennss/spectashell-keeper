// serial_darwin.go
// Author: Kennt Kim
// Created: 2026-10-27 · Last updated: 2026-10-30
//
// Purpose: a serial port's settings on macOS: termios through TIOCGETA and TIOCSETA, a standard
// speed with them, any other through IOSSIOSPEED — termios alone takes only the standard ones, and
// an ESP32's 921600 is not one of them.
//
// Notes:
//   - The ports a console uses are the callout devices, /dev/cu.*: opening /dev/tty.* waits for the
//     modem's carrier, which a USB adapter never raises.
// - Moved here from core/internal/console on 2026-10-29, with the keeper program
//   (Docs/design/01-DESIGN.md §3.2).

package console

import (
	"os"
	"os/exec"
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type termFlag = uint64

const (
	getTermios        = unix.TIOCGETA
	setTermiosRequest = unix.TIOCSETA
)

// ioSSIOSpeed is IOSSIOSPEED, _IOW('T', 2, speed_t) from IOKit/serial/ioss.h.
const ioSSIOSpeed = 0x80085402

var portPatterns = []string{"/dev/cu.*"}

const permissionHint = ""

func setTermios(fd int, t *unix.Termios, speed int) error {
	// A standard speed through termios itself; any other through IOSSIOSPEED after a standard one,
	// since TIOCSETA refuses a speed it does not know. A pseudo-terminal takes only the first.
	standard := standardSpeeds[speed]
	if standard {
		t.Ispeed, t.Ospeed = uint64(speed), uint64(speed)
	} else {
		t.Ispeed, t.Ospeed = 9600, 9600
	}
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, t); err != nil || standard {
		return err
	}
	s := uint64(speed)
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), ioSSIOSpeed, uintptr(unsafe.Pointer(&s)))
	if errno != 0 {
		return errno
	}
	return nil
}

// standardSpeeds are the speeds macOS's termios takes as they are (B-constants equal their speed).
var standardSpeeds = map[int]bool{50: true, 75: true, 110: true, 134: true, 150: true, 200: true, 300: true,
	600: true, 1200: true, 1800: true, 2400: true, 4800: true, 7200: true, 9600: true, 14400: true, 19200: true,
	28800: true, 38400: true, 57600: true, 76800: true, 115200: true, 230400: true}

// KeepAwake holds the Mac out of idle sleep while this program runs (caffeinate -i -w <pid>), so a
// board's console is not cut while nobody touches the Mac; a closed lid still sleeps. It says
// whether the assertion was taken.
func KeepAwake() bool {
	return exec.Command("/usr/bin/caffeinate", "-i", "-w", strconv.Itoa(os.Getpid())).Start() == nil
}

// placeholderPort: a Mac lists only the ports it has.
func placeholderPort(string) bool { return false }

func sendBreak(fd int) error {
	if err := unix.IoctlSetInt(fd, unix.TIOCSBRK, 0); err != nil {
		return err
	}
	time.Sleep(250 * time.Millisecond)
	return unix.IoctlSetInt(fd, unix.TIOCCBRK, 0)
}

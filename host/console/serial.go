// serial.go
// Author: Kennt Kim
// Created: 2026-10-27 · Last updated: 2026-10-30
//
// Purpose: a serial port opened as a console needs it (Docs/design/02-CONNECTIONS.md §6): raw, at
// the speed and framing asked for (8N1 and the rest), with the flow control asked for, held
// exclusively so a second program does not read half of its bytes; and the ports a machine has, for
// the app to choose from.
//
// Notes:
//   - The port is opened non-blocking and handed to os.NewFile, so its reads go through the
//     runtime's poller and Close ends a read in progress — what releasing the port needs.
//   - Held with flock and TIOCEXCL: no other open of the device succeeds while it is held (TIOCEXCL,
//     root's aside), and a second console, or pyserial opened exclusive, is told it is in use
//     (flock). A flashing tool that finds it busy is the reason the bridge can release the port
//     (bridge.go). A pseudo-terminal keeps only the lock, as the tests find.
//   - The speed is any whole number of bits per second, not only the standard ones: ESP32 boards
//     talk at 921600 and 1500000. How a system takes one is its own (serial_darwin.go,
//     serial_linux.go).
//   - Permission: on Linux a port belongs to the dialout group; the error says so.
// - Moved here from core/internal/console on 2026-10-29, with the keeper program
//   (Docs/design/01-DESIGN.md §3.2).

//go:build darwin || linux

package console

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// Parity is a port's parity bit.
type Parity string

const (
	ParityNone Parity = "none"
	ParityEven Parity = "even"
	ParityOdd  Parity = "odd"
)

// Flow is a port's flow control.
type Flow string

const (
	FlowNone     Flow = "none"
	FlowHardware Flow = "rts/cts"
	FlowSoftware Flow = "xon/xoff"
)

// Port is how a serial port is opened.
type Port struct {
	// Device is its path: /dev/cu.usbserial-110, /dev/ttyUSB0, /dev/serial/by-id/….
	Device string
	// Speed in bits per second.
	Speed int
	// DataBits is 5 to 8.
	DataBits int
	Parity   Parity
	// StopBits is 1 or 2.
	StopBits int
	Flow     Flow
}

// Framing is the port's framing as a console's manual writes it: "8N1".
func (p Port) Framing() string {
	parity := map[Parity]string{ParityNone: "N", ParityEven: "E", ParityOdd: "O"}[p.Parity]
	return fmt.Sprintf("%d%s%d", p.DataBits, parity, p.StopBits)
}

// Check says what is wrong with p, if anything.
func (p Port) Check() error {
	switch {
	case p.Device == "":
		return errors.New("no serial port named")
	case p.Speed <= 0:
		return fmt.Errorf("speed %d is not a speed", p.Speed)
	case p.DataBits < 5 || p.DataBits > 8:
		return fmt.Errorf("%d data bits: a port takes 5 to 8", p.DataBits)
	case p.StopBits != 1 && p.StopBits != 2:
		return fmt.Errorf("%d stop bits: a port takes 1 or 2", p.StopBits)
	case p.Parity != ParityNone && p.Parity != ParityEven && p.Parity != ParityOdd:
		return fmt.Errorf("parity %q: none, even or odd", p.Parity)
	case p.Flow != FlowNone && p.Flow != FlowHardware && p.Flow != FlowSoftware:
		return fmt.Errorf("flow control %q: none, rts/cts or xon/xoff", p.Flow)
	}
	return nil
}

// Open opens the port for a console, exclusively.
func Open(p Port) (*os.File, error) {
	if err := p.Check(); err != nil {
		return nil, err
	}
	fd, err := unix.Open(p.Device, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, openError(p.Device, err)
	}
	fail := func(what string, err error) (*os.File, error) {
		unix.Close(fd)
		return nil, fmt.Errorf("%s: %s: %w", p.Device, what, err)
	}
	// Held two ways: an advisory lock, which another console of this program and pyserial's
	// exclusive opens respect (and a pseudo-terminal keeps), then TIOCEXCL, which the system's
	// serial drivers enforce for every other open.
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(fd)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%s is in use by another program", p.Device)
		}
		return nil, fmt.Errorf("%s: holding the port: %w", p.Device, err)
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCEXCL, 0); err != nil {
		return fail("holding the port", err)
	}
	t, err := unix.IoctlGetTermios(fd, getTermios)
	if err != nil {
		return fail("reading its settings", err)
	}
	raw(t, p)
	if err := setTermios(fd, t, p.Speed); err != nil {
		return fail(fmt.Sprintf("setting %d bits per second, %s", p.Speed, p.Framing()), err)
	}
	return os.NewFile(uintptr(fd), p.Device), nil
}

// raw sets t for a console: no line editing, no echo, no character translated, the framing and
// flow control of p, the modem lines ignored (CLOCAL) so an adapter without them opens.
func raw(t *unix.Termios, p Port) {
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL |
		unix.IXON | unix.IXOFF | unix.IXANY | unix.INPCK
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.PARODD | unix.CSTOPB | unix.CRTSCTS
	t.Cflag |= unix.CREAD | unix.CLOCAL
	t.Cflag |= map[int]termFlag{5: unix.CS5, 6: unix.CS6, 7: unix.CS7, 8: unix.CS8}[p.DataBits]
	switch p.Parity {
	case ParityEven:
		t.Cflag |= unix.PARENB
		t.Iflag |= unix.INPCK
	case ParityOdd:
		t.Cflag |= unix.PARENB | unix.PARODD
		t.Iflag |= unix.INPCK
	}
	if p.StopBits == 2 {
		t.Cflag |= unix.CSTOPB
	}
	switch p.Flow {
	case FlowHardware:
		t.Cflag |= unix.CRTSCTS
	case FlowSoftware:
		t.Iflag |= unix.IXON | unix.IXOFF
	}
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
}

// openError says why a port would not open, in the words that help: busy, missing, or not the
// user's to open.
func openError(device string, err error) error {
	switch {
	case errors.Is(err, unix.EBUSY):
		return fmt.Errorf("%s is in use by another program", device)
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENXIO):
		return fmt.Errorf("%s is not there: is the adapter plugged in?", device)
	case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
		return fmt.Errorf("%s: %w%s", device, err, permissionHint)
	default:
		return fmt.Errorf("%s: %w", device, err)
	}
}

// SendBreak sends a break on the port for about a quarter of a second, as a console's Send Break
// does (a router's ROM monitor, a password recovery).
func SendBreak(f *os.File) error {
	return sendBreak(int(f.Fd()))
}

// A serial port a machine has.
type Found struct {
	// Path opens it.
	Path string `json:"path"`
	// Name says what it is, where the system says: Linux's /dev/serial/by-id name
	// ("usb-FTDI_FT232R_USB_UART_A50285BI-if00-port0"); else empty.
	Name string `json:"name,omitempty"`
}

// Ports lists the serial ports of this machine, the ones a console would use first.
func Ports() []Found {
	var found []Found
	for _, pattern := range portPatterns {
		paths, _ := filepath.Glob(pattern)
		for _, path := range paths {
			if skipPort(path) {
				continue
			}
			found = append(found, Found{Path: path, Name: portNames()[path]})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].Path < found[j].Path })
	return found
}

// portNames maps a port's device path to its /dev/serial/by-id name, on a system that has them.
func portNames() map[string]string {
	names := map[string]string{}
	links, _ := filepath.Glob("/dev/serial/by-id/*")
	for _, link := range links {
		if target, err := filepath.EvalSymlinks(link); err == nil {
			names[target] = filepath.Base(link)
		}
	}
	return names
}

// skipPort leaves out the ports no console is on: a Mac's Bluetooth and debug ports, and the legacy
// UARTs Linux lists whether or not the machine has them (placeholderPort, by their type).
func skipPort(path string) bool {
	base := filepath.Base(path)
	return strings.Contains(base, "Bluetooth") || strings.HasSuffix(base, "debug-console") || placeholderPort(base)
}

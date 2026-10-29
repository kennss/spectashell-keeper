// console_test.go
// Author: Kennt Kim
// Created: 2026-10-29 · Last updated: 2026-10-29
//
// Purpose: tests of the console bridge and the serial port: bytes both ways; the app's Release
// Port, Reopen Port and Send Break, typing dropped while released; a sequence that is not the
// app's passed on as typed; the console's ends — a port unplugged (1), a telnet server closing the
// session (0), the terminal closing (0); a telnet session through the bridge; a port's settings
// checked and its framing written; and a pseudo-terminal opened as a port, raw, and a second console on it refused.
//
// Notes:
//   - The remote is one end of net.Pipe, the terminal a pair of os.Pipes; every step waits on the
//     bytes it expects, never on a clock.
// - Moved here from core/internal/console on 2026-10-29, with the keeper program
//   (Docs/DESIGN.md §3.2).

package console

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// rig is a bridge between a test's terminal and remotes it hands out.
type rig struct {
	t        *testing.T
	typing   *os.File      // what the test types
	screen   *bufio.Reader // what the terminal shows
	remotes  chan net.Conn // the far ends of the remotes the bridge opens, in order
	opens    atomic.Int32
	breaks   atomic.Int32
	exit     chan int
	failOpen atomic.Bool
}

func newRig(t *testing.T, configure func(*Bridge)) *rig {
	t.Helper()
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	r := &rig{t: t, typing: inW, screen: bufio.NewReader(outR), remotes: make(chan net.Conn, 4), exit: make(chan int, 1)}
	b := &Bridge{In: inR, Out: outW, Name: "/dev/test · 115200 8N1", Releasable: true,
		Open: func() (Remote, Translator, error) {
			r.opens.Add(1)
			if r.failOpen.Load() {
				return nil, nil, io.ErrClosedPipe
			}
			near, far := net.Pipe()
			r.remotes <- far
			return near, Plain{}, nil
		},
		SendBreak: func(Remote) error { r.breaks.Add(1); return nil }}
	if configure != nil {
		configure(b)
	}
	go func() { r.exit <- b.Run(make(chan [2]int)); outW.Close() }()
	t.Cleanup(func() { inW.Close(); inR.Close(); outR.Close() })
	return r
}

// see reads the screen until it has shown want.
func (r *rig) see(want string) {
	r.t.Helper()
	var got []byte
	for !bytes.Contains(got, []byte(want)) {
		b, err := r.screen.ReadByte()
		if err != nil {
			r.t.Fatalf("screen ended at %q, waiting for %q", got, want)
		}
		got = append(got, b)
	}
}

func (r *rig) typeIn(s string) {
	if _, err := r.typing.Write([]byte(s)); err != nil {
		r.t.Fatal(err)
	}
}

// hear reads n bytes from a remote's far end.
func hear(t *testing.T, far net.Conn, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(far, buf); err != nil {
		t.Fatal(err)
	}
	return string(buf)
}

func TestBytesGoBothWaysAsTheyAre(t *testing.T) {
	r := newRig(t, func(b *Bridge) { b.Title = "cu.test" })
	r.see("\x1b]2;cu.test\a")
	r.see("/dev/test · 115200 8N1")
	far := <-r.remotes
	go far.Write([]byte("U-Boot 2024.01\r\n=> "))
	r.see("=> ")
	r.typeIn("printenv\r\x03")
	if got := hear(t, far, 10); got != "printenv\r\x03" {
		t.Fatalf("the board heard %q", got)
	}
}

func TestReleaseDropsTypingAndReopenTakesThePortBack(t *testing.T) {
	r := newRig(t, nil)
	first := <-r.remotes
	r.typeIn(string(ControlSequence(Release)))
	r.see("Released /dev/test")
	if _, err := first.Read(make([]byte, 1)); err == nil {
		t.Fatal("the port is still open after Release Port")
	}
	r.typeIn("lost")
	r.see("goes nowhere until Reopen Port")
	r.typeIn("lost again") // told once only
	r.typeIn(string(ControlSequence(Reopen)))
	second := <-r.remotes
	r.see("/dev/test · 115200 8N1")
	r.typeIn("ok")
	if got := hear(t, second, 2); got != "ok" {
		t.Fatalf("after Reopen Port the board heard %q", got)
	}
	if n := r.opens.Load(); n != 2 {
		t.Fatalf("%d opens, want 2", n)
	}
}

func TestReopenThatFailsSaysWhyAndStaysReleased(t *testing.T) {
	r := newRig(t, nil)
	<-r.remotes
	r.typeIn(string(ControlSequence(Release)))
	r.see("Released")
	r.failOpen.Store(true)
	r.typeIn(string(ControlSequence(Reopen)))
	r.see("Could not take the port back")
}

func TestSendBreakAndASequenceNotTheApps(t *testing.T) {
	r := newRig(t, nil)
	far := <-r.remotes
	r.typeIn("a" + string(ControlSequence(Break)) + "b")
	if got := hear(t, far, 2); got != "ab" {
		t.Fatalf("around the break the board heard %q", got)
	}
	if r.breaks.Load() != 1 {
		t.Fatalf("%d breaks, want 1", r.breaks.Load())
	}
	// An OSC 7503 with no end, and Escape alone: typed as they are, at once.
	r.typeIn("\x1b]7503;nope")
	if got := hear(t, far, 11); got != "\x1b]7503;nope" {
		t.Fatalf("heard %q", got)
	}
	r.typeIn("\x1b")
	if got := hear(t, far, 1); got != "\x1b" {
		t.Fatalf("Escape heard as %q", got)
	}
}

func TestAPortUnpluggedEndsTheProgramWithAFailure(t *testing.T) {
	r := newRig(t, nil)
	far := <-r.remotes
	far.Close()
	r.see("the adapter was unplugged")
	if code := <-r.exit; code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}

func TestAServerClosingTheSessionEndsTheProgram(t *testing.T) {
	r := newRig(t, func(b *Bridge) { b.EOFIsExit = true; b.Releasable = false })
	far := <-r.remotes
	far.Close()
	r.see("The session was closed")
	if code := <-r.exit; code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
}

func TestTheTerminalClosingEndsTheProgram(t *testing.T) {
	r := newRig(t, nil)
	<-r.remotes
	r.typing.Close()
	if code := <-r.exit; code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
}

func TestATelnetSessionThroughTheBridge(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	heard := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.ReadFull(conn, make([]byte, 9)) // the terminal's opening requests
		conn.Write([]byte("Router>"))
		line, _ := bufio.NewReader(conn).ReadString('\n')
		heard <- line
		conn.Write([]byte("\r\nbye\r\n"))
	}()
	r := newRig(t, func(b *Bridge) {
		b.Open = func() (Remote, Translator, error) { return OpenTelnet(listener.Addr().String(), 80, 24) }
		b.EOFIsExit, b.Releasable = true, false
	})
	r.see("Router>")
	r.typeIn("show version\r")
	if got := <-heard; got != "show version\r\n" {
		t.Fatalf("the server heard %q", got)
	}
	r.see("bye")
	r.see("The session was closed")
	if code := <-r.exit; code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestAPortsSettingsAreCheckedAndItsFramingWritten(t *testing.T) {
	good := Port{Device: "/dev/x", Speed: 921600, DataBits: 7, Parity: ParityEven, StopBits: 2, Flow: FlowHardware}
	if err := good.Check(); err != nil || good.Framing() != "7E2" {
		t.Fatalf("Check %v, framing %q", err, good.Framing())
	}
	for _, bad := range []Port{{Speed: 9600, DataBits: 8, Parity: ParityNone, StopBits: 1, Flow: FlowNone},
		{Device: "/dev/x", DataBits: 8, Parity: ParityNone, StopBits: 1, Flow: FlowNone},
		{Device: "/dev/x", Speed: 9600, DataBits: 9, Parity: ParityNone, StopBits: 1, Flow: FlowNone},
		{Device: "/dev/x", Speed: 9600, DataBits: 8, Parity: "mark", StopBits: 1, Flow: FlowNone},
		{Device: "/dev/x", Speed: 9600, DataBits: 8, Parity: ParityNone, StopBits: 3, Flow: FlowNone}} {
		if bad.Check() == nil {
			t.Errorf("%+v passed", bad)
		}
	}
	if err := (Port{Device: "/dev/no-such-port", Speed: 9600, DataBits: 8, Parity: ParityNone, StopBits: 1,
		Flow: FlowNone}).Check(); err != nil {
		t.Fatal(err)
	}
	_, err := Open(Port{Device: "/dev/no-such-port", Speed: 9600, DataBits: 8, Parity: ParityNone, StopBits: 1,
		Flow: FlowNone})
	if err == nil || !strings.Contains(err.Error(), "is the adapter plugged in") {
		t.Fatalf("a missing port: %v", err)
	}
}

// A pseudo-terminal stands in for an adapter: it takes termios as a port does. Opened, it is raw
// and held, so a second open is refused.
func TestAPseudoTerminalOpensAsARawHeldPort(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skip("no pseudo-terminal:", err)
	}
	defer ptmx.Close()
	name := tty.Name()
	tty.Close()
	f, err := Open(Port{Device: name, Speed: 115200, DataBits: 8, Parity: ParityNone, StopBits: 1, Flow: FlowNone})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := unix.IoctlGetTermios(int(f.Fd()), getTermios)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lflag&(unix.ICANON|unix.ECHO|unix.ISIG) != 0 || got.Oflag&unix.OPOST != 0 || got.Cflag&unix.CSIZE != unix.CS8 {
		t.Fatalf("not raw 8-bit: lflag %#x oflag %#x cflag %#x", got.Lflag, got.Oflag, got.Cflag)
	}
	// A second console on the same port is told it is in use (macOS's pseudo-terminals ignore
	// TIOCEXCL, so the lock is what holds here; a real adapter refuses the open itself too).
	_, err = Open(Port{Device: name, Speed: 9600, DataBits: 8, Parity: ParityNone, StopBits: 1, Flow: FlowNone})
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("a second console on a held port: %v", err)
	}
	go ptmx.Write([]byte("boot\n"))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(f, buf); err != nil || string(buf) != "boot\n" {
		t.Fatalf("read %q, %v: raw keeps the newline as sent", buf, err)
	}
}

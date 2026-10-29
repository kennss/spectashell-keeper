// telnet_test.go
// Author: Kennt Kim
// Created: 2026-10-27 · Last updated: 2026-10-29
//
// Purpose: tests of the telnet codec and link: the requests a terminal opens with, each option
// answered once and never in a loop, TERMINAL-TYPE and NAWS as the server asks, a command split
// across reads, CR NUL and a doubled IAC in the output, the Return key as CR LF and IAC doubled
// in the typing, binary when agreed; and a session over TCP with a server that negotiates, echoes
// and closes.
//
// Notes:
//   - The server in the link test is a goroutine on a loopback listener, written byte for byte as a
//     router's telnetd talks; nothing waits on a clock.

package telnet

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"testing"

	"github.com/kennss/spectashell-keeper/keeper"
	"github.com/kennss/spectashell-keeper/termhost"
)

var _ termhost.Link = (*Link)(nil)

func iac(b ...byte) []byte { return append([]byte{cmdIAC}, b...) }

func TestATerminalOpensAskingForWhatItNeeds(t *testing.T) {
	_, hello := NewCodec(80, 24)
	want := bytes.Join([][]byte{iac(cmdDO, optSGA), iac(cmdWILL, optTermType), iac(cmdWILL, optNAWS)}, nil)
	if !bytes.Equal(hello, want) {
		t.Fatalf("hello = %v, want %v", hello, want)
	}
}

func TestEachOptionIsAnsweredOnceAndNeverInALoop(t *testing.T) {
	c, _ := NewCodec(132, 43)
	// The server agrees to what we asked (no answer), offers echo (DO it), and asks for an
	// option we refuse (WONT) and one we offered.
	in := bytes.Join([][]byte{iac(cmdWILL, optSGA), iac(cmdWILL, optEcho), iac(cmdDO, 5), iac(cmdWILL, 39)}, nil)
	out, reply := c.Receive(in)
	if len(out) != 0 {
		t.Fatalf("output %q from commands", out)
	}
	want := bytes.Join([][]byte{iac(cmdDO, optEcho), iac(cmdWONT, 5), iac(cmdDONT, 39)}, nil)
	if !bytes.Equal(reply, want) {
		t.Fatalf("reply = %v, want %v", reply, want)
	}
	// Said again, an option in force is not answered again.
	if _, reply := c.Receive(iac(cmdWILL, optEcho)); len(reply) != 0 {
		t.Fatalf("WILL ECHO again answered %v", reply)
	}
	// The server stops echoing: answered once, then silence.
	if _, reply := c.Receive(iac(cmdWONT, optEcho)); !bytes.Equal(reply, iac(cmdDONT, optEcho)) {
		t.Fatalf("WONT ECHO answered %v", reply)
	}
	if _, reply := c.Receive(iac(cmdWONT, optEcho)); len(reply) != 0 {
		t.Fatalf("WONT ECHO again answered %v", reply)
	}
}

func TestTerminalTypeAndWindowSizeAreSentAsTheServerAsks(t *testing.T) {
	c, _ := NewCodec(80, 24)
	// DO NAWS answers our WILL: no second WILL, but the size at once.
	_, reply := c.Receive(iac(cmdDO, optNAWS))
	if want := []byte{cmdIAC, cmdSB, optNAWS, 0, 80, 0, 24, cmdIAC, cmdSE}; !bytes.Equal(reply, want) {
		t.Fatalf("NAWS = %v, want %v", reply, want)
	}
	// A width of 255 is IAC in the size: doubled.
	if got, want := c.Resize(255, 50), []byte{cmdIAC, cmdSB, optNAWS, 0, 255, 255, 0, 50, cmdIAC, cmdSE}; !bytes.Equal(got, want) {
		t.Fatalf("NAWS 255 = %v, want %v", got, want)
	}
	if _, reply := c.Receive(iac(cmdDO, optTermType)); len(reply) != 0 {
		t.Fatalf("DO TERMINAL-TYPE, asked for, answered %v", reply)
	}
	_, reply = c.Receive(iac(cmdSB, optTermType, ttSEND, cmdIAC, cmdSE))
	want := append(iac(cmdSB, optTermType, ttIS), append([]byte(TerminalType), cmdIAC, cmdSE)...)
	if !bytes.Equal(reply, want) {
		t.Fatalf("TERMINAL-TYPE IS = %q, want %q", reply, want)
	}
}

func TestNoSizeIsSentBeforeTheServerAgreesToNAWS(t *testing.T) {
	c, _ := NewCodec(80, 24)
	if msg := c.Resize(100, 30); msg != nil {
		t.Fatalf("size sent unasked: %v", msg)
	}
	_, reply := c.Receive(iac(cmdDO, optNAWS))
	if want := []byte{cmdIAC, cmdSB, optNAWS, 0, 100, 0, 30, cmdIAC, cmdSE}; !bytes.Equal(reply, want) {
		t.Fatalf("the size the server first hears = %v, want %v", reply, want)
	}
}

func TestOutputSplitAcrossReadsCRNULAndADoubledIAC(t *testing.T) {
	c, _ := NewCodec(80, 24)
	var got []byte
	var replies []byte
	for _, piece := range [][]byte{[]byte("Router>"), {cmdIAC}, {cmdWILL}, {optEcho, 'a', '\r'}, {0, 'b', '\r', '\n',
		cmdIAC}, {cmdIAC, 'c', cmdIAC, 249 /* GA */}} {
		out, reply := c.Receive(piece)
		got = append(got, out...)
		replies = append(replies, reply...)
	}
	if want := []byte("Router>a\rb\r\n\xffc"); !bytes.Equal(got, want) {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if !bytes.Equal(replies, iac(cmdDO, optEcho)) {
		t.Fatalf("replies = %v", replies)
	}
}

func TestReturnIsCRLFAndIACIsDoubledUnlessBinary(t *testing.T) {
	c, _ := NewCodec(80, 24)
	if got, want := c.Send([]byte("ls\r\xff\r\n")), []byte("ls\r\n\xff\xff\r\n"); !bytes.Equal(got, want) {
		t.Fatalf("typed = %q, want %q", got, want)
	}
	c.Receive(iac(cmdDO, optBinary))
	if got, want := c.Send([]byte("a\r\xff")), []byte("a\r\xff\xff"); !bytes.Equal(got, want) {
		t.Fatalf("typed in binary = %q, want %q", got, want)
	}
}

// A session over TCP: the server negotiates, prompts, echoes a line, and closes — the program's
// exit.
func TestASessionOverTCPEndsWhenTheServerCloses(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	heard := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		hello := make([]byte, 9)
		if _, err := io.ReadFull(r, hello); err != nil {
			return
		}
		conn.Write(bytes.Join([][]byte{iac(cmdWILL, optEcho), iac(cmdWILL, optSGA), iac(cmdDO, optNAWS),
			[]byte("login: ")}, nil))
		// DO ECHO, then the size, then what was typed, up to its end of line.
		var got []byte
		for !bytes.HasSuffix(got, []byte("\r\n")) {
			b, err := r.ReadByte()
			if err != nil {
				return
			}
			got = append(got, b)
		}
		heard <- got
		conn.Write([]byte("sam\r\nbye\r\n"))
	}()

	link, err := Dial(context.Background(), listener.Addr().String(), 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer link.Close()
	var output bytes.Buffer
	var exit *keeper.Exit
	replayed := false
	prompted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- link.ReadEvents(keeper.Events{
			Output: func(b []byte) {
				output.Write(b)
				if output.String() == "login: " {
					close(prompted)
				}
			},
			ReplayEnd: func() { replayed = true },
			Exit:      func(e keeper.Exit) { exit = &e },
		})
	}()
	// Typed once the prompt shows, as a person types: by then the link has answered the server.
	<-prompted
	if err := link.Input([]byte("sam\r")); err != nil {
		t.Fatal(err)
	}
	got := <-heard
	want := bytes.Join([][]byte{iac(cmdDO, optEcho), {cmdIAC, cmdSB, optNAWS, 0, 100, 0, 30, cmdIAC, cmdSE},
		[]byte("sam\r\n")}, nil)
	if !bytes.Equal(got, want) {
		t.Fatalf("the server heard %v, want %v", got, want)
	}
	if err := <-done; err != io.EOF {
		t.Fatalf("ReadEvents = %v, want EOF", err)
	}
	if !replayed || exit == nil || exit.Code != 0 {
		t.Fatalf("replayed %v, exit %+v", replayed, exit)
	}
	if output.String() != "login: sam\r\nbye\r\n" {
		t.Fatalf("output = %q", output.String())
	}
}

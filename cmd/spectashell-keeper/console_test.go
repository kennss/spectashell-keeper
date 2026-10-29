// console_test.go
// Author: Kennt Kim
// Created: 2026-10-29 · Last updated: 2026-10-30
//
// Purpose: spectashell-keeper's consoles as a keeper runs them (Docs/CONNECTIONS.md §6), the real
// binary on a pseudo-terminal as a keeper gives it one: serial on another pseudo-terminal standing
// in for an adapter, its bytes both ways raw — Control-C reaching the board as a byte, not a signal
// — and Release Port letting a second console have it; telnet to a server on loopback, ended by the
// server; serial-ports printing JSON; and arguments it cannot use refused.
//
// Notes:
//   - Every step waits on the bytes it expects, read from the pseudo-terminal.
// - Moved here from core/cmd/spectashell-keeper on 2026-10-29, with the keeper program
//   (Docs/DESIGN.md §3.2).

//go:build darwin || linux

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"

	"github.com/creack/pty"

	"github.com/kennss/spectashell-keeper/host/console"
)

// screen reads what a console shows until it has shown want.
func screenShows(t *testing.T, r *bufio.Reader, want string) {
	t.Helper()
	var got []byte
	for !bytes.Contains(got, []byte(want)) {
		b, err := r.ReadByte()
		if err != nil {
			t.Fatalf("the console ended at %q, waiting for %q", got, want)
		}
		got = append(got, b)
	}
}

// runConsole starts the binary with args on a pseudo-terminal, as a keeper would.
func runConsole(t *testing.T, bin string, args ...string) (*os.File, *bufio.Reader, *exec.Cmd) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	term, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait(); term.Close() })
	return term, bufio.NewReader(term), cmd
}

func TestASerialConsoleCarriesBytesRawAndReleasesThePort(t *testing.T) {
	bin := binary(t)
	board, device, err := pty.Open()
	if err != nil {
		t.Skip("no pseudo-terminal:", err)
	}
	defer board.Close()
	name := device.Name()
	device.Close()

	term, screen, _ := runConsole(t, bin, "serial", "--speed", "115200", name)
	screenShows(t, screen, name+" · 115200 8N1")
	board.Write([]byte("U-Boot> "))
	screenShows(t, screen, "U-Boot> ")
	// Control-C is a byte for the board, not a signal for the console.
	term.Write([]byte("reset\r\x03"))
	heard := make([]byte, 7)
	if _, err := io.ReadFull(board, heard); err != nil || string(heard) != "reset\r\x03" {
		t.Fatalf("the board heard %q, %v", heard, err)
	}

	// Released, a second console can have the port; reopened while it holds it, the first is told.
	term.Write(console.ControlSequence(console.Release))
	screenShows(t, screen, "Released "+name)
	_, other, _ := runConsole(t, bin, "serial", name)
	screenShows(t, other, name+" · 115200 8N1")
	term.Write(console.ControlSequence(console.Reopen))
	screenShows(t, screen, "is in use by another program")
}

func TestATelnetConsoleEndsWhenTheServerCloses(t *testing.T) {
	bin := binary(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.ReadFull(conn, make([]byte, 9))
		conn.Write([]byte("Password: "))
		line, _ := bufio.NewReader(conn).ReadString('\n')
		conn.Write([]byte("\r\nyou typed " + line))
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	term, screen, cmd := runConsole(t, bin, "telnet", host, port)
	screenShows(t, screen, "Password: ")
	term.Write([]byte("secret\r"))
	screenShows(t, screen, "you typed secret")
	screenShows(t, screen, "The session was closed")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("telnet ended with %v, want 0", err)
	}
}

func TestSerialPortsPrintsJSONAndBadArgumentsAreRefused(t *testing.T) {
	bin := binary(t)
	out, err := exec.Command(bin, "serial-ports").Output()
	var answer SerialPortsAnswer
	if err != nil || json.Unmarshal(out, &answer) != nil || answer.Ports == nil {
		t.Fatalf("serial-ports printed %q, %v", out, err)
	}
	for _, args := range [][]string{{"serial"}, {"serial", "--parity", "mark", "/dev/x"}, {"telnet"},
		{"telnet", "h", "99999"}} {
		err := exec.Command(bin, args...).Run()
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
			t.Errorf("%v: %v, want exit 2", args, err)
		}
	}
}

// main_test.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-30
//
// Purpose: spectashell-keeper as the core drives it on a host (Docs/design/02-CONNECTIONS.md §3),
// the real binary over pipes as over SSH exec channels: start prints a keeper's id and leaves it
// running; list describes it, with this boot's id, without taking it over; attach carries the
// keeper protocol, a second attach takes the shell over from the first, and Hangup through it ends
// the keeper; join watches the shell without taking it over (2026-10-26). Every keeper started is
// hung up when the test ends, failed or not.
//
// Notes:
// - Moved here from core/cmd/spectashell-keeper on 2026-10-29, with the keeper program
//   (Docs/design/01-DESIGN.md §3.2).

//go:build unix

package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kennss/spectashell-keeper/keeper"
)

// binary builds spectashell-keeper for the test.
func binary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "spectashell-keeper")
	cmd := exec.Command("go", "build", "-ldflags", "-X main.build=test-build", "-o", out, ".")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	return out
}

// runtimeDir is a short keepers' folder (a socket's path is at most 104 bytes on macOS), whose
// keepers are hung up when the test ends.
func runtimeDir(t *testing.T) string {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "skb")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "k")
	t.Setenv("SDTERM_RUNTIME_DIR", dir)
	t.Cleanup(func() {
		for _, d := range keeper.List(dir) {
			if c, err := keeper.Dial(keeper.SocketPath(dir, d.KeeperID)); err == nil {
				c.Hangup(keeper.Who{})
			}
		}
		deadline := time.Now().Add(5 * time.Second)
		for len(keeper.List(dir)) > 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		os.RemoveAll(base)
	})
	return dir
}

// pipeConn is a command's standard input and output as one stream, as an exec channel is.
type pipeConn struct {
	io.Reader
	io.WriteCloser
	cmd *exec.Cmd
}

func (p *pipeConn) Close() error {
	p.WriteCloser.Close()
	return p.cmd.Wait()
}

func attachOver(t *testing.T, bin, id string) *keeper.Conn {
	t.Helper()
	return relayOver(t, bin, "attach", id)
}

// relayOver runs `spectashell-keeper <how> <id>` (attach or join) and speaks the protocol through it.
func relayOver(t *testing.T, bin, how, id string) *keeper.Conn {
	t.Helper()
	cmd := exec.Command(bin, how, id)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c, err := keeper.NewConn(&pipeConn{Reader: out, WriteCloser: in, cmd: cmd})
	if err != nil {
		t.Fatalf("attach %s: %v", id, err)
	}
	return c
}

func TestStartListAttach(t *testing.T) {
	bin := binary(t)
	dir := runtimeDir(t)

	cmd := exec.Command(bin, "start")
	cmd.Stdin = strings.NewReader(`{"argv":["/bin/sh"],"columns":70,"rows":20,"term_program":"sdTerm",` +
		`"env":{"LANG":"en_US.UTF-8"},"meta":{"owner":"sdTerm","persist":true}}`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("start: %v %s", err, out)
	}
	var started StartAnswer
	if err := json.Unmarshal(out, &started); err != nil || !keeper.ValidID(started.KeeperID) {
		t.Fatalf("start printed %q", out)
	}

	out, err = exec.Command(bin, "list").Output()
	if err != nil {
		t.Fatal(err)
	}
	var listed ListAnswer
	if err := json.Unmarshal(out, &listed); err != nil {
		t.Fatal(err)
	}
	// Program is what runs in the shell, read as it is listed: the shell itself at its prompt.
	if listed.Boot == "" || len(listed.Keepers) != 1 || listed.Keepers[0].KeeperID != started.KeeperID ||
		listed.Keepers[0].Build != "test-build" || !listed.Keepers[0].Meta.Persist || listed.Keepers[0].Program != "sh" {
		t.Fatalf("list = %+v", listed)
	}

	first := attachOver(t, bin, started.KeeperID)
	if first.Hello.Columns != 70 || first.Hello.Rows != 20 {
		t.Fatalf("Hello = %+v", first.Hello)
	}
	got := make(chan string, 64)
	ended := make(chan error, 1)
	go func() {
		ended <- first.ReadEvents(keeper.Events{Output: func(b []byte) { got <- string(b) }, ReplayEnd: func() {},
			Exit: func(keeper.Exit) {}})
	}()
	_ = first.Input([]byte("echo \"over-attach $TERM_PROGRAM $LANG\"\n"))
	waitOutput(t, got, "over-attach sdTerm en_US.UTF-8")

	// Another device attaches: the first is told, and the shell carries on for the second.
	second := attachOver(t, bin, started.KeeperID)
	select {
	case err := <-ended:
		if err != keeper.ErrTakenOver {
			t.Fatalf("the first attach ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first attach was not taken over")
	}
	got2 := make(chan string, 64)
	go second.ReadEvents(keeper.Events{Output: func(b []byte) { got2 <- string(b) }, ReplayEnd: func() {},
		Exit: func(keeper.Exit) {}})
	_ = second.Input([]byte("echo second-$((1+1))\n"))
	waitOutput(t, got2, "second-2")

	second.Hangup(keeper.Who{})
	deadline := time.Now().Add(5 * time.Second)
	for len(keeper.List(dir)) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the keeper still runs after Hangup: %+v", keeper.List(dir))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAttachRefusesWhatIsNotAKeeperID(t *testing.T) {
	bin := binary(t)
	runtimeDir(t)
	for _, how := range []string{"attach", "join"} {
		if err := exec.Command(bin, how, "../../etc/passwd").Run(); err == nil {
			t.Fatalf("%s took a path", how)
		}
	}
}

// A teammate joins over SSH: they see what the holder types and its answer, and the holder keeps
// the shell.
func TestJoinWatchesWithoutTakingOver(t *testing.T) {
	bin := binary(t)
	runtimeDir(t)
	cmd := exec.Command(bin, "start")
	cmd.Stdin = strings.NewReader(`{"argv":["/bin/sh"],"columns":70,"rows":20,"meta":{"owner":"sdTerm","persist":true}}`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("start: %v %s", err, out)
	}
	var started StartAnswer
	if err := json.Unmarshal(out, &started); err != nil {
		t.Fatalf("start printed %q", out)
	}
	holder := attachOver(t, bin, started.KeeperID)
	defer holder.Hangup(keeper.Who{})
	ended := make(chan error, 1)
	go func() {
		ended <- holder.ReadEvents(keeper.Events{Output: func([]byte) {}, ReplayEnd: func() {}, Exit: func(keeper.Exit) {}})
	}()

	joiner := relayOver(t, bin, "join", started.KeeperID)
	defer joiner.Close()
	got := make(chan string, 64)
	go joiner.ReadEvents(keeper.Events{Output: func(b []byte) { got <- string(b) }, ReplayEnd: func() {},
		Exit: func(keeper.Exit) {}})
	_ = holder.Input([]byte("echo watched-$((2+3))\n"))
	waitOutput(t, got, "watched-5")
	select {
	case err := <-ended:
		t.Fatalf("the holder was taken over by a joiner: %v", err)
	default:
	}
}

func waitOutput(t *testing.T, got <-chan string, want string) {
	t.Helper()
	var all strings.Builder
	deadline := time.After(10 * time.Second)
	for !strings.Contains(all.String(), want) {
		select {
		case s := <-got:
			all.WriteString(s)
		case <-deadline:
			t.Fatalf("no %q in %q", want, all.String())
		}
	}
}

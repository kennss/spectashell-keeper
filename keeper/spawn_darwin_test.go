// spawn_darwin_test.go
// Author: Kennt Kim
// Created: 2026-11-07 · Last updated: 2026-11-07
//
// Purpose: a keeper started on a Mac answers for itself to the system's privacy checks, in a
// session of its own, with the stdin and stdout it was given (spawn_darwin.go).
//
// Notes:
// - Without the disclaim, the child answers to whatever this test runs under (Terminal, sshd), and
//   a shell it holds loses ~/Desktop when that ends (2026-11-07).

//go:build darwin && cgo && !ios

package keeper

import (
	"bufio"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestSpawnedKeeperAnswersForItself(t *testing.T) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	// It echoes its line and waits for its stdin to end, so it is alive while it is looked at.
	proc, err := spawn([]string{"sh", "-c", `read line; echo "$line"; cat >/dev/null`}, stdinR, stdoutW, devNull)
	stdinR.Close()
	stdoutW.Close()
	devNull.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdinW.Close()
		_, _ = proc.Wait()
	}()

	if _, err := stdinW.WriteString("hello\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdoutR).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "hello" {
		t.Fatalf("the child said %q (%v), want hello", line, err)
	}
	if got := responsibleFor(proc.Pid); got != proc.Pid {
		t.Errorf("the child (%d) answers to %d, want itself; this test runs under %d", proc.Pid, got,
			responsibleFor(os.Getpid()))
	}
	if sid, err := syscall.Getsid(proc.Pid); err != nil || sid != proc.Pid {
		t.Errorf("the child's session is %d (%v), want its own, %d", sid, err, proc.Pid)
	}
}

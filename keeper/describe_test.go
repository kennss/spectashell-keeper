// describe_test.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-30
//
// Purpose: what sdTerm grew the keeper by (Docs/KEEPER.md): a client taken over is told so
// (ErrTakenOver) instead of seeing its connection simply end; List reads each keeper's
// description without connecting — the client holding it keeps it — and drops descriptions whose
// keeper is gone, and says who holds the keeper, whether a client is attached and how the
// program ended; a connection is any stream (NewConn), as a relay over SSH is; and the folder of
// keepers must be the user's own (EnsurePrivateDir); a keeper whose folder was removed makes its
// socket and description again, and its shell is reached as before; a keeper describes its screen's
// last lines, as its emulator draws them, for a device that lists it without connecting, and what
// its program says it is doing (OSC 7501), ended once the shell is back in front; and who started a
// keeper is kept as its Config gave it.

//go:build unix

package keeper

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestATakenOverClientIsToldSo(t *testing.T) {
	dir := shortDir(t)
	first := startKeeper(t, dir, "t1", []string{"/bin/sh"})
	firstEnd := make(chan error, 1)
	go func() {
		firstEnd <- first.ReadEvents(Events{Output: func([]byte) {}, ReplayEnd: func() {}, Exit: func(Exit) {}})
	}()
	second, err := Dial(SocketPath(dir, "t1"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Hangup(Who{})
	select {
	case err := <-firstEnd:
		if !errors.Is(err, ErrTakenOver) {
			t.Fatalf("the first client's connection ended with %v, want ErrTakenOver", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first client was never told")
	}
	// The program still runs, for the second client.
	r := record(second)
	_ = second.Input([]byte("echo still-here\n"))
	r.wait(t, "the second client's echo", func(out string, _ int, _ *Exit) bool {
		return strings.Contains(out, "still-here")
	})
}

func TestListReadsDescriptionsWithoutTakingOver(t *testing.T) {
	dir := shortDir(t)
	c := startKeeper(t, dir, "d1", []string{"/bin/sh"})
	r := record(c)
	r.wait(t, "the replay", func(_ string, replayEnd int, _ *Exit) bool { return replayEnd >= 0 })
	if err := c.SetMeta(Meta{Owner: "test-owner", Persist: true, Title: "build", Directory: dir,
		Client: "Test iPad"}); err != nil {
		t.Fatal(err)
	}
	var found []Description
	waitFor(t, "the description with the new meta", func() bool {
		found = List(dir)
		return len(found) == 1 && found[0].Meta.Title == "build"
	})
	d := found[0]
	if d.KeeperID != "d1" || d.Version != Version || d.PID <= 0 || d.KeeperPID <= 0 || d.Exited || d.Meta.Directory != dir ||
		!d.Attached || d.Meta.Client != "Test iPad" || d.Exit != nil {
		t.Fatalf("description = %+v", d)
	}
	info, err := os.Stat(DescriptionPath(SocketPath(dir, "d1")))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("description mode = %o, want 600", info.Mode().Perm())
	}
	// Listing did not take the keeper over: the client still types into its shell.
	_ = c.Input([]byte("echo listed-and-kept\n"))
	r.wait(t, "the echo after listing", func(out string, _ int, _ *Exit) bool {
		return strings.Contains(out, "listed-and-kept")
	})
	// The program's end is described, with its code.
	_ = c.Input([]byte("exit 3\n"))
	r.wait(t, "the exit", func(_ string, _ int, exit *Exit) bool { return exit != nil })
	waitFor(t, "the exited description", func() bool {
		found = List(dir)
		return len(found) == 1 && found[0].Exited && found[0].Exit != nil && found[0].Exit.Code == 3
	})
	// A client that leaves is no longer attached; the device it named stays, as the last to hold it.
	c.Close()
	waitFor(t, "the detached description", func() bool {
		found = List(dir)
		return len(found) == 1 && !found[0].Attached && found[0].Meta.Client == "Test iPad"
	})
	// The keeper's end removes the description.
	again, err := Dial(SocketPath(dir, "d1"))
	if err != nil {
		t.Fatal(err)
	}
	again.Hangup(Who{})
	waitFor(t, "the description to go", func() bool { return len(List(dir)) == 0 })
}

func TestAKeeperWhoseFolderWasRemovedIsReachedAgain(t *testing.T) {
	// The folder as an app cleaner removes it: the keepers' folder and the one above it.
	base := filepath.Join(shortDir(t), "app")
	dir := filepath.Join(base, "keepers")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	c := startKeeper(t, dir, "r1", []string{"/bin/sh"})
	r := record(c)
	r.wait(t, "the replay", func(_ string, replayEnd int, _ *Exit) bool { return replayEnd >= 0 })
	_ = c.Input([]byte("echo before-the-clean\n"))
	r.wait(t, "the first echo", func(out string, _ int, _ *Exit) bool { return strings.Contains(out, "before-the-clean") })
	found := List(dir)
	if len(found) != 1 {
		t.Fatalf("List = %+v", found)
	}
	pid := found[0].KeeperPID
	c.Close()
	// Its description says so before the folder goes, so no write of it races the removal.
	waitFor(t, "the detached description", func() bool { found = List(dir); return len(found) == 1 && !found[0].Attached })
	if err := os.RemoveAll(base); err != nil {
		t.Fatal(err)
	}
	// Signalled rather than waited for: SIGUSR1 has it look at once, as every reachableEvery.
	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the description made again", func() bool {
		found = List(dir)
		return len(found) == 1 && found[0].KeeperID == "r1" && found[0].KeeperPID == pid
	})
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the folder made again: %v %v", info, err)
	}
	again, err := Dial(SocketPath(dir, "r1"))
	if err != nil {
		t.Fatalf("the socket made again: %v", err)
	}
	r = record(again)
	r.wait(t, "the replay of what was kept", func(out string, replayEnd int, _ *Exit) bool {
		return replayEnd >= 0 && strings.Contains(out, "before-the-clean")
	})
	_ = again.Input([]byte("echo after-the-clean\n"))
	r.wait(t, "the shell answering", func(out string, _ int, _ *Exit) bool { return strings.Contains(out, "after-the-clean") })
	// A second look finds both in place and leaves them be: the same socket answers.
	_ = syscall.Kill(pid, syscall.SIGUSR1)
	_ = again.Input([]byte("echo still-here\n"))
	r.wait(t, "the shell after a second look", func(out string, _ int, _ *Exit) bool { return strings.Contains(out, "still-here") })
	again.Hangup(Who{})
	waitFor(t, "the keeper to end", func() bool { return len(List(dir)) == 0 })
	if _, err := os.Stat(SocketPath(dir, "r1")); !os.IsNotExist(err) {
		t.Fatalf("the socket was left behind (%v)", err)
	}
}

func TestADescriptionCarriesTheScreensLastLines(t *testing.T) {
	dir := shortDir(t)
	c := startKeeper(t, dir, "p1", []string{"/bin/sh"})
	r := record(c)
	r.wait(t, "the replay", func(_ string, replayEnd int, _ *Exit) bool { return replayEnd >= 0 })
	// Six lines, the screen cleared first: the last four, drawn — the escapes and the carriage return acted on,
	// not shown — with the trailing spaces and the blank lines below them left out.
	_ = c.Input([]byte("clear; printf 'one\\ntwo\\nthree\\nfour   \\nfive\\n\\033[1msix\\033[0m\\rSIX\\n'; sleep 30\n"))
	var found []Description
	waitFor(t, "the preview", func() bool {
		found = List(dir)
		return len(found) == 1 && len(found[0].Preview) == 4 && found[0].Preview[3] == "SIX"
	})
	if got := found[0].Preview; !slices.Equal(got, []string{"three", "four", "five", "SIX"}) {
		t.Fatalf("preview = %q", got)
	}
	c.Hangup(Who{})
	waitFor(t, "the keeper to end", func() bool { return len(List(dir)) == 0 })
}

// A program drawing its whole screen (the alternate screen) is said to, with the title it set: its last lines are a
// piece of its interface, which a device shows by that title instead.
func TestADescriptionSaysAFullScreenProgramAndItsTitle(t *testing.T) {
	dir := shortDir(t)
	c := startKeeper(t, dir, "f1", []string{"/bin/sh"})
	r := record(c)
	r.wait(t, "the replay", func(_ string, replayEnd int, _ *Exit) bool { return replayEnd >= 0 })
	_ = c.Input([]byte("printf '\\033]2;Review the panes\\007\\033[?1049h+--+\\n| > |\\n'; sleep 30\n"))
	var found []Description
	waitFor(t, "the full screen", func() bool {
		found = List(dir)
		return len(found) == 1 && found[0].FullScreen && found[0].ScreenTitle == "Review the panes"
	})
	c.Hangup(Who{})
	waitFor(t, "the keeper to end", func() bool { return len(List(dir)) == 0 })
}

// A program that says it waits for the user (OSC 7501 blocked) is said to, with why, so a device that has not opened
// the shell marks it waiting; a program that says it is done is said to, and the blocked record is gone.
func TestADescriptionSaysTheProgramWaitsForTheUser(t *testing.T) {
	dir := shortDir(t)
	c := startKeeper(t, dir, "w1", []string{"/bin/sh"})
	r := record(c)
	r.wait(t, "the replay", func(_ string, replayEnd int, _ *Exit) bool { return replayEnd >= 0 })
	_ = c.Input([]byte("printf '\\033]7501;state=blocked:id=main:kind=permission\\007'; sleep 30\n"))
	var found []Description
	waitFor(t, "the status blocked", func() bool {
		found = List(dir)
		return len(found) == 1 && found[0].Status == "blocked"
	})
	if found[0].StatusKind != "permission" {
		t.Fatalf("status kind = %q", found[0].StatusKind)
	}
	c.Hangup(Who{})
	waitFor(t, "the keeper to end", func() bool { return len(List(dir)) == 0 })
}

// With no client connected, a blocked record ends once the shell is back in front: the program that set it ended
// without clearing it, and no prompt mark came (a shell without shell integration).
func TestAWaitingStatusEndsWhenTheShellIsBackInFront(t *testing.T) {
	dir := shortDir(t)
	c := startKeeper(t, dir, "w2", []string{"/bin/sh"})
	r := record(c)
	r.wait(t, "the replay", func(_ string, replayEnd int, _ *Exit) bool { return replayEnd >= 0 })
	// A program in front of the shell says it waits, and ends without saying more once it is answered — after the
	// client has gone.
	_ = c.Input([]byte("sh -c \"printf '\\033]7501;state=blocked:id=main:kind=question\\007'; read answer\"\n"))
	waitFor(t, "the status blocked", func() bool {
		found := List(dir)
		return len(found) == 1 && found[0].Status == "blocked"
	})
	_ = c.Input([]byte("\n"))
	c.Close()
	waitFor(t, "the status to end", func() bool {
		found := List(dir)
		return len(found) == 1 && found[0].Status == "" && !found[0].Attached
	})
	again, err := Dial(SocketPath(dir, "w2"))
	if err != nil {
		t.Fatal(err)
	}
	again.Hangup(Who{})
	waitFor(t, "the keeper to end", func() bool { return len(List(dir)) == 0 })
}

// A keeper hung up while its program runs leaves no description: the program's end, which the hangup brings, wrote it
// back after the keeper removed it (2026-10-25: six keepers left six descriptions).
func TestAHungUpKeeperLeavesNoDescription(t *testing.T) {
	for i := range 10 {
		dir := shortDir(t)
		c := startKeeper(t, dir, "h1", []string{"/bin/sh"})
		r := record(c)
		r.wait(t, "the replay", func(_ string, replayEnd int, _ *Exit) bool { return replayEnd >= 0 })
		pid := List(dir)[0].KeeperPID
		c.Hangup(Who{})
		waitFor(t, "the keeper to end", func() bool { return syscall.Kill(pid, 0) != nil })
		if _, err := os.Stat(DescriptionPath(SocketPath(dir, "h1"))); err == nil {
			t.Fatalf("run %d: the description outlived its keeper", i)
		}
	}
}

func TestListDropsDescriptionsOfKeepersThatAreGone(t *testing.T) {
	dir := shortDir(t)
	// A keeper killed outright leaves its socket and description behind.
	stale := filepath.Join(dir, "gone.json")
	if err := os.WriteFile(stale, []byte(`{"version":1,"keeper_id":"gone","keeper_pid":999999999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gone.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if found := List(dir); len(found) != 0 {
		t.Fatalf("List = %+v, want nothing", found)
	}
	for _, name := range []string{"gone.json", "gone.sock"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was left behind (%v)", name, err)
		}
	}
}

// onlyStream hides everything a socket is beyond a byte stream, as a relay over SSH would be.
type onlyStream struct{ io.ReadWriteCloser }

func TestAConnectionIsAnyStream(t *testing.T) {
	dir := shortDir(t)
	first := startKeeper(t, dir, "s1", []string{"/bin/sh"})
	first.Close()
	raw, err := net.Dial("unix", SocketPath(dir, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewConn(onlyStream{raw})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Hangup(Who{})
	if c.Hello.KeeperID != "s1" {
		t.Fatalf("Hello = %+v", c.Hello)
	}
	r := record(c)
	_ = c.Input([]byte("echo over-a-stream\n"))
	r.wait(t, "the echo over a stream", func(out string, _ int, _ *Exit) bool {
		return strings.Contains(out, "over-a-stream")
	})
}

func TestAStreamWithNoHelloIsClosed(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	start := time.Now()
	if _, err := NewConn(a); err == nil {
		t.Fatal("NewConn succeeded without a Hello")
	}
	if waited := time.Since(start); waited < helloTimeout {
		t.Fatalf("gave up after %v, before helloTimeout", waited)
	}
}

func TestTheKeepersFolderMustBeTheUsersOwn(t *testing.T) {
	dir := shortDir(t)
	open := filepath.Join(dir, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(open); err == nil {
		t.Fatal("a folder others can read was accepted")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(link); err == nil {
		t.Fatal("a symbolic link was accepted")
	}
	mine := filepath.Join(dir, "mine")
	if err := EnsurePrivateDir(mine); err != nil {
		t.Fatalf("a new private folder was refused: %v", err)
	}
	t.Setenv("SDTERM_TEST_KEEPERS", mine)
	got, err := Place{TempName: "unused", AppSupportName: "unused", Override: "SDTERM_TEST_KEEPERS"}.Dir()
	if err != nil || got != mine {
		t.Fatalf("Place.Dir with the override = %q, %v", got, err)
	}
}

// waitFor polls cond until it holds; what fails the test after 5 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// What runs in the shell reaches the client: the shell at its prompt, then the program the user
// started in front, with its process id.
func TestTheClientHearsWhatRunsInTheShell(t *testing.T) {
	dir := shortDir(t)
	c := startKeeper(t, dir, "i1", []string{"/bin/sh"})
	defer c.Hangup(Who{})
	infos := make(chan Info, 64)
	go func() {
		_ = c.ReadEvents(Events{Output: func([]byte) {}, ReplayEnd: func() {}, Exit: func(Exit) {},
			Info: func(i Info) { infos <- i }})
	}()
	waitInfo := func(what string, ok func(Info) bool) Info {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case i := <-infos:
				if ok(i) {
					return i
				}
			case <-deadline:
				t.Fatalf("no Info with %s", what)
			}
		}
	}
	shell := waitInfo("the shell in front", func(i Info) bool { return i.Program == "sh" })
	_ = c.Input([]byte("sleep 30\n"))
	sleep := waitInfo("sleep in front", func(i Info) bool { return i.Program == "sleep 30" })
	if sleep.ProgramPID == 0 || sleep.ProgramPID == shell.ProgramPID {
		t.Fatalf("sleep's pid %d, the shell's %d", sleep.ProgramPID, shell.ProgramPID)
	}
}

// Who started a keeper is described as its Config gave it, whatever a client's Meta says later: a
// client of an earlier build sends Meta without it.
func TestWhoStartedAKeeperIsKeptAsGiven(t *testing.T) {
	dir := shortDir(t)
	t.Setenv("SDTERM_KEEPER_HELPER", "1")
	starter := &Who{Client: "Sam's MacBook Pro", ClientID: "mac-1"}
	c, err := Start([]string{os.Args[0]}, Config{
		Socket: SocketPath(dir, "s1"), KeeperID: "s1", Path: "/bin/sh", Argv: []string{"/bin/sh"}, Dir: dir,
		Env: []string{"PATH=/usr/bin:/bin", "TERM=xterm-256color"}, Columns: 80, Rows: 24,
		Meta: Meta{Owner: "test-owner", Persist: true, StartedBy: starter},
	}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hangUpLeftover(SocketPath(dir, "s1")) })
	defer c.Hangup(Who{})
	if err := c.SetMeta(Meta{Owner: "test-owner", Persist: true, Title: "after", Client: "iPad",
		StartedBy: &Who{Client: "iPad", ClientID: "ipad-1"}}); err != nil {
		t.Fatal(err)
	}
	var found []Description
	waitFor(t, "the description with the new title", func() bool {
		found = List(dir)
		return len(found) == 1 && found[0].Meta.Title == "after"
	})
	if got := found[0].Meta.StartedBy; got == nil || *got != *starter {
		t.Fatalf("started_by = %+v, want %+v", got, starter)
	}
}

// keeper_test.go
// Author: Kennt Kim
// Created: 2026-10-14 · Last updated: 2026-11-02
//
// Purpose: keepers as real processes (this test binary re-run as `keep`): input and output, a
// session detaching and another taking over with the kept output replayed and the remembered
// meta, a program ending while no session is connected (a look by Discover does not end its
// keeper), Hangup, a replay of output that was cut starting at a clean place, and the modes set in
// output that was cut put back before the replay; Stop for a web app's server — SIGTERM, then
// SIGKILL for a program that ignores it — with its Exit before the keeper ends, the server's port
// and token kept in the meta, and what the program left in its process group stopped after it died.
// A test's keepers end with it, even when it stops before its own Hangup (startKeeper).
//
// Notes:
// - Taken from SiliconDesk's host/internal/keeper/keeper_test.go on 2026-10-23.

//go:build unix

package keeper

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("SPECTASHELL_KEEPER_HELPER") == "1" {
		if path := os.Getenv("SPECTASHELL_KEEPER_NOTICES"); path != "" {
			os.Exit(MainObserved(func(v View) Observer { return &fileObserver{path: path, view: v} }))
		}
		os.Exit(Main())
	}
	os.Exit(m.Run())
}

// shortDir is a private directory with a short path (Unix socket paths are limited to 104 bytes on macOS).
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sdk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func startKeeper(t *testing.T, dir, id string, argv []string) *Conn {
	t.Helper()
	t.Setenv("SPECTASHELL_KEEPER_HELPER", "1")
	c, err := Start([]string{os.Args[0]}, Config{
		Socket: SocketPath(dir, id), KeeperID: id, Path: argv[0], Argv: argv, Dir: dir,
		Env: []string{"PATH=/usr/bin:/bin", "TERM=xterm-256color"}, Columns: 80, Rows: 24,
		Meta: Meta{Owner: "test-owner", Persist: true},
	}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hangUpLeftover(SocketPath(dir, id)) })
	return c
}

// hangUpLeftover ends the keeper at socket if it still answers: a test that failed before its own
// Hangup left it running with its shell, for good (2026-10-26: six keepers of tests that failed
// the day before). It runs before shortDir's cleanup removes the socket's folder.
func hangUpLeftover(socket string) {
	if c, err := Dial(socket); err == nil {
		c.Hangup(Who{})
	}
}

// A test that stops before its Hangup — failed, or skipped as here — leaves no keeper running.
func TestAKeeperDoesNotOutliveItsTest(t *testing.T) {
	dir := shortDir(t)
	var pid int
	t.Run("stopped early", func(t *testing.T) {
		startKeeper(t, dir, "s1", []string{"/bin/sh"})
		waitFor(t, "its description", func() bool { return len(List(dir)) == 1 })
		pid = List(dir)[0].KeeperPID
		t.SkipNow()
	})
	waitFor(t, "the keeper to end", func() bool { return !alive(pid) })
}

// recorder collects what a connection delivers.
type recorder struct {
	mu        sync.Mutex
	output    strings.Builder
	replayEnd int // output length when ReplayEnd arrived, -1 before
	exit      *Exit
	done      chan struct{}
}

func record(c *Conn) *recorder {
	r := &recorder{replayEnd: -1, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		_ = c.ReadEvents(Events{
			Output: func(b []byte) { r.mu.Lock(); r.output.Write(b); r.mu.Unlock() },
			ReplayEnd: func() {
				r.mu.Lock()
				r.replayEnd = r.output.Len()
				r.mu.Unlock()
			},
			Exit: func(e Exit) { r.mu.Lock(); r.exit = &e; r.mu.Unlock() },
		})
	}()
	return r
}

func (r *recorder) wait(t *testing.T, what string, ok func(out string, replayEnd int, exit *Exit) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		done := ok(r.output.String(), r.replayEnd, r.exit)
		out := r.output.String()
		r.mu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; output %q", what, out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDetachTakeOverAndReplay(t *testing.T) {
	dir := shortDir(t)
	c := startKeeper(t, dir, "a1", []string{"/bin/sh", "-c", "echo started; read x; echo got:$x; sleep 30"})
	if c.Hello.Version != Version || c.Hello.KeeperID != "a1" || c.Hello.Exited || c.Hello.Meta.Owner != "test-owner" ||
		c.Hello.Push {
		t.Fatalf("hello %+v", c.Hello)
	}
	r := record(c)
	r.wait(t, "started", func(out string, _ int, _ *Exit) bool { return strings.Contains(out, "started") })
	if err := c.Input([]byte("abc\n")); err != nil {
		t.Fatal(err)
	}
	r.wait(t, "the answer", func(out string, _ int, _ *Exit) bool { return strings.Contains(out, "got:abc") })
	if err := c.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	if err := c.SetMeta(Meta{Owner: "test-owner", Persist: true, Title: "sh", Directory: dir}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	c.Close() // detach: the session ended

	// The next session: everything said so far comes back before ReplayEnd.
	c2, err := Dial(SocketPath(dir, "a1"))
	if err != nil {
		t.Fatal(err)
	}
	if c2.Hello.Columns != 100 || c2.Hello.Rows != 30 || c2.Hello.Meta.Title != "sh" || c2.Hello.Meta.Directory != dir {
		t.Fatalf("hello after detach %+v", c2.Hello)
	}
	r2 := record(c2)
	r2.wait(t, "the replay", func(out string, end int, _ *Exit) bool {
		return end >= 0 && strings.Contains(out[:end], "started") && strings.Contains(out[:end], "got:abc")
	})

	// A third connection takes over from the second.
	c3, err := Dial(SocketPath(dir, "a1"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-r2.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the second session was not dropped")
	}
	pid := c3.Hello.PID
	c3.Hangup(Who{})
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, statErr := os.Stat(SocketPath(dir, "a1"))
		if os.IsNotExist(statErr) && syscall.Kill(pid, 0) != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after Hangup: socket %v, program alive %v", statErr, syscall.Kill(pid, 0) == nil)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAProgramThatEndsWhileDetached(t *testing.T) {
	dir := shortDir(t)
	c := startKeeper(t, dir, "b2", []string{"/bin/sh", "-c", "sleep 0.3; echo bye; exit 3"})
	c.Close()
	time.Sleep(800 * time.Millisecond)

	// Looking does not end it; the exit waits for a session.
	for i := 0; i < 2; i++ {
		found := Discover(dir)
		if len(found) != 1 || found[0].KeeperID != "b2" || !found[0].Exited {
			t.Fatalf("discover %d: %+v", i, found)
		}
	}
	c2, err := Dial(SocketPath(dir, "b2"))
	if err != nil {
		t.Fatal(err)
	}
	r := record(c2)
	r.wait(t, "the replay and the exit", func(out string, end int, exit *Exit) bool {
		return end >= 0 && strings.Contains(out[:end], "bye") && exit != nil && exit.Code == 3
	})
	c2.Hangup(Who{})

	// A socket nobody answers on is cleaned up by Discover.
	stale := filepath.Join(dir, "dead.sock")
	os.WriteFile(stale, nil, 0o600)
	time.Sleep(200 * time.Millisecond)
	if found := Discover(dir); len(found) != 0 {
		t.Fatalf("after hangup %+v", found)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("a stale socket was left")
	}
}

func TestCleanStart(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"2;124;199;157m 0.1\x1b[0mtext", "\x1b[0mtext"}, // the tail of an SGR sequence
		{"tail of a line\nnext\x1b[1m", "next\x1b[1m"},   // a newline comes first
		{"\x1b[38;2;1;2;3mok", "\x1b[38;2;1;2;3mok"},     // already clean
		{"\x9c\xa0rest", "rest"},                         // UTF-8 continuation bytes, nothing else
		{"", ""},
	} {
		if got := string(CleanStart([]byte(c.in))); got != c.want {
			t.Errorf("CleanStart(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestAReplayThatWasCutStartsClean: a program that wrote more than the keeper keeps, in truecolor
// escape sequences, is replayed from a clean place, not from the middle of a sequence.
func TestAReplayThatWasCutStartsClean(t *testing.T) {
	dir := shortDir(t)
	// 70,000 lines of "\x1b[38;2;124;199;157m<n>" (about 1.6 MiB), so the 1 MiB cut lands inside one.
	script := `i=0; while [ $i -lt 70000 ]; do printf '\033[38;2;124;199;157m%07d ' $i; i=$((i+1)); done; echo; echo done; sleep 30`
	c := startKeeper(t, dir, "c3", []string{"/bin/sh", "-c", script})
	r := record(c)
	r.wait(t, "all the output", func(out string, _ int, _ *Exit) bool { return strings.Contains(out, "done") })
	c.Close()

	c2, err := Dial(SocketPath(dir, "c3"))
	if err != nil {
		t.Fatal(err)
	}
	r2 := record(c2)
	r2.wait(t, "the replay", func(out string, end int, _ *Exit) bool { return end >= 0 })
	r2.mu.Lock()
	replay := r2.output.String()[:r2.replayEnd]
	r2.mu.Unlock()
	if len(replay) > keptBytes || len(replay) < keptBytes-64 {
		t.Fatalf("replay of %d bytes", len(replay))
	}
	if replay[0] != 0x1b {
		t.Fatalf("the replay starts with %q", replay[:min(20, len(replay))])
	}
	c2.Hangup(Who{})
}

// TestModesSetBeforeTheCutAreReplayed: a program that set modes and then wrote more than the keeper
// keeps (twice over, so the kept output is also trimmed) is replayed with those modes first.
func TestModesSetBeforeTheCutAreReplayed(t *testing.T) {
	dir := shortDir(t)
	script := `printf '\033[?2004h\033[>1u\033]2;kept title\007'; ` +
		`i=0; while [ $i -lt 150000 ]; do printf '\033[1m%07d\033[0m ' $i; i=$((i+1)); done; echo; echo done; sleep 30`
	c := startKeeper(t, dir, "c4", []string{"/bin/sh", "-c", script})
	r := record(c)
	r.wait(t, "all the output", func(out string, _ int, _ *Exit) bool { return strings.Contains(out, "done") })
	c.Close()

	for round := 0; round < 2; round++ { // a second replay starts where the first did
		c2, err := Dial(SocketPath(dir, "c4"))
		if err != nil {
			t.Fatal(err)
		}
		r2 := record(c2)
		r2.wait(t, "the replay", func(out string, end int, _ *Exit) bool { return end >= 0 })
		r2.mu.Lock()
		replay := r2.output.String()[:r2.replayEnd]
		r2.mu.Unlock()
		want := "\x1b[?2004h\x1b[>1u\x1b]2;kept title\x07\x1b["
		if !strings.HasPrefix(replay, want) {
			t.Fatalf("round %d: the replay starts with %q", round, replay[:min(60, len(replay))])
		}
		if round == 1 {
			c2.Hangup(Who{})
		} else {
			c2.Close()
		}
	}
}

func TestStopEndsTheProgramGentlyThenForcibly(t *testing.T) {
	dir := shortDir(t)
	// A server that saves its work on SIGTERM.
	c := startKeeper(t, dir, "s1", []string{"/bin/sh", "-c", "trap 'echo saved; exit 0' TERM; echo up; while :; do sleep 0.1; done"})
	if err := c.SetMeta(Meta{Owner: "org.jupyter.lab", Persist: true, Server: &Server{Port: 41234, Token: "t0k"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	c.Close()
	c, err := Dial(SocketPath(dir, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	if srv := c.Hello.Meta.Server; srv == nil || srv.Port != 41234 || srv.Token != "t0k" {
		t.Fatalf("server meta %+v", c.Hello.Meta)
	}
	r := record(c)
	r.wait(t, "up", func(out string, _ int, _ *Exit) bool { return strings.Contains(out, "up") })
	start := time.Now()
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	r.wait(t, "the saved exit", func(out string, _ int, exit *Exit) bool {
		return strings.Contains(out, "saved") && exit != nil && exit.Code == 0
	})
	if time.Since(start) > 3*time.Second {
		t.Fatalf("a program that ends on SIGTERM took %v", time.Since(start))
	}
	waitGone(t, dir, "s1")

	// One that ignores SIGTERM gets SIGKILL after the grace period.
	c = startKeeper(t, dir, "s2", []string{"/bin/sh", "-c", "trap '' TERM; echo up; while :; do sleep 0.1; done"})
	r = record(c)
	r.wait(t, "up", func(out string, _ int, _ *Exit) bool { return strings.Contains(out, "up") })
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(stopGrace + 5*time.Second)
	for {
		r.mu.Lock()
		exit := r.exit
		r.mu.Unlock()
		if exit != nil {
			if exit.Signal != "SIGKILL" {
				t.Fatalf("exit %+v, want SIGKILL", exit)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a program that ignores SIGTERM was not killed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	waitGone(t, dir, "s2")
}

// waitGone waits until the keeper's socket is gone (the keeper ended).
func waitGone(t *testing.T, dir, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(SocketPath(dir, id)); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("keeper %s did not end", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStopEndsWhatTheProgramLeftInItsGroup(t *testing.T) {
	dir := shortDir(t)
	childPid := filepath.Join(dir, "child.pid")
	// A server whose worker outlives it: the program starts a child in its group that, like MLflow's
	// uvicorn workers, survives the SIGHUP its death sends, and is then killed.
	c := startKeeper(t, dir, "s3", []string{"/bin/sh", "-c", "trap '' HUP; sleep 300 & echo $! > " + childPid + "; echo up; wait"})
	r := record(c)
	r.wait(t, "up", func(out string, _ int, _ *Exit) bool { return strings.Contains(out, "up") })
	_ = syscall.Kill(c.Hello.PID, syscall.SIGKILL)
	r.wait(t, "the program's end", func(_ string, _ int, exit *Exit) bool { return exit != nil })
	b, err := os.ReadFile(childPid)
	if err != nil {
		t.Fatal(err)
	}
	child, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if syscall.Kill(child, 0) != nil {
		t.Fatal("the child did not outlive the program; the test proves nothing")
	}
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	waitGone(t, dir, "s3")
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(child, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the program's child outlived Stop")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

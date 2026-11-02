// termhost_test.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-11-02
//
// Purpose: a terminal on a real keeper (this test binary re-run as `keep`): its frames show the
// program's output and the replay after a reattach; a terminal whose keeper another client took
// over is lost, not ended, and its program still runs; pacing holds at two frames the views have
// not applied and goes on at each ack; a replay is drawn at the sizes its output was written at;
// what Prepare makes of a login shell and a command; TerminalEnv, whose extra variables win and
// which names each variable once; a terminal that joined a keeper to watch, drawn at the
// program's size, typing nothing, until it takes the keyboard (2026-10-26); and a resize reaching
// the program in the keeper, a shell's terminal size and a full-screen program's redraw
// (2026-10-27, Equalize Splits); the arrangement the holder tells its keeper, and none a watcher
// tells (2026-10-30).

//go:build unix

package termhost

import (
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/kennss/spectashell-keeper/gen/terminal/v1"
	"github.com/kennss/spectashell-keeper/keeper"
)

func TestMain(m *testing.M) {
	if os.Getenv("SDTERM_KEEPER_HELPER") == "1" {
		os.Exit(keeper.Main())
	}
	os.Exit(m.Run())
}

// shortDir is a private folder with a short path (a Unix socket's path is at most 104 bytes on macOS).
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sdt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func startKeeper(t *testing.T, dir, id string, argv []string) *keeper.Conn {
	t.Helper()
	t.Setenv("SDTERM_KEEPER_HELPER", "1")
	prog, err := Prepare(Launch{Argv: argv, Dir: dir, Env: []string{"PATH=/usr/bin:/bin", "LANG=en_US.UTF-8"},
		TermProgram: "test"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := keeper.Start([]string{os.Args[0]}, keeper.Config{Socket: keeper.SocketPath(dir, id), KeeperID: id,
		Path: prog.Path, Argv: prog.Argv, Dir: prog.Dir, Env: prog.Env, Columns: 40, Rows: 10,
		Meta: keeper.Meta{Owner: "test-owner", Persist: true}}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// A test that fails before its own Hangup does not leave the keeper running (keeper's hangUpLeftover).
	t.Cleanup(func() {
		if left, err := keeper.Dial(keeper.SocketPath(dir, id)); err == nil {
			left.Hangup(keeper.Who{})
		}
	})
	return c
}

// views stands for a terminal's views: it keeps the frames it is sent, and the screen they make.
type views struct {
	mu     sync.Mutex
	frames []*pb.TerminalFrame
	rows   map[uint32]string
	exit   *[2]any
	lost   *bool
}

func newViews() *views { return &views{rows: map[uint32]string{}} }

func (v *views) send(f *pb.TerminalFrame) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.frames = append(v.frames, f)
	if f.GetStartOver() {
		v.rows = map[uint32]string{}
	}
	for _, r := range f.GetChangedRows() {
		var b strings.Builder
		for _, run := range r.GetLine().GetRuns() {
			b.WriteString(run.GetText())
		}
		v.rows[r.GetIndex()] = b.String()
	}
}

func (v *views) screen() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var lines []string
	for i := uint32(0); i < 10; i++ {
		lines = append(lines, v.rows[i])
	}
	return strings.Join(lines, "\n")
}

func (v *views) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.frames)
}

// attach puts a terminal on conn whose frames go to v through a pacer.
func attach(conn *keeper.Conn, v *views) (*Terminal, *Pacer) { return attachAs(conn, v, false) }

// attachAs is attach for a terminal that joined to watch, when watch.
func attachAs(conn *keeper.Conn, v *views, watch bool) (*Terminal, *Pacer) {
	var p *Pacer
	var ready sync.WaitGroup
	ready.Add(1)
	t := Attach(conn, KeeperInfo(conn), Config{
		Name:  "test",
		Watch: watch,
		OnChange: func() {
			ready.Wait()
			p.Changed()
		},
		OnExit: func(code int, signal string) {
			v.mu.Lock()
			v.exit = &[2]any{code, signal}
			v.mu.Unlock()
		},
		OnLost: func(takenOver bool, _ error) {
			v.mu.Lock()
			v.lost = &takenOver
			v.mu.Unlock()
		},
	})
	p = NewPacer(t, 7, v.send)
	ready.Done()
	return t, p
}

// ackAll stands for views that apply every frame at once.
func ackAll(v *views, p *Pacer) {
	v.mu.Lock()
	n := len(v.frames)
	v.mu.Unlock()
	if n > 0 {
		p.Ack(uint64(n))
	}
}

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

func TestFramesShowTheProgramAndTheReplayAfterAReattach(t *testing.T) {
	dir := shortDir(t)
	conn := startKeeper(t, dir, "f1", []string{"/bin/sh"})
	v := newViews()
	term, p := attach(conn, v)
	_ = conn
	term.Input(&pb.TerminalInput{Kind: &pb.TerminalInput_Text{Text: "echo first-$((40+2))\n"}})
	waitFor(t, "the echo on the screen", func() bool { ackAll(v, p); return strings.Contains(v.screen(), "first-42") })
	if f := v.frames[0]; !f.GetStartOver() || f.GetTerminalId() != 7 || f.GetSeq() != 1 || f.GetColumns() != 40 {
		t.Fatalf("first frame = start_over %v, id %d, seq %d, columns %d", f.GetStartOver(), f.GetTerminalId(), f.GetSeq(), f.GetColumns())
	}
	p.Stop()
	term.Detach()

	// Attached again, the replay rebuilds the screen.
	again, err := keeper.Dial(keeper.SocketPath(dir, "f1"))
	if err != nil {
		t.Fatal(err)
	}
	v2 := newViews()
	term2, p2 := attach(again, v2)
	defer term2.Close()
	waitFor(t, "the replayed screen", func() bool { ackAll(v2, p2); return strings.Contains(v2.screen(), "first-42") })
}

func TestATerminalTakenOverIsLostNotEnded(t *testing.T) {
	dir := shortDir(t)
	conn := startKeeper(t, dir, "l1", []string{"/bin/sh"})
	v := newViews()
	_, p := attach(conn, v)
	defer p.Stop()
	other, err := keeper.Dial(keeper.SocketPath(dir, "l1"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Hangup(keeper.Who{})
	waitFor(t, "the loss", func() bool { v.mu.Lock(); defer v.mu.Unlock(); return v.lost != nil })
	v.mu.Lock()
	takenOver, exit := *v.lost, v.exit
	v.mu.Unlock()
	if !takenOver || exit != nil {
		t.Fatalf("taken over = %v, exit = %v; want a takeover and no exit", takenOver, exit)
	}
}

func TestPacingHoldsAtTwoUnappliedFrames(t *testing.T) {
	dir := shortDir(t)
	// A hundred lines take about 1.3 s (macOS's `sleep 0.005` sleeps some 12 ms), so the output goes
	// on through the hold below and ends well inside waitFor's 5 s. Four hundred took 5 s, as long
	// as the wait for the last line, which then failed when the machine was busy (2026-11-02).
	conn := startKeeper(t, dir, "p1", []string{"/bin/sh", "-c", "i=0; while [ $i -lt 100 ]; do echo line $i; i=$((i+1)); sleep 0.005; done; sleep 30"})
	v := newViews()
	term, p := attach(conn, v)
	defer term.Close()
	defer p.Stop()
	waitFor(t, "two frames", func() bool { return v.count() >= 2 })
	// No ack: the output goes on, the frames do not.
	time.Sleep(300 * time.Millisecond)
	if n := v.count(); n != 2 {
		t.Fatalf("%d frames sent without an ack, want 2", n)
	}
	p.Ack(1)
	waitFor(t, "a third frame after the ack", func() bool { return v.count() == 3 })
	waitFor(t, "the last line", func() bool { ackAll(v, p); return strings.Contains(v.screen(), "line 99") })
}

func TestPrepare(t *testing.T) {
	env := []string{"PATH=/usr/bin:/bin", "LANG=C", "TERM=dumb", "TERM_PROGRAM=Other", "SHELL=/bin/sh"}
	prog, err := Prepare(Launch{Env: env, TermProgram: "sdTerm", Extra: map[string]string{"FOO": "bar"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(prog.Argv[0], "-") || prog.Path == "" {
		t.Fatalf("a login shell runs as -<name>: path %q, argv %q", prog.Path, prog.Argv)
	}
	home, _ := os.UserHomeDir()
	if prog.Dir != home {
		t.Fatalf("dir = %q, want home %q", prog.Dir, home)
	}
	for _, want := range []string{"TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=sdTerm", "LANG=C.UTF-8", "FOO=bar"} {
		if !slices.Contains(prog.Env, want) {
			t.Fatalf("env lacks %s: %q", want, prog.Env)
		}
	}
	for _, gone := range []string{"TERM=dumb", "TERM_PROGRAM=Other", "LANG=C"} {
		if slices.Contains(prog.Env, gone) {
			t.Fatalf("env still has %s", gone)
		}
	}
	cmd, err := Prepare(Launch{Argv: []string{"sh", "-c", "true"}, Dir: "/tmp", Env: env})
	if err != nil || cmd.Path != "/bin/sh" && cmd.Path != "/usr/bin/sh" {
		t.Fatalf("command: %+v, %v", cmd, err)
	}
	if _, err := Prepare(Launch{Argv: []string{"no-such-program-anywhere"}, Env: env}); err == nil {
		t.Fatal("a program not on PATH was prepared")
	}
}

// A shell's end-of-line mark (zsh's PROMPT_SP: "%", spaces to the last column, a carriage return,
// a space over the "%", another) written at 40 columns, and the screen narrowed after: attached
// again, the replay draws the mark at 40 columns, where it erases itself, not at 20, where it would
// wrap and leave the "%" above the prompt (2026-10-23, in sdTerm on the Mac).
func TestAReplayIsDrawnAtTheSizesItWasWrittenAt(t *testing.T) {
	dir := shortDir(t)
	conn := startKeeper(t, dir, "r1", []string{"/bin/sh", "-c", `printf '%%%39s\r \r> '; read x; sleep 30`})
	v := newViews()
	term, p := attach(conn, v)
	waitFor(t, "the prompt", func() bool { ackAll(v, p); return strings.HasPrefix(v.screen(), "> ") })
	term.Resize(20, 10)
	// The echo comes after the keeper read the resize, sent before it on the same connection.
	term.Input(&pb.TerminalInput{Kind: &pb.TerminalInput_Text{Text: "typed"}})
	waitFor(t, "the echo", func() bool { ackAll(v, p); return strings.Contains(v.screen(), "typed") })
	p.Stop()
	term.Detach()

	again, err := keeper.Dial(keeper.SocketPath(dir, "r1"))
	if err != nil {
		t.Fatal(err)
	}
	v2 := newViews()
	term2, p2 := attach(again, v2)
	defer term2.Close()
	waitFor(t, "the replayed echo", func() bool { ackAll(v2, p2); return strings.Contains(v2.screen(), "typed") })
	if first := strings.Split(v2.screen(), "\n")[0]; strings.TrimRight(first, " ") != "> typed" {
		t.Fatalf("the replayed screen starts %q, want the prompt; screen:\n%s", first, v2.screen())
	}
	if term2.emu.Columns() != 20 || term2.emu.Rows() != 10 {
		t.Fatalf("after the replay the screen is %d × %d, want the terminal's 20 × 10", term2.emu.Columns(), term2.emu.Rows())
	}
}

// A resize the program answers with nothing (a shell at its prompt prints nothing on SIGWINCH)
// still reaches the views: the screen changed size, so a frame of the new size goes out without
// waiting for output. A pane split, or closed beside another, kept drawing at its old size until
// the next output (2026-10-25, in sdTerm on the Mac: "84 × 53" in a pane 170 columns wide).
func TestAResizeIsSentWithoutOutput(t *testing.T) {
	dir := shortDir(t)
	conn := startKeeper(t, dir, "z1", []string{"/bin/sh", "-c", `printf '> '; read x`})
	v := newViews()
	term, p := attach(conn, v)
	defer term.Close()
	waitFor(t, "the prompt", func() bool { ackAll(v, p); return strings.HasPrefix(v.screen(), "> ") })
	term.Resize(30, 8)
	waitFor(t, "a frame of 30 × 8", func() bool {
		ackAll(v, p)
		v.mu.Lock()
		defer v.mu.Unlock()
		last := v.frames[len(v.frames)-1]
		return last.GetColumns() == 30 && last.GetRows() == 8
	})
}

// A resize — a divider dragged, Equalize Splits — reaches the program in the keeper: its terminal
// has the new size, as `stty size` there says. The views drawing it are not enough; the program
// lays its output out by the terminal's size (a review of 2026-10-27).
func TestAShellInAKeeperIsToldItsNewSize(t *testing.T) {
	dir := shortDir(t)
	conn := startKeeper(t, dir, "r1", []string{"/bin/sh", "-c", `printf '> '; while read x; do stty size; printf '> '; done`})
	v := newViews()
	term, p := attach(conn, v)
	defer term.Close()
	waitFor(t, "the prompt", func() bool { ackAll(v, p); return strings.HasPrefix(v.screen(), "> ") })
	term.Resize(30, 8)
	waitFor(t, "a frame of 30 × 8", func() bool { ackAll(v, p); c, r := v.lastSize(); return c == 30 && r == 8 })
	term.Input(&pb.TerminalInput{Kind: &pb.TerminalInput_Text{Text: "\n"}})
	waitFor(t, "the shell's size, 8 rows of 30", func() bool { ackAll(v, p); return strings.Contains(v.screen(), "8 30") })
}

// A full-screen program — vim, htop — on the alternate screen is signalled (SIGWINCH) when its
// terminal is resized, and redraws at the new size, which the views get whole.
func TestAFullScreenProgramRedrawsAtItsNewSize(t *testing.T) {
	dir := shortDir(t)
	program := `printf '\033[?1049h\033[Hfull'; trap 'printf "\033[H\033[2J"; stty size' WINCH; while :; do sleep 0.05; done`
	conn := startKeeper(t, dir, "r2", []string{"/bin/sh", "-c", program})
	v := newViews()
	term, p := attach(conn, v)
	defer term.Close()
	waitFor(t, "the full-screen program", func() bool { ackAll(v, p); return strings.HasPrefix(v.screen(), "full") })
	term.Resize(30, 8)
	waitFor(t, "its redraw at 8 rows of 30", func() bool {
		ackAll(v, p)
		c, r := v.lastSize()
		return c == 30 && r == 8 && strings.HasPrefix(v.screen(), "8 30")
	})
}

// lastSize is the size of the last frame v was sent.
func (v *views) lastSize() (cols, rows uint32) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.frames) == 0 {
		return 0, 0
	}
	last := v.frames[len(v.frames)-1]
	return last.GetColumns(), last.GetRows()
}

// A teammate's terminal joins the shell: it draws at the shell's size, follows it, and types
// nothing, until it takes the keyboard; then the shell takes its size, and the one that held it
// watches.
func TestAWatcherDrawsAtTheProgramsSizeUntilItTakesTheKeyboard(t *testing.T) {
	dir := shortDir(t)
	conn := startKeeper(t, dir, "w1", []string{"/bin/sh"})
	va := newViews()
	a, pa := attach(conn, va)
	defer a.Close()
	defer pa.Stop()

	joined, err := keeper.DialJoin(keeper.SocketPath(dir, "w1"))
	if err != nil {
		t.Fatal(err)
	}
	vb := newViews()
	b, pb2 := attachAs(joined, vb, true)
	defer b.Detach()
	defer pb2.Stop()
	b.Resize(30, 8) // the watcher's views: told to the keeper, not drawn at
	a.Input(&pb.TerminalInput{Kind: &pb.TerminalInput_Text{Text: "echo held-$((1+1))\n"}})
	waitFor(t, "the holder's command, watched", func() bool { ackAll(vb, pb2); return strings.Contains(vb.screen(), "held-2") })
	if cols, rows := vb.lastSize(); cols != 40 || rows != 10 || !b.Watching() {
		t.Fatalf("the watcher draws at %d × %d (watching %v), want the shell's 40 × 10", cols, rows, b.Watching())
	}

	// The watcher's typing goes nowhere.
	b.Input(&pb.TerminalInput{Kind: &pb.TerminalInput_Text{Text: "echo typed-$((2+3))\n"}})
	a.Input(&pb.TerminalInput{Kind: &pb.TerminalInput_Text{Text: "echo after\n"}})
	waitFor(t, "the holder's next command", func() bool { ackAll(va, pa); return strings.Contains(va.screen(), "after") })
	if strings.Contains(va.screen(), "typed-5") {
		t.Fatalf("a watcher typed into the shell:\n%s", va.screen())
	}

	// The holder's size reaches the watcher.
	a.Resize(36, 9)
	waitFor(t, "the watcher at the shell's new size", func() bool {
		ackAll(vb, pb2)
		cols, rows := vb.lastSize()
		return cols == 36 && rows == 9
	})

	// The watcher takes the keyboard: the shell takes its size, the holder watches at it.
	b.Take()
	waitFor(t, "the keyboard taken", func() bool { return !b.Watching() && a.Watching() })
	waitFor(t, "both at the taker's size", func() bool {
		ackAll(va, pa)
		ackAll(vb, pb2)
		ca, ra := va.lastSize()
		cb, rb := vb.lastSize()
		return ca == 30 && ra == 8 && cb == 30 && rb == 8
	})
	b.Input(&pb.TerminalInput{Kind: &pb.TerminalInput_Text{Text: "echo taken-$((3+4))\n"}})
	waitFor(t, "the taker's command, watched by the old holder", func() bool {
		ackAll(va, pa)
		return strings.Contains(va.screen(), "taken-7")
	})
}

func TestTheHoldersArrangementIsKeptAndAWatchersIsNot(t *testing.T) {
	dir := shortDir(t)
	conn := startKeeper(t, dir, "r1", []string{"/bin/sh"})
	va := newViews()
	a, pa := attach(conn, va)
	defer a.Close()
	defer pa.Stop()
	described := func() string {
		for _, d := range keeper.List(dir) {
			if d.KeeperID == "r1" {
				return d.Meta.Arrangement
			}
		}
		return ""
	}

	a.RememberArrangement(`{"id":"desk-1"}`)
	waitFor(t, "the holder's arrangement in the description", func() bool { return described() == `{"id":"desk-1"}` })

	joined, err := keeper.DialJoin(keeper.SocketPath(dir, "r1"))
	if err != nil {
		t.Fatal(err)
	}
	vb := newViews()
	b, pb2 := attachAs(joined, vb, true)
	defer b.Detach()
	defer pb2.Stop()
	b.RememberArrangement(`{"id":"desk-2"}`)
	// The holder's next one is written after the watcher's would have been: the description then shows
	// whether the watcher's ever was.
	a.RememberArrangement(`{"id":"desk-3"}`)
	waitFor(t, "the holder's next arrangement", func() bool { return described() == `{"id":"desk-3"}` })
	if b.meta.Arrangement == `{"id":"desk-2"}` {
		t.Fatal("a watcher kept an arrangement of its own")
	}
}

// getenv is what a C program reads: the first of two values (2026-10-23: a LANG the engine's user
// gave in Extra came after LANG=C.UTF-8 and was never read; a TERM likewise).
func getenv(env []string, key string) (string, int) {
	value, n := "", 0
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == key {
			if n == 0 {
				value = v
			}
			n++
		}
	}
	return value, n
}

func TestTerminalEnvLetsExtraWinAndNamesEachVariableOnce(t *testing.T) {
	cases := []struct {
		name  string
		env   []string
		extra map[string]string
		want  map[string]string // "" for a variable that must be absent
	}{
		{"a locale that is not UTF-8 is replaced", []string{"LANG=C", "LC_CTYPE=C"}, nil,
			map[string]string{"LANG": "C.UTF-8", "LC_CTYPE": ""}},
		{"a UTF-8 locale is kept", []string{"LANG=ko_KR.UTF-8"}, nil,
			map[string]string{"LANG": "ko_KR.UTF-8"}},
		{"extra's LANG wins with none inherited", nil, map[string]string{"LANG": "ko_KR.UTF-8"},
			map[string]string{"LANG": "ko_KR.UTF-8"}},
		{"extra's LANG wins over an inherited LC_ALL=C", []string{"LC_ALL=C", "LANG=C"},
			map[string]string{"LANG": "ja_JP.UTF-8"}, map[string]string{"LANG": "ja_JP.UTF-8", "LC_ALL": ""}},
		{"extra's own non-UTF-8 choice stands", []string{"LANG=en_US.UTF-8"}, map[string]string{"LC_ALL": "C"},
			map[string]string{"LC_ALL": "C", "LANG": ""}}, // LC_ALL decides every category; the inherited LANG goes
		{"extra's TERM wins", []string{"TERM=dumb"}, map[string]string{"TERM": "xterm-direct"},
			map[string]string{"TERM": "xterm-direct", "COLORTERM": "truecolor", "TERM_PROGRAM": "sdTerm"}},
		// Only the login session's variables are taken: nothing of another program's session — an
		// agent's, another terminal's, a multiplexer's — nor of an app's process (2026-10-27).
		{"another program's session stays out", []string{"HOME=/Users/x", "USER=x", "LOGNAME=x", "SHELL=/bin/zsh",
			"PATH=/usr/bin:/bin", "TMPDIR=/tmp/x/", "LC_ALL=en_US.UTF-8", "SSH_AUTH_SOCK=/tmp/agent",
			"XDG_RUNTIME_DIR=/run/user/1000", "__CF_USER_TEXT_ENCODING=0x1F6:0x0:0x0",
			"CLAUDE_CODE_CHILD_SESSION=1", "CLAUDECODE=1", "TERM_PROGRAM=iTerm.app", "TERM_PROGRAM_VERSION=3.5",
			"ITERM_SESSION_ID=w0", "TERM_SESSION_ID=x", "LC_TERMINAL=iTerm2", "TMUX=/tmp/t,1,0", "TMUX_PANE=%1",
			"STY=1.pts", "CMUX_WORKSPACE_ID=w", "XPC_SERVICE_NAME=0", "__CFBundleIdentifier=ai.calidalab.sdterm",
			"DYLD_FRAMEWORK_PATH=/x", "SDTERM_TEST=1", "SSH_CONNECTION=1 2 3 4"}, nil,
			map[string]string{"HOME": "/Users/x", "USER": "x", "LOGNAME": "x", "SHELL": "/bin/zsh",
				"PATH": "/usr/bin:/bin", "TMPDIR": "/tmp/x/", "LC_ALL": "en_US.UTF-8", "SSH_AUTH_SOCK": "/tmp/agent",
				"XDG_RUNTIME_DIR": "/run/user/1000", "__CF_USER_TEXT_ENCODING": "0x1F6:0x0:0x0",
				"TERM_PROGRAM": "sdTerm", "TERM_PROGRAM_VERSION": "", "CLAUDE_CODE_CHILD_SESSION": "", "CLAUDECODE": "",
				"ITERM_SESSION_ID": "", "TERM_SESSION_ID": "", "LC_TERMINAL": "", "TMUX": "", "TMUX_PANE": "",
				"STY": "", "CMUX_WORKSPACE_ID": "", "XPC_SERVICE_NAME": "", "__CFBundleIdentifier": "",
				"DYLD_FRAMEWORK_PATH": "", "SDTERM_TEST": "", "SSH_CONNECTION": ""}},
	}
	for _, c := range cases {
		env := TerminalEnv(c.env, "sdTerm", c.extra)
		for _, kv := range env {
			k, _, _ := strings.Cut(kv, "=")
			if _, n := getenv(env, k); n != 1 {
				t.Errorf("%s: %s appears %d times: %q", c.name, k, n, env)
			}
		}
		for k, want := range c.want {
			if got, _ := getenv(env, k); got != want {
				t.Errorf("%s: %s = %q, want %q (env %q)", c.name, k, got, want, env)
			}
		}
	}
}

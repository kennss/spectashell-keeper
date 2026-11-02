// prompts_test.go
// Author: Kennt Kim
// Created: 2026-10-19 · Last updated: 2026-11-02
//
// Purpose: shell integration's marks (prompts.go) as bash with SiliconDesk's script prints them: a
// prompt, the command typed (read from the screen), its output and end on the right lines, with
// exit status and duration, into the history; a cancelled line leaving nothing; a nested ssh
// shell's commands and the cascade when ssh ends; command lines from cmdline_url and OSC 633;E;
// continuation prompts; no marks on the alternate screen; an erased display taking its marks; a
// running command carried to a fresh terminal by ModeSequences with its start; an unknown time
// giving no duration; another host's OSC 7 directory ignored; each prompt carrying the directory
// reported before it, none after another host's, and that kept across a fresh terminal; a command
// found running on another host losing its prompt's directory, the innermost one of this host.
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/prompts_test.go on 2026-10-23.

package term

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

type promptFixture struct {
	*fixture
	now      time.Time
	finished []CommandFinished
}

func newPromptFixture(t *testing.T, rows int) *promptFixture {
	p := &promptFixture{fixture: &fixture{t: t}, now: time.UnixMilli(1_000_000)}
	p.term = New(Options{Columns: 40, Rows: rows, Hostname: "Box",
		CommandFinished: func(c CommandFinished) { p.finished = append(p.finished, c) },
		Now:             func() time.Time { return p.now }})
	return p
}

// run types a command at a bash prompt as the script marks it, and prints its output.
func (p *promptFixture) run(command, output string, exit int, took time.Duration) {
	p.write("\x1b]133;A;aid=42\x07$ \x1b]133;B\x07" + command + "\r\n")
	p.write("\x1b]133;C;aid=42\x07" + output)
	p.now = p.now.Add(took)
	p.write("\x1b]133;D;" + strconv.Itoa(exit) + ";aid=42\x07")
}

func (p *promptFixture) markOf(y int) Mark {
	if m := p.term.Line(y).Mark; m != nil {
		return *m
	}
	return Mark{}
}

func TestShellIntegrationMarksCommands(t *testing.T) {
	p := newPromptFixture(t, 10)
	p.run("ls -l", "a\r\nb\r\n", 0, 3*time.Second)
	p.run("false", "", 1, 25*time.Second)
	p.write("\x1b]133;A;aid=42\x07$ \x1b]133;B\x07")
	p.expectScreen("$ ls -l", "a", "b", "$ false", "$")
	if m := p.markOf(0); !m.Prompt || m.Output || m.End != nil {
		t.Fatalf("prompt line %+v", m)
	}
	if m := p.markOf(1); !m.Output || m.Command != "ls -l" || m.OutputColumn != 0 {
		t.Fatalf("output line %+v", m)
	}
	// ls ended where the next prompt begins; false had no output, so its output starts and ends there.
	if m := p.markOf(3); !m.Prompt || m.End == nil || m.End.ExitCode != 0 || !m.End.HasExitCode || m.End.Column != 0 {
		t.Fatalf("ls's end %+v", m)
	}
	if m := p.markOf(4); !m.Prompt || !m.Output || m.Command != "false" || m.End == nil || m.End.ExitCode != 1 {
		t.Fatalf("false's line %+v", m)
	}
	if len(p.finished) != 2 || p.finished[0].Command != "ls -l" || p.finished[0].Duration != 3*time.Second ||
		!p.finished[0].HasDuration || p.finished[1].ExitCode != 1 || p.finished[1].Duration != 25*time.Second {
		t.Fatalf("finished %+v", p.finished)
	}

	// Output without a final newline: the end carries its column.
	p.write("printf x\r\n\x1b]133;C;aid=42\x07x\x1b]133;D;0;aid=42\x07")
	if m := p.markOf(5); m.End == nil || m.End.Column != 1 {
		t.Fatalf("end column %+v", m)
	}

	// Marks go into the history with their lines.
	for i := 0; i < 10; i++ {
		p.write("\r\n")
	}
	if m := p.term.HistoryLine(1).Mark; m == nil || !m.Output || m.Command != "ls -l" {
		t.Fatalf("history mark %+v", m)
	}
}

func TestShellIntegrationCancelledLinesAndContinuations(t *testing.T) {
	p := newPromptFixture(t, 10)
	p.write("\x1b]133;D;0;aid=42\x07") // the first prompt's D: nothing ran
	p.write("\x1b]133;A;aid=42\x07$ \x1b]133;B\x07sleep 1^C\r\n\x1b]133;D;130;aid=42\x07")
	p.write("\x1b]133;A;aid=42\x07$ \x1b]133;B\x07for x in a\r\n")
	p.write("\x1b]133;A;k=s;aid=42\x07> \x1b]133;B\x07do echo $x; done\r\n\x1b]133;C;aid=42\x07a\r\n")
	p.write("\x1b]133;D;0;aid=42\x07\x1b]133;D;0;aid=42\x07") // only the first D ends it
	if len(p.finished) != 1 || p.finished[0].Command != "for x in a > do echo $x; done" {
		t.Fatalf("finished %+v", p.finished)
	}
	if m := p.markOf(0); !m.Prompt || m.End != nil {
		t.Fatalf("cancelled line %+v", m)
	}
	if m := p.markOf(2); m.Prompt {
		t.Fatalf("a continuation prompt marked %+v", m)
	}
}

func TestShellIntegrationNestedShells(t *testing.T) {
	p := newPromptFixture(t, 20)
	p.write("\x1b]133;A;aid=1\x07$ \x1b]133;B\x07ssh nas\r\n\x1b]133;C;aid=1\x07")
	start := p.now
	// The remote shell's own marks, with an aid of its own.
	p.write("\x1b]133;A;aid=9\x07nas$ \x1b]133;B\x07make\r\n\x1b]133;C;aid=9\x07ok\r\n")
	p.now = p.now.Add(time.Minute)
	p.write("\x1b]133;D;0;aid=9\x07\x1b]133;A;aid=9\x07nas$ \x1b]133;B\x07exit\r\n\x1b]133;C;aid=9\x07")
	// ssh ends before the remote shell can say so; the local shell's D closes both.
	p.now = p.now.Add(time.Minute)
	p.write("logout\r\n\x1b]133;D;0;aid=1\x07")
	if len(p.finished) != 2 || p.finished[0].Command != "make" || p.finished[1].Command != "ssh nas" ||
		p.finished[1].Duration != p.now.Sub(start) {
		t.Fatalf("finished %+v", p.finished)
	}
	if len(p.term.shells) != 1 || p.term.shells[0].aid != "1" {
		t.Fatalf("stack %+v", p.term.shells)
	}
}

func TestShellIntegrationCommandLines(t *testing.T) {
	p := newPromptFixture(t, 10)
	p.write("\x1b]133;A\x07% \x1b]133;B\x07git st\x1b]133;C;cmdline_url=git%20status%3B%20ls\x07\x1b]133;D;0\x07")
	p.write("\x1b]633;A\x07> \x1b]633;B\x07\x1b]633;E;echo a\\x3bb\\\\c;nonce\x07\x1b]633;C\x07\x1b]633;D;0\x07")
	if len(p.finished) != 2 || p.finished[0].Command != "git status; ls" || p.finished[1].Command != `echo a;b\c` {
		t.Fatalf("finished %+v", p.finished)
	}
}

func TestShellIntegrationScreens(t *testing.T) {
	p := newPromptFixture(t, 10)
	// A full-screen program's alternate screen holds no marks.
	p.write("\x1b[?1049h\x1b]133;A;aid=42\x07")
	if p.term.Line(0).Mark != nil {
		t.Fatal("a mark on the alternate screen")
	}
	p.write("\x1b[?1049l")
	p.run("top", "", 0, time.Second)
	// Erasing in a line keeps the mark (a progress bar); erasing the display takes it.
	p.write("\x1b[2;1H\x1b[2K\x1b[1;1H\x1b[2K")
	if p.term.Line(0).Mark == nil {
		t.Fatal("EL took the mark")
	}
	p.write("\x1b[2J")
	for y := 0; y < 10; y++ {
		if p.term.Line(y).Mark != nil {
			t.Fatalf("row %d kept its mark after ED 2", y)
		}
	}
}

func TestRunningCommandsSurviveAReplay(t *testing.T) {
	p := newPromptFixture(t, 10)
	p.write("\x1b]133;A;aid=42\x07$ \x1b]133;B\x07python train.py\r\n")
	p.write("\x1b]133;C;aid=42;spectashell_started=" + strconv.FormatInt(p.now.UnixMilli(), 10) + "\x07epoch 1\r\n")
	modes := string(p.term.ModeSequences())
	if !strings.Contains(modes, "\x1b]133;C;aid=42;cmdline_url=python%20train.py;spectashell_started=1000000\x07") {
		t.Fatalf("modes %q", modes)
	}
	// A fresh terminal, as after a session change: its clock knows nothing of the old output.
	fresh := newPromptFixture(t, 10)
	fresh.now = time.Time{}
	fresh.write(modes + "epoch 2\r\n")
	fresh.now = time.UnixMilli(1_000_000).Add(2*time.Hour + 13*time.Minute)
	fresh.write("\x1b]133;D;0;aid=42\x07")
	if len(fresh.finished) != 1 || fresh.finished[0].Command != "python train.py" ||
		fresh.finished[0].Duration != 2*time.Hour+13*time.Minute {
		t.Fatalf("finished %+v", fresh.finished)
	}
	// A command whose start nobody knows has no duration.
	fresh.now = time.Time{}
	fresh.write("\x1b]133;A;aid=42\x07$ \x1b]133;B\x07make\r\n\x1b]133;C;aid=42\x07")
	fresh.now = time.Now()
	fresh.write("\x1b]133;D;2;aid=42\x07")
	if f := fresh.finished[1]; f.HasDuration || f.ExitCode != 2 {
		t.Fatalf("unknown start %+v", f)
	}
}

// A shell whose integration SiliconDesk installed, or one started before the scripts took
// SpectaShell's name, gives its command's start as sdterm_started; it is read the same.
func TestTheEarlierStartOptionIsRead(t *testing.T) {
	p := newPromptFixture(t, 10)
	p.write("\x1b]133;A;aid=7\x07$ \x1b]133;B\x07make\r\n")
	started := p.now.UnixMilli()
	p.now = p.now.Add(10 * time.Minute) // C arrives late: the start is the shell's, not C's
	p.write("\x1b]133;C;aid=7;sdterm_started=" + strconv.FormatInt(started, 10) + "\x07")
	p.now = time.UnixMilli(started).Add(time.Hour)
	p.write("\x1b]133;D;0;aid=7\x07")
	if len(p.finished) != 1 || !p.finished[0].HasDuration || p.finished[0].Duration != time.Hour {
		t.Fatalf("finished %+v, want make taking the hour since sdterm_started", p.finished)
	}
}

func TestDirectoriesOfOtherHosts(t *testing.T) {
	p := newPromptFixture(t, 10)
	p.write("\x1b]7;file://box/home/sam/src\x07")
	p.write("\x1b]7;file://nas/volume1/media\x07")
	if got := p.term.WorkingDirectory(); got != "/home/sam/src" {
		t.Fatalf("directory %q", got)
	}
	p.write("\x1b]7;file:///tmp\x07\x1b]7;file://localhost/var\x07")
	if got := p.term.WorkingDirectory(); got != "/var" {
		t.Fatalf("directory %q", got)
	}
}

func TestPromptsCarryTheirDirectory(t *testing.T) {
	p := newPromptFixture(t, 10)
	p.write("\x1b]7;file://box/home/sam/src\x07")
	p.run("make", "src/main.c:3: error\r\n", 2, time.Second)
	p.write("\x1b]7;file:///tmp\x07")
	p.run("ls", "a\r\n", 0, time.Second)
	p.write("\x1b]7;file://nas/volume1\x07")
	p.write("\x1b]133;A;aid=42\x07nas$ \x1b]133;B\x07")
	for y, want := range map[int]string{0: "/home/sam/src", 2: "/tmp", 4: ""} {
		if m := p.markOf(y); !m.Prompt || m.Directory != want {
			t.Fatalf("line %d: %+v, want %q", y, m, want)
		}
	}
	// A fresh terminal fed ModeSequences is still on the other host.
	fresh := newPromptFixture(t, 10)
	fresh.write(string(p.term.ModeSequences()))
	fresh.write("\x1b]133;A;aid=42\x07nas$ ")
	if m := fresh.markOf(0); !m.Prompt || m.Directory != "" || fresh.term.WorkingDirectory() != "/tmp" {
		t.Fatalf("fresh %+v %q", m, fresh.term.WorkingDirectory())
	}
}

func TestACommandOnAnotherHostLosesItsDirectory(t *testing.T) {
	p := newPromptFixture(t, 10)
	p.write("\x1b]7;file://box/home/sam\x07")
	p.run("ls", "notes.txt\r\n", 0, time.Second)
	if p.term.CommandRanElsewhere() {
		t.Fatal("no command runs")
	}
	// `ssh gpu` runs; its host prints its own files.
	p.write("\x1b]133;A;aid=1\x07$ \x1b]133;B\x07ssh gpu\r\n\x1b]133;C;aid=1\x07config.yaml\r\n")
	if !p.term.CommandRanElsewhere() || p.term.CommandRanElsewhere() {
		t.Fatal("the ssh command's prompt changed not once")
	}
	if m := p.markOf(2); !m.Prompt || m.Directory != "" {
		t.Fatalf("ssh's prompt %+v", m)
	}
	if m := p.markOf(0); m.Directory != "/home/sam" {
		t.Fatalf("the command before %+v", m)
	}
	// With integration on that host its prompts have no directory already: the local one is found
	// beneath them.
	q := newPromptFixture(t, 10)
	q.write("\x1b]7;file://box/home/sam\x07")
	q.write("\x1b]133;A;aid=1\x07$ \x1b]133;B\x07ssh nas\r\n\x1b]133;C;aid=1\x07")
	q.write("\x1b]7;file://nas/volume1\x07\x1b]133;A;aid=9\x07nas$ \x1b]133;B\x07make\r\n\x1b]133;C;aid=9\x07")
	if !q.term.CommandRanElsewhere() || q.markOf(0).Directory != "" || q.markOf(1).Directory != "" {
		t.Fatalf("nested: %+v %+v", q.markOf(0), q.markOf(1))
	}
}

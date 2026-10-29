// resume_test.go
// Author: Kennt Kim
// Created: 2026-10-22 · Last updated: 2026-10-23
//
// Purpose: the command that resumes a coding agent (resume.go), through Linux's /proc reader from a
// /proc made for the test and fake agents' records — so it runs on any machine: Claude Code started
// through `ollama launch` (its record found on the child, in CLAUDE_CONFIG_DIR, the earlier
// --resume taken out) and started directly (~/.claude, or the process's own HOME; --continue and
// its prompt left out, so the prompt is not sent again), and found when the group's leader has
// exited; nothing without a record, for a record of another process, a malformed id or a --print
// run; Codex's from the thread lock it holds open, through `ollama launch` and typed through Node,
// its options kept and `resume <id>` after them, none for `codex exec`; the options kept and the
// rest left out; quoting, for zsh too; and none for ssh in front, through Read.
//
// Notes:
// - Taken from SiliconDesk's host/internal/apps/terminal/restart_test.go on 2026-10-23: its resume
//   tests. sdLLM's variables before the command are gone with RunCommand (resume.go).

package paneinfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAgents makes /proc entries: pid → stat after the pid, command line, environment.
func fakeAgents(t *testing.T, procs map[string][3]string) string {
	t.Helper()
	root := t.TempDir()
	for pid, p := range procs {
		dir := filepath.Join(root, pid)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "stat"), []byte(pid+" "+p[0]), 0o644)
		os.WriteFile(filepath.Join(dir, "cmdline"), []byte(p[1]), 0o644)
		os.WriteFile(filepath.Join(dir, "environ"), []byte(p[2]), 0o644)
	}
	return root
}

func record(t *testing.T, dir, pid, body string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "sessions"), 0o700)
	os.WriteFile(filepath.Join(dir, "sessions", pid+".json"), []byte(body), 0o600)
}

// resumeOf is the resume command for front in the session of shell 100 under root.
func resumeOf(t *testing.T, root string, front int) string {
	t.Helper()
	sys := procfs{root: root}
	procs, err := sys.session(100)
	if err != nil {
		t.Fatal(err)
	}
	return resumeCommand(sys, newSnapshot(procs), front)
}

func TestTheCommandThatResumesClaudeCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(t.TempDir(), "claude-config")
	ownHome := t.TempDir()
	const id = "063a8e1e-a86a-4148-a783-1b72c652189f"
	root := fakeAgents(t, map[string][3]string{
		// Shell 100; `ollama launch claude` in front (group 200) and its claude, with its own config dir.
		"100": {"(bash) S 50 100 100 34816 200 0", "-bash\x00", ""},
		"200": {"(ollama) S 100 200 100 34816 200 0",
			"ollama\x00launch\x00claude\x00--model\x00gemma4:12b-64k\x00--\x00--permission-mode\x00manual\x00--resume\x00old\x00", ""},
		"201": {"(claude) S 200 200 100 34816 200 0", "claude\x00--permission-mode\x00manual\x00",
			"PATH=/usr/bin\x00CLAUDE_CONFIG_DIR=" + config + "\x00"},
		// claude typed at a prompt with a prompt of its own, continuing a conversation, its record in
		// ~/.claude.
		"300": {"(claude) S 100 300 100 34816 300 0", "/home/u/.local/bin/claude\x00-c\x00--model\x00opus\x00fix it\x00", ""},
		// A program that keeps no record; a record naming another process; a malformed id; --print.
		"400": {"(python3) S 100 400 100 34816 400 0", "python3\x00train.py\x00", ""},
		"500": {"(claude) S 100 500 100 34816 500 0", "claude\x00", ""},
		"600": {"(claude) S 100 600 100 34816 600 0", "claude\x00", ""},
		"700": {"(claude) S 100 700 100 34816 700 0", "claude\x00-p\x00hi\x00", ""},
		// claude whose group's leader (800) has exited, and one with a HOME of its own.
		"801": {"(claude) S 1 800 100 34816 800 0", "claude\x00--model\x00opus\x00", ""},
		"900": {"(claude) S 100 900 100 34816 900 0", "claude\x00", "HOME=" + ownHome + "\x00"},
	})
	record(t, config, "201", `{"pid":201,"sessionId":"`+id+`","cwd":"/srv"}`)
	record(t, filepath.Join(home, ".claude"), "300", `{"pid":300,"sessionId":"`+id+`"}`)
	record(t, filepath.Join(home, ".claude"), "500", `{"pid":999,"sessionId":"`+id+`"}`)
	record(t, filepath.Join(home, ".claude"), "600", `{"pid":600,"sessionId":"x; rm -rf ~"}`)
	record(t, filepath.Join(home, ".claude"), "700", `{"pid":700,"sessionId":"`+id+`","kind":"print"}`)
	record(t, filepath.Join(home, ".claude"), "801", `{"pid":801,"sessionId":"`+id+`","kind":"interactive"}`)
	record(t, filepath.Join(ownHome, ".claude"), "900", `{"pid":900,"sessionId":"`+id+`"}`)

	if got, want := resumeOf(t, root, 200),
		"ollama launch claude --model gemma4:12b-64k -- --permission-mode manual --resume "+id; got != want {
		t.Fatalf("through ollama launch:\n got %q\nwant %q", got, want)
	}
	if got, want := resumeOf(t, root, 300), "/home/u/.local/bin/claude --model opus --resume "+id; got != want {
		t.Fatalf("typed: got %q, want %q", got, want)
	}
	if got, want := resumeOf(t, root, 801), "claude --model opus --resume "+id; got != want {
		t.Fatalf("leader gone: got %q, want %q", got, want)
	}
	if got, want := resumeOf(t, root, 900), "claude --resume "+id; got != want {
		t.Fatalf("own HOME: got %q, want %q", got, want)
	}
	for _, pid := range []int{400, 500, 600, 700, 1000} {
		if got := resumeOf(t, root, pid); got != "" {
			t.Fatalf("pid %d: %q", pid, got)
		}
	}
	// Through Read: the program in front's.
	if info, err := procReader(root).Read(100, ""); err != nil || info.ProgramPID != 200 ||
		info.Resume != "ollama launch claude --model gemma4:12b-64k -- --permission-mode manual --resume "+id {
		t.Fatalf("read %+v %v", info, err)
	}
}

func TestNoResumeForSSH(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := fakeAgents(t, map[string][3]string{
		"100": {"(bash) S 50 100 100 34816 200 0", "-bash\x00", ""},
		"200": {"(ssh) S 100 200 100 34816 200 0", "ssh\x00gpu\x00", ""},
	})
	// Even with a record for its pid: what runs in front is on another host.
	record(t, filepath.Join(home, ".claude"), "200", `{"pid":200,"sessionId":"063a8e1e-a86a-4148-a783-1b72c652189f"}`)
	if info, _ := procReader(root).Read(100, ""); info.Remote != "gpu" || info.Resume != "" {
		t.Fatalf("ssh %+v", info)
	}
}

func TestResumeArgumentsAndQuoting(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"ollama", "launch", "claude", "--model", "m"}, []string{"ollama", "launch", "claude", "--model", "m", "--"}},
		// A prompt, an earlier conversation and an option not known go; the session's options stay.
		{[]string{"claude", "fix the build", "--resume=abc", "-r", "def", "--continue", "--verbose",
			"--permission-mode", "manual", "--add-dir", "../lib", "../docs", "--model=opus", "-p"},
			[]string{"claude", "--permission-mode", "manual", "--add-dir", "../lib", "../docs", "--model=opus"}},
		{[]string{"claude", "--model", "opus", "why is it slow"}, []string{"claude", "--model", "opus"}},
	}
	for _, c := range cases {
		if got := resumeArgs(agentClaude, c.in); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Fatalf("%v: got %v, want %v", c.in, got, c.want)
		}
	}
	if got := shellJoin([]string{"claude", "--append-system-prompt", "it's a test", "a/b:c", "--model=opus", "=x", ""}); got !=
		`claude --append-system-prompt 'it'\''s a test' a/b:c --model=opus '=x' ''` {
		t.Fatalf("quoted %s", got)
	}
	if got := withoutInterpreter([]string{"/usr/local/bin/bun", "x.js", "a"}); strings.Join(got, " ") != "x.js a" {
		t.Fatalf("bun %v", got)
	}
}

// lock makes pid's open file pointing at target (a link in its fd folder, as /proc has it).
func lock(t *testing.T, root, pid, fd, target string) {
	t.Helper()
	dir := filepath.Join(root, pid, "fd")
	os.MkdirAll(dir, 0o755)
	if err := os.Symlink(target, filepath.Join(dir, fd)); err != nil {
		t.Fatal(err)
	}
}

func TestTheCommandThatResumesCodex(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no Claude Code records
	const id = "01a164e3-3c52-73f0-8835-12e397e40b19"
	root := fakeAgents(t, map[string][3]string{
		"100": {"(bash) S 50 100 100 34816 200 0", "-bash\x00", ""},
		// `ollama launch codex` in front (group 200), Codex's Node launcher and its binary.
		"200": {"(ollama) S 100 200 100 34816 200 0",
			"ollama\x00launch\x00codex\x00--model\x00gemma4:12b-64k\x00--\x00--sandbox\x00read-only\x00" +
				"--ask-for-approval\x00on-request\x00", ""},
		"201": {"(node) S 200 200 100 34816 200 0", "node\x00/home/u/.local/bin/codex\x00--profile\x00ollama-launch\x00", ""},
		"202": {"(codex) S 201 200 100 34816 200 0", "/x/codex\x00--profile\x00ollama-launch\x00", ""},
		// Typed through Node, with a prompt and an earlier resume; and `codex exec`.
		"300": {"(node) S 100 300 100 34816 300 0",
			"node\x00/home/u/.local/bin/codex\x00-m\x00gpt-6\x00-a\x00never\x00resume\x00old\x00fix it\x00", ""},
		"400": {"(node) S 100 400 100 34816 400 0", "node\x00/home/u/.local/bin/codex\x00exec\x00do it\x00", ""},
		"500": {"(codex) S 100 500 100 34816 500 0", "codex\x00", ""},
	})
	lock(t, root, "202", "57", "/home/u/.codex/sessions/2026/10/23/rollout-2026-10-23T01-32-56-"+id+".jsonl")
	lock(t, root, "202", "44", "/home/u/.codex/thread-writer-locks/"+id+".lock")
	lock(t, root, "202", "3", "socket:[1234]")
	lock(t, root, "300", "9", "/home/u/.codex/thread-writer-locks/"+id+".lock")
	lock(t, root, "400", "9", "/home/u/.codex/thread-writer-locks/"+id+".lock")
	lock(t, root, "500", "9", "/home/u/.codex/thread-writer-locks/not-an-id.lock")

	if got, want := resumeOf(t, root, 200), "ollama launch codex --model gemma4:12b-64k -- --sandbox read-only "+
		"--ask-for-approval on-request resume "+id; got != want {
		t.Fatalf("through ollama launch:\n got %q\nwant %q", got, want)
	}
	if got, want := resumeOf(t, root, 300), "/home/u/.local/bin/codex -m gpt-6 -a never resume "+id; got != want {
		t.Fatalf("typed: got %q, want %q", got, want)
	}
	for _, pid := range []int{400, 500} {
		if got := resumeOf(t, root, pid); got != "" {
			t.Fatalf("pid %d: %q", pid, got)
		}
	}
}

// A record left from before a restart names a pid another program now has: it is not that program's
// conversation, and nothing is offered.
func TestAStaleRecordIsNotAnotherProgramsResume(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := fakeAgents(t, map[string][3]string{
		"100": {"(bash) S 50 100 100 34816 400 0", "-bash\x00", ""},
		"400": {"(python3) S 100 400 100 34816 400 0", "python3\x00train.py\x00", ""},
		"500": {"(node) S 100 500 100 34816 500 0",
			"node\x00/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js\x00", ""},
	})
	const id = "063a8e1e-a86a-4148-a783-1b72c652189f"
	record(t, filepath.Join(home, ".claude"), "400", `{"pid":400,"sessionId":"`+id+`","kind":"interactive"}`)
	record(t, filepath.Join(home, ".claude"), "500", `{"pid":500,"sessionId":"`+id+`","kind":"interactive"}`)
	if got := resumeOf(t, root, 400); got != "" {
		t.Fatalf("python3 holding a stale record's pid got %q", got)
	}
	// Claude Code run through Node, from its npm package, is still Claude Code.
	if got := resumeOf(t, root, 500); !strings.HasSuffix(got, "--resume "+id) {
		t.Fatalf("Claude Code through Node got %q", got)
	}
}

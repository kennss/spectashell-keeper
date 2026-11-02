// shellinteg_test.go
// Author: Kennt Kim
// Created: 2026-10-19 · Last updated: 2026-11-02
//
// Purpose: shell integration with the real shells of the machine (SiliconDesk's Docs/SDTERM-2.md
// §4.4): bash (4.4 or later), zsh and fish, each started as the session starts it, on a
// pseudo-terminal read by the emulator. The user's startup files still run (an alias from them
// works); each command ends with its command line and exit status; Ctrl-C at the prompt is no
// command (pressed again when bash lost the first); the user's own PROMPT_COMMAND still sees $?;
// bash's HISTFILE is ~/.bash_history and not exported; the variables that loaded the scripts are
// gone; zsh with no startup files is left alone. A shell that is missing (or a bash too old for
// marks: macOS's 3.2) is skipped.
//
// Notes:
// - Taken from SiliconDesk's host/internal/shellinteg/shellinteg_test.go on 2026-10-23.

//go:build unix

package shellinteg

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/kennss/spectashell-keeper/term"
)

type shellRun struct {
	t        *testing.T
	mu       sync.Mutex
	emu      *term.Terminal
	raw      bytes.Buffer
	finished []term.CommandFinished
	ptmx     *os.File
}

// startShell starts the login shell at path with the integration, in a home holding files.
func startShell(t *testing.T, path string, files map[string]string) *shellRun {
	return startShellAs(t, path, files, true)
}

// startShellAs starts the login shell at path in a home holding files, with the session's
// injection or as a plain login shell (another host's, whose rc file sources the scripts).
func startShellAs(t *testing.T, path string, files map[string]string, inject bool) *shellRun {
	t.Helper()
	home := t.TempDir()
	for name, text := range files {
		file := filepath.Join(home, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "integration")
	if err := Install(dir); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + home, "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "TERM=xterm-256color", "LANG=C.UTF-8",
		"USER=" + os.Getenv("USER")}
	argv := []string{"-" + filepath.Base(path)}
	if inject {
		var ok bool
		argv, env, ok = Inject(dir, path, argv, env, home)
		if !ok {
			t.Fatalf("%s got no integration", path)
		}
	} else {
		env = append(env, "SPECTASHELL_TEST_DIR="+dir)
	}
	cmd := exec.Command(path)
	cmd.Args, cmd.Env, cmd.Dir = argv, env, home
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	r := &shellRun{t: t, ptmx: ptmx}
	host, _ := os.Hostname() // as the session's terminals have it: the shell's OSC 7 names this machine
	r.emu = term.New(term.Options{Columns: 100, Rows: 40, Now: time.Now, Hostname: host,
		CommandFinished: func(c term.CommandFinished) { r.finished = append(r.finished, c) },
		Reply:           func(b []byte) { _, _ = ptmx.Write(b) }})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			r.mu.Lock()
			r.raw.Write(buf[:n])
			r.emu.Write(buf[:n])
			r.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = ptmx.Close()
		_ = cmd.Wait()
	})
	return r
}

// until waits for cond, holding the lock while it looks.
func (r *shellRun) until(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		ok := cond()
		r.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.t.Fatalf("waiting for %s; output %q", what, r.raw.String())
}

// within reports whether cond holds within d. Only for what the shell may never do by itself (a lost
// interrupt): everything else waits with until.
func (r *shellRun) within(d time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		r.mu.Lock()
		ok := cond()
		r.mu.Unlock()
		if ok {
			return true
		}
	}
	return false
}

func (r *shellRun) send(s string) {
	if _, err := r.ptmx.Write([]byte(s)); err != nil {
		r.t.Fatal(err)
	}
}

func (r *shellRun) commands() int { return len(r.finished) }

func (r *shellRun) output() string { return r.raw.String() }

// run types a command and waits for its end.
func (r *shellRun) run(line string) term.CommandFinished {
	r.t.Helper()
	before := 0
	r.mu.Lock()
	before = r.commands()
	r.mu.Unlock()
	r.send(line + "\r")
	r.until("the end of "+line, func() bool { return r.commands() > before })
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finished[before]
}

// prompts counts the lines of screen and history marked as prompts.
func (r *shellRun) prompts() int { return len(r.promptMarks()) }

// promptMarks are the prompt marks of history and screen, in order.
func (r *shellRun) promptMarks() []*term.Mark {
	var out []*term.Mark
	for i := 0; i < r.emu.HistoryLen(); i++ {
		if m := r.emu.HistoryLine(i).Mark; m != nil && m.Prompt {
			out = append(out, m)
		}
	}
	for y := 0; y < r.emu.Rows(); y++ {
		if m := r.emu.Line(y).Mark; m != nil && m.Prompt {
			out = append(out, m)
		}
	}
	return out
}

func expectEnd(t *testing.T, c term.CommandFinished, command string, exit int) {
	t.Helper()
	if c.Command != command || !c.HasExitCode || c.ExitCode != exit || !c.HasDuration {
		t.Fatalf("command %+v, want %q exit %d", c, command, exit)
	}
}

// exercise is what every shell is put through.
func exercise(t *testing.T, r *shellRun) {
	r.until("the prompt", func() bool { return strings.Contains(r.output(), "READY>") && r.prompts() > 0 })
	expectEnd(t, r.run("ll"), "ll", 0)
	r.until("the alias", func() bool { return strings.Contains(r.output(), "aliased!") })
	expectEnd(t, r.run("false"), "false", 1)
	// Ctrl-C at the prompt is no command. Each key waits for the shell: Ctrl-C flushes what was typed
	// ahead.
	prompts := 0
	r.until("the prompt", func() bool { prompts = r.prompts(); return true })
	r.send("echo never")
	r.until("the line typed", func() bool { return strings.Contains(r.output(), "echo never") })
	r.send("\x03")
	if !r.within(3*time.Second, func() bool { return r.prompts() > prompts }) {
		// bash 5.2 can lose the interrupt: readline echoed ^C, cleaned up and started reading again
		// before bash acted on it, so bash acts only at the next key (CI, 2026-10-21: the output
		// ended "echo never^C\e[?2004l\r\e[?2004h" for 15 s, where a passing run goes on with
		// "\e[?2004l\r\r\n" and the prompt). A second Ctrl-C, as a user would press, ends the line.
		r.t.Log("bash did not act on Ctrl-C; pressing it again")
		r.send("\x03")
	}
	r.until("a new prompt", func() bool { return r.prompts() > prompts })
	expectEnd(t, r.run("echo  two   words"), "echo two words", 0)
	// Every prompt carries the directory it was drawn in, one reported again at a prompt that did not
	// change it (after ssh to a host that reported its own, this shell's prompts still have one).
	root, _ := filepath.EvalSymlinks("/tmp")
	r.run("cd " + root)
	r.run(`printf '\033]7;file://elsewhere/x\007'`) // as a shell on another host reports
	r.until("a prompt in "+root, func() bool {
		marks := r.promptMarks()
		return len(marks) > 0 && marks[len(marks)-1].Directory == root
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, m := range r.promptMarks()[1:] { // the first is drawn before zsh's hooks are in
		if m.Directory == "" {
			t.Fatalf("prompt %d has no directory", i+1)
		}
	}
	for _, c := range r.finished {
		if strings.Contains(c.Command, "never") {
			t.Fatalf("a cancelled line ran: %+v", r.finished)
		}
	}
	if r.prompts() < 4 {
		t.Fatalf("%d prompts marked", r.prompts())
	}
	if !strings.Contains(r.output(), "\x1b]7;file://") {
		t.Fatal("no working directory reported")
	}
}

func TestBash(t *testing.T) {
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	out, err := exec.Command(path, "-c", `(( BASH_VERSINFO[0] * 100 + BASH_VERSINFO[1] >= 404 )) && echo yes`).Output()
	if err != nil || strings.TrimSpace(string(out)) != "yes" {
		t.Skip("bash before 4.4 has no PS0")
	}
	r := startShell(t, path, map[string]string{
		".bash_profile": "source ~/.bashrc\n",
		".bashrc": "PS1='READY> '\nalias ll='echo aliased!'\n" +
			`PROMPT_COMMAND='echo "rc saw $?"'` + "\n",
	})
	exercise(t, r)
	r.until("$? handed on", func() bool { return strings.Contains(r.output(), "rc saw 1") })
	r.run(`echo "hist=$HISTFILE env=[$ENV] exported=$(env | grep -c -e ^HISTFILE= -e ^SPECTASHELL_)"`)
	r.until("the variables", func() bool { return strings.Contains(r.output(), "/.bash_history env=[] exported=0") })
	if !strings.Contains(r.output(), "spectashell_started=") {
		t.Fatal("C without the start")
	}
}

// TestBashOnAnotherHost is bash where "Install Shell Integration on This Host…" put the script: a
// plain login shell whose ~/.bashrc sources it (its ~/.profile sourcing ~/.bashrc, as Ubuntu's does)
// marks its commands, and reads its startup files once — the script's own startup part is the
// session's injection's only.
func TestBashOnAnotherHost(t *testing.T) {
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	out, err := exec.Command(path, "-c", `(( BASH_VERSINFO[0] * 100 + BASH_VERSINFO[1] >= 404 )) && echo yes`).Output()
	if err != nil || strings.TrimSpace(string(out)) != "yes" {
		t.Skip("bash before 4.4 has no PS0")
	}
	r := startShellAs(t, path, map[string]string{
		".profile": "echo profile-read\n. ~/.bashrc\n",
		".bashrc": "PS1='READY> '\nalias ll='echo aliased!'\n" +
			`[ -r "$SPECTASHELL_TEST_DIR/bash/spectashell.bash" ] && . "$SPECTASHELL_TEST_DIR/bash/spectashell.bash"` + "\n",
	}, false)
	exercise(t, r)
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := strings.Count(r.output(), "profile-read"); n != 1 {
		t.Fatalf("~/.profile read %d times", n)
	}
}

func TestZsh(t *testing.T) {
	path, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh")
	}
	r := startShell(t, path, map[string]string{
		".zshrc": "PROMPT='READY> '\nalias ll='echo aliased!'\nunsetopt PROMPT_SP\n",
	})
	exercise(t, r)
	r.run(`echo "zdotdir=[$ZDOTDIR] exported=$(env | grep -c -e ^SPECTASHELL_)"`)
	r.until("the variables", func() bool { return strings.Contains(r.output(), "zdotdir=[] exported=0") })
	if !strings.Contains(r.output(), "cmdline_url=ll") || !strings.Contains(r.output(), "spectashell_started=") {
		t.Fatal("C without the command line or the start")
	}
}

func TestZshWithoutStartupFilesIsLeftAlone(t *testing.T) {
	home := t.TempDir()
	if _, _, ok := Inject("/x", "/bin/zsh", []string{"-zsh"}, []string{"HOME=" + home}, home); ok {
		t.Fatal("zsh-newuser-install would not run")
	}
	if _, _, ok := Inject("/x", "/bin/zsh", []string{"-zsh"}, nil, home); ok {
		t.Fatal("injected with no files")
	}
	if argv, env, ok := Inject("/x", "/bin/dash", []string{"-dash"}, []string{"A=1"}, home); ok ||
		len(argv) != 1 || len(env) != 1 {
		t.Fatal("dash changed")
	}
}

func TestFish(t *testing.T) {
	path, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("no fish")
	}
	r := startShell(t, path, map[string]string{
		".config/fish/config.fish": "function fish_prompt; echo -n 'READY> '; end\nalias ll 'echo aliased!'\n",
	})
	exercise(t, r)
	r.run(`echo "dirs=[$XDG_DATA_DIRS] exported=(env | grep -c -e ^SPECTASHELL_)"`)
	r.until("the variables", func() bool { return strings.Contains(r.output(), "dirs=[] exported=0") })
}

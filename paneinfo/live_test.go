// live_test.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-29
//
// Purpose: what runs in real terminals on this machine (Linux or macOS), read by Reader.Read: a
// shell on a pseudo-terminal with a program in front in a process group of its own and a server in
// the background — the program and its process id, its folder and branch (the shell's reported
// folder when it is where the program is, the program's when the report is stale), the server's
// ports on loopback and on every address; Claude Code's resume command from a record in
// CLAUDE_CONFIG_DIR, and Codex's from the thread lock it holds open; a shell without a terminal;
// and a shell that ended. Also what a read costs (BenchmarkRead).
//
// Notes:
// - The test binary is its own shell and programs (TestMain, helperEnv): the "shell" starts its
//   front program with the terminal's foreground given to it (SysProcAttr.Foreground) and its
//   background one in its own group, waits for each to say it is set up (a line on descriptor 3),
//   then prints "ready <front pid>" on the terminal. So each test awaits what it reads, never a
//   deadline. Programs are copies of the binary under the names they are shown by (`sleep`,
//   `claude`, `codex`, `server`): argv[0] is what Program shows, and the name a port's holder has.
// - What macOS reads through libproc (folder, open files, ports) is checked only in a build with
//   cgo (readsProcessFiles); Linux reads it all.

//go:build linux || darwin

package paneinfo

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

const (
	helperEnv = "SDTERM_PANEINFO_HELPER" // "shell" or "idle": the test binary plays that part
	configEnv = "SDTERM_PANEINFO_CONFIG" // the shell's helperConfig, as JSON
	lockEnv   = "SDTERM_PANEINFO_LOCK"   // a file an idle program holds open
	listenEnv = "SDTERM_PANEINFO_LISTEN" // set: an idle program listens on 127.0.0.1:0 and :0
)

// helperConfig is what a helper shell runs.
type helperConfig struct {
	Front      []string // the program in front, in a process group of its own; none: at its prompt
	FrontEnv   []string // added to its environment
	Background []string // a program in the shell's group, not in front
}

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "shell":
		helperShell()
	case "idle":
		helperIdle()
	default:
		os.Exit(m.Run())
	}
}

// kept holds what a helper keeps open until it is killed.
var kept []any

// waitForever blocks until the helper is killed (a sleep, not select{}: with nothing else to run
// the runtime would call that a deadlock).
func waitForever() {
	for {
		time.Sleep(time.Hour)
	}
}

func helperShell() {
	var cfg helperConfig
	if err := json.Unmarshal([]byte(os.Getenv(configEnv)), &cfg); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	// Giving the terminal to another group from outside the foreground stops the caller unless
	// SIGTTOU is ignored, as a shell ignores it.
	signal.Ignore(syscall.SIGTTOU, syscall.SIGTTIN)
	start := func(args, env []string, front bool) int {
		r, w, err := os.Pipe()
		if err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = append(append(os.Environ(), helperEnv+"=idle"), env...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.ExtraFiles = []*os.File{w}
		if front {
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Foreground: true, Ctty: 0}
		}
		if err := cmd.Start(); err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		w.Close()
		if line, err := bufio.NewReader(r).ReadString('\n'); err != nil || !strings.HasPrefix(line, "up") {
			fmt.Printf("error: %s did not start: %q %v\n", args[0], line, err)
			os.Exit(1)
		}
		kept = append(kept, cmd)
		return cmd.Process.Pid
	}
	front := os.Getpid()
	if len(cfg.Background) > 0 {
		start(cfg.Background, []string{listenEnv + "=1"}, false)
	}
	if len(cfg.Front) > 0 {
		front = start(cfg.Front, cfg.FrontEnv, true)
	}
	fmt.Printf("ready %d\n", front)
	waitForever()
}

func helperIdle() {
	if path := os.Getenv(lockEnv); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			os.Exit(1)
		}
		kept = append(kept, f)
	}
	if os.Getenv(listenEnv) != "" {
		for _, addr := range []string{"127.0.0.1:0", ":0"} {
			l, err := net.Listen("tcp", addr)
			if err != nil {
				os.Exit(1)
			}
			kept = append(kept, l)
		}
	}
	if report := os.NewFile(3, "report"); report != nil {
		fmt.Fprintln(report, "up")
		report.Close()
	}
	waitForever()
}

// copyAs copies the test binary to dir/name, so it runs as a program of that name.
func copyAs(t testing.TB, dir, name string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(dir, name)
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// terminal starts a helper shell on a pseudo-terminal in dir, and returns its pid and the pid of
// the program in front (the shell's own at its prompt) once both are set up. Everything it started
// is killed when the test ends.
func terminal(t testing.TB, dir string, cfg helperConfig) (shell, front int) {
	t.Helper()
	data, _ := json.Marshal(cfg)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), helperEnv+"=shell", configEnv+"="+string(data))
	ptmx, err := pty.Start(cmd) // a session of its own, the terminal its controlling one
	if err != nil {
		t.Fatal(err)
	}
	shell = cmd.Process.Pid
	t.Cleanup(func() {
		if front != shell && front > 0 {
			syscall.Kill(-front, syscall.SIGKILL)
		}
		syscall.Kill(-shell, syscall.SIGKILL)
		cmd.Wait()
		ptmx.Close()
	})
	line, err := bufio.NewReader(ptmx).ReadString('\n')
	ready, ok := strings.CutPrefix(strings.TrimSpace(line), "ready ")
	if err != nil || !ok {
		t.Fatalf("the shell did not start: %q %v", line, err)
	}
	if front, err = strconv.Atoi(ready); err != nil {
		t.Fatalf("ready %q", ready)
	}
	return shell, front
}

// canonical is dir as the system names it (macOS's temporary folders are under a link, /var).
func canonical(t testing.TB, dir string) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestReadingARealTerminal(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	bin := t.TempDir()
	shell, front := terminal(t, dir, helperConfig{
		Front:      []string{copyAs(t, bin, "sleep"), "30"},
		Background: []string{copyAs(t, bin, "server")},
	})
	if front == shell {
		t.Fatal("nothing in front")
	}
	r := NewReader()
	info, err := r.Read(shell, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", info)
	if info.ProgramPID != front || info.Program != "sleep 30" || info.Remote != "" || info.Resume != "" {
		t.Fatalf("front %+v (front %d)", info, front)
	}
	if readsProcessFiles {
		if info.Directory != canonical(t, dir) || info.Branch != "main" {
			t.Fatalf("folder %+v", info)
		}
		// One port on loopback and one on every address, both the server's.
		if len(info.Ports) != 2 || info.Ports[0].Process != "server" || info.Ports[1].Process != "server" ||
			info.Ports[0].Everywhere == info.Ports[1].Everywhere {
			t.Fatalf("ports %+v", info.Ports)
		}
	} else if info.Directory != "" || info.Ports != nil {
		t.Fatalf("not known without libproc: %+v", info)
	}
	// The folder as the shell reported it, when it is where the program is: macOS's temporary
	// folder is a link (/var to /private/var), and the shell's spelling is kept.
	if info, _ := r.Read(shell, dir); info.Directory != dir || info.Branch != "main" {
		t.Fatalf("reported %+v", info)
	}
	// A folder the shell reported before the program started elsewhere is stale: the program's own
	// (2026-10-29); without libproc only the report is known.
	want := "/"
	if readsProcessFiles {
		want = canonical(t, dir)
	}
	if info, _ := r.Read(shell, "/"); info.Directory != want {
		t.Fatalf("stale report %+v", info)
	}
}

func TestResumingRealAgents(t *testing.T) {
	bin := t.TempDir()
	home := t.TempDir()
	config := t.TempDir()
	const id = "063a8e1e-a86a-4148-a783-1b72c652189f"
	claude := copyAs(t, bin, "claude")
	shell, front := terminal(t, t.TempDir(), helperConfig{
		Front:    []string{claude, "--permission-mode", "manual", "fix it"},
		FrontEnv: []string{"CLAUDE_CONFIG_DIR=" + config, "HOME=" + home},
	})
	r := NewReader()
	if info, _ := r.Read(shell, ""); info.ProgramPID != front || info.Resume != "" {
		t.Fatalf("no record yet %+v", info)
	}
	record(t, config, strconv.Itoa(front), `{"pid":`+strconv.Itoa(front)+`,"sessionId":"`+id+`","kind":"interactive"}`)
	info, err := r.Read(shell, "")
	if want := shellJoin([]string{claude, "--permission-mode", "manual", "--resume", id}); err != nil || info.Resume != want {
		t.Fatalf("claude:\n got %q %v\nwant %q", info.Resume, err, want)
	}
	if info.Program != "claude --permission-mode manual fix it" {
		t.Fatalf("program %q", info.Program)
	}

	if !readsProcessFiles {
		return // Codex's lock is among its open files, which only libproc tells on macOS
	}
	const thread = "01a164e3-3c52-73f0-8835-12e397e40b19"
	locks := filepath.Join(home, ".codex", "thread-writer-locks")
	os.MkdirAll(locks, 0o700)
	codex := copyAs(t, bin, "codex")
	shell, _ = terminal(t, t.TempDir(), helperConfig{
		Front:    []string{codex, "-m", "gpt-6", "resume", "old"},
		FrontEnv: []string{"HOME=" + home, lockEnv + "=" + filepath.Join(locks, thread+".lock")},
	})
	info, err = r.Read(shell, "")
	if want := shellJoin([]string{codex, "-m", "gpt-6", "resume", thread}); err != nil || info.Resume != want {
		t.Fatalf("codex:\n got %q %v\nwant %q", info.Resume, err, want)
	}
}

func TestReadingAShellWithoutATerminal(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), helperEnv+"=idle")
	cmd.ExtraFiles = []*os.File{w}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	if line, err := bufio.NewReader(r).ReadString('\n'); err != nil || line != "up\n" {
		t.Fatalf("idle %q %v", line, err)
	}
	pid := cmd.Process.Pid
	info, err := NewReader().Read(pid, "")
	if err != nil || info.ProgramPID != pid || info.Program != filepath.Base(self) {
		t.Fatalf("no terminal %+v %v", info, err)
	}
}

func TestAShellThatEnded(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	// Its pid is free now (until reused, which is unlikely within the test).
	if _, err := NewReader().Read(cmd.Process.Pid, ""); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ended: %v", err)
	}
}

// BenchmarkRead is what a keeper's read every 2 seconds costs, for a terminal with a program in
// front and a server listening in the background.
func BenchmarkRead(b *testing.B) {
	dir := b.TempDir()
	bin := b.TempDir()
	shell, _ := terminal(b, dir, helperConfig{
		Front:      []string{copyAs(b, bin, "sleep"), "30"},
		Background: []string{copyAs(b, bin, "server")},
	})
	r := NewReader()
	for b.Loop() {
		if _, err := r.Read(shell, ""); err != nil {
			b.Fatal(err)
		}
	}
}

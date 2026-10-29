// launch.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-30
//
// Purpose: what a terminal's program is, before a keeper starts it: the user's login shell or a
// command, its folder, and its environment with the terminal's variables and, for a login shell,
// shell integration (Docs/CONNECTIONS.md §1; SiliconDesk's PROTOCOL.md §7.1, §7.7).
//
// Notes:
// - Taken from SiliconDesk's host/internal/termhost/termhost.go (LoginShell, TerminalEnv and their
//   helpers) and host/internal/session/terminal.go (lookPath, and how a login shell gets shell
//   integration) on 2026-10-23. Changed: TERM_PROGRAM is the engine user's, not built in; the
//   login shell is read from the user database before $SHELL, since a Mac app's $SHELL is the one
//   launchd gave at login and a `chsh` since is not in it (usershell_*.go). Fixed: a locale that
//   is not UTF-8 is replaced, not followed, by LANG=C.UTF-8 (SiliconDesk's TerminalEnv left the
//   old LANG first, which getenv returns); and a variable the engine's user gives (Extra) replaces
//   the terminal's own instead of following it — a LANG or TERM given there came after
//   LANG=C.UTF-8 or TERM=xterm-256color and was never read (found by SiliconDesk on 2026-10-23).
// - A login shell runs as "-<name>" (argv[0] with a dash), as login(1) starts it.
// - The program starts from the user's login session, not from whatever started the process
//   building it (`sessionVariable`): only the variables a login session has — who the user is,
//   where home is, the path, the locale, the time zone, the temporary folder, the agent's socket,
//   the display — are taken from the environment given; the shell's profile adds the user's own.
//   Any other variable is another program's: a coding agent's marks on its child processes
//   (Claude Code's CLAUDE_CODE_CHILD_SESSION, after which it saves no transcript), another
//   terminal's names and session ids (iTerm's, cmux's, Apple Terminal's), a multiplexer's (TMUX,
//   STY), an app's own process. A Mac app opened from a shell — `open` in a Claude Code session —
//   has that shell's whole environment, and its shells had it too (2026-10-27); opened from the
//   Dock it has launchd's. The shell now starts the same either way.

//go:build unix

package termhost

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/kennss/spectashell-keeper/shellinteg"
)

// Program is what a keeper runs: Path is executed with Argv (Argv[0] may differ, "-zsh"), in
// Dir, with exactly Env.
type Program struct {
	Path string
	Argv []string
	Dir  string
	Env  []string
}

// Launch describes the program a terminal should run.
type Launch struct {
	// Argv is the command and its arguments; empty runs the user's login shell as a login shell.
	Argv []string
	// Dir is its folder; empty is the user's home.
	Dir string
	// Env is the environment it is started from: only its login session's variables are taken
	// (TerminalEnv), whatever else the process starting it had.
	Env []string
	// TermProgram is TERM_PROGRAM's value: the application's name ("sdTerm").
	TermProgram string
	// Extra is added after the terminal's variables, so these win.
	Extra map[string]string
	// IntegrationDir, when not empty and Argv is, is where the shell integration scripts are
	// written; the login shell then loads them (shellinteg.Inject). Empty: no integration.
	IntegrationDir string
}

// Prepare turns a Launch into the Program to start.
func Prepare(l Launch) (Program, error) {
	home, _ := os.UserHomeDir()
	dir := l.Dir
	if dir == "" {
		dir = home
	}
	env := TerminalEnv(l.Env, l.TermProgram, l.Extra)
	if len(l.Argv) == 0 {
		path := LoginShell(env)
		argv := []string{"-" + filepath.Base(path)}
		if l.IntegrationDir != "" {
			// Scripts that cannot be written leave the shell as it would start without them.
			if err := shellinteg.Install(l.IntegrationDir); err == nil {
				argv, env, _ = shellinteg.Inject(l.IntegrationDir, path, argv, env, home)
			}
		}
		return Program{Path: path, Argv: argv, Dir: dir, Env: env}, nil
	}
	path, err := lookPath(l.Argv[0], env)
	if err != nil {
		return Program{}, err
	}
	return Program{Path: path, Argv: l.Argv, Dir: dir, Env: env}, nil
}

// LoginShell returns the user's shell: the user database's entry when it is an executable, else
// $SHELL from env when it is one, else /bin/sh.
func LoginShell(env []string) string {
	if sh := userShell(); sh != "" && isExecutable(sh) {
		return sh
	}
	if sh := lookupEnv(env, "SHELL"); sh != "" && isExecutable(sh) {
		return sh
	}
	return "/bin/sh"
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// lookPath resolves a program name with the PATH of env (not the engine's own).
func lookPath(name string, env []string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	for _, dir := range filepath.SplitList(lookupEnv(env, "PATH")) {
		p := filepath.Join(dir, name)
		if isExecutable(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not found in PATH", name)
}

func lookupEnv(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

// TerminalEnv returns the login session's variables of env (sessionVariable) with the terminal
// variables set: TERM, COLORTERM, TERM_PROGRAM (when program is not empty), and LANG=C.UTF-8 when
// the character locale is not UTF-8; then extra, whose values win. Each variable appears once: the C library's getenv returns the first of two, so a
// value appended after another of the same name is never read.
//
// The locale is judged with extra applied. When it is not UTF-8, the inherited LANG, LC_ALL and
// LC_CTYPE go; LANG=C.UTF-8 takes their place unless extra names a locale, which then stands as
// the engine's user chose it.
func TerminalEnv(env []string, program string, extra map[string]string) []string {
	own := []string{"TERM=xterm-256color", "COLORTERM=truecolor"}
	if program != "" {
		own = append(own, "TERM_PROGRAM="+program)
	}
	out := make([]string, 0, len(env)+len(own)+len(extra)+1)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if _, set := extra[k]; sessionVariable(k) && !set {
			out = append(out, kv)
		}
	}
	for _, kv := range own {
		k, _, _ := strings.Cut(kv, "=")
		if _, set := extra[k]; !set {
			out = append(out, kv)
		}
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	added := make([]string, 0, len(keys))
	for _, k := range keys {
		added = append(added, k+"="+extra[k])
	}
	if !localeIsUTF8(append(slices.Clip(out), added...)) {
		out = removeKeys(out, "LANG", "LC_ALL", "LC_CTYPE")
		if !namesLocale(extra) {
			out = append(out, "LANG=C.UTF-8")
		}
	}
	return append(out, added...)
}

// sessionVariable says whether the variable named k is one a user's login session has, which a
// terminal's program starts with (see the notes): any other is another program's.
func sessionVariable(k string) bool {
	switch k {
	case "HOME", "USER", "LOGNAME", "SHELL", "PATH", "TMPDIR", "TZ", "LANG", "LANGUAGE", "MAIL",
		"SSH_AUTH_SOCK", "DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS",
		// The Mac's: the text encoding CoreFoundation reads, which launchd sets for every session.
		"__CF_USER_TEXT_ENCODING":
		return true
	}
	return localeCategories[k] || strings.HasPrefix(k, "XDG_")
}

// localeCategories are the variables that choose a locale, POSIX's and glibc's, by name: other
// LC_ names are not the locale's — iTerm2 calls its own LC_TERMINAL, so that ssh's usual
// `SendEnv LC_*` carries it to hosts.
var localeCategories = map[string]bool{
	"LC_ALL": true, "LC_CTYPE": true, "LC_COLLATE": true, "LC_MESSAGES": true, "LC_MONETARY": true,
	"LC_NUMERIC": true, "LC_TIME": true, "LC_ADDRESS": true, "LC_IDENTIFICATION": true,
	"LC_MEASUREMENT": true, "LC_NAME": true, "LC_PAPER": true, "LC_TELEPHONE": true,
}

// namesLocale says whether extra sets one of the variables that choose the character locale.
func namesLocale(extra map[string]string) bool {
	for _, k := range []string{"LANG", "LC_ALL", "LC_CTYPE"} {
		if _, ok := extra[k]; ok {
			return true
		}
	}
	return false
}

func localeIsUTF8(env []string) bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := lookupEnv(env, k); v != "" {
			v = strings.ToLower(v)
			return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
		}
	}
	return false
}

func removeKeys(env []string, keys ...string) []string {
	out := env[:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		keep := true
		for _, drop := range keys {
			if k == drop {
				keep = false
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}

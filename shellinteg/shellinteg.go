// shellinteg.go
// Author: Kennt Kim
// Created: 2026-10-19 · Last updated: 2026-11-02
//
// Purpose: shell integration for terminals (SiliconDesk's Docs/SDTERM-2.md §4.3, PROTOCOL.md §7.7)
// — the scripts that make bash, zsh and fish mark their prompts and commands with OSC 133, and the
// change to how a login shell is started that loads them, without editing the user's files (as
// kitty, Ghostty and VS Code do).
//
// Notes:
// - Taken from SiliconDesk's host/internal/shellinteg/shellinteg.go on 2026-10-23.
// - The scripts are built into the binary (scripts/) and written to a directory on the host by
//   Install, rewritten only when they differ; a shell reads them once, at start, so a shell left
//   running by an earlier build is not affected by a later one rewriting them.
// - Install also removes what the directory holds that is not one of the scripts: the Mac's
//   directory is the app's, kept from build to build, and fish reads every file in
//   vendor_conf.d — the scripts renamed from sdterm.* on 2026-11-02 would have left sdterm.fish
//   there, marking each prompt and command a second time.
// - Inject changes the argv and environment of a login shell (argv0 starting with '-'):
//   bash: `-bash --posix` with ENV=…/bash/spectashell.bash and SPECTASHELL_BASH_INJECT (the script
//   ends POSIX mode and reads the login files; without the variable — sourced from another host's
//   ~/.bashrc — it only sets the marks); HISTFILE exported when unset, since POSIX mode would use
//   ~/.sh_history, and unexported by the script; the user's ENV kept in SPECTASHELL_ENV.
//   zsh: ZDOTDIR=…/zsh, the user's own kept in SPECTASHELL_ZDOTDIR — not when the user has no zsh
//   startup files at all, so zsh-newuser-install still runs.
//   fish: …/ (holding fish/vendor_conf.d) first in XDG_DATA_DIRS, which is made up as fish's own
//   default (/usr/local/share:/usr/share) when unset; the script takes it out again.
//   Any other shell is started as it is.

package shellinteg

import (
	"bytes"
	"embed"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

//go:embed all:scripts
var scripts embed.FS

// Install writes the scripts under dir, leaving files that are already the same, and removes
// whatever else dir holds (see the notes): dir is the scripts' own.
func Install(dir string) error {
	ours := map[string]bool{}
	err := fs.WalkDir(scripts, "scripts", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(name, "scripts"), "/")
		ours[rel] = true
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := scripts.ReadFile(name)
		if err != nil {
			return err
		}
		if old, err := os.ReadFile(target); err == nil && bytes.Equal(old, data) {
			return nil
		}
		tmp := target + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return err
		}
		return os.Rename(tmp, target)
	})
	if err != nil {
		return err
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." || ours[filepath.ToSlash(rel)] {
			return nil
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		if d.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
}

// Inject returns the argv and environment that start the login shell at shellPath (argv[0] begins
// with '-') with the integration installed in dir; ok is false when this shell gets none, and argv
// and env are then returned unchanged. home is the user's home directory.
func Inject(dir, shellPath string, argv, env []string, home string) (newArgv, newEnv []string, ok bool) {
	if len(argv) != 1 || !strings.HasPrefix(argv[0], "-") {
		return argv, env, false
	}
	switch path.Base(filepath.ToSlash(shellPath)) {
	case "bash":
		userEnv, hasEnv := lookup(env, "ENV")
		_, hasHistfile := lookup(env, "HISTFILE")
		env = withoutKeys(env, "ENV", "SPECTASHELL_ENV", "SPECTASHELL_UNEXPORT_HISTFILE", "SPECTASHELL_BASH_INJECT")
		if hasEnv {
			env = append(env, "SPECTASHELL_ENV="+userEnv)
		}
		if !hasHistfile && home != "" {
			env = append(env, "HISTFILE="+filepath.Join(home, ".bash_history"), "SPECTASHELL_UNEXPORT_HISTFILE=1")
		}
		env = append(env, "ENV="+filepath.Join(dir, "bash", "spectashell.bash"), "SPECTASHELL_BASH_INJECT=1")
		return []string{argv[0], "--posix"}, env, true
	case "zsh":
		zdotdir, set := lookup(env, "ZDOTDIR")
		if !hasZshFiles(zdotdir, home) {
			return argv, env, false
		}
		env = withoutKeys(env, "ZDOTDIR", "SPECTASHELL_ZDOTDIR")
		if set {
			env = append(env, "SPECTASHELL_ZDOTDIR="+zdotdir)
		}
		env = append(env, "ZDOTDIR="+filepath.Join(dir, "zsh"))
		return argv, env, true
	case "fish":
		dirs, set := lookup(env, "XDG_DATA_DIRS")
		env = withoutKeys(env, "XDG_DATA_DIRS", "SPECTASHELL_XDG_DATA_DIRS_UNSET", "SPECTASHELL_SHELL_INTEGRATION_DIR")
		if !set || dirs == "" {
			dirs = "/usr/local/share:/usr/share"
			env = append(env, "SPECTASHELL_XDG_DATA_DIRS_UNSET=1")
		}
		env = append(env, "XDG_DATA_DIRS="+dir+":"+dirs, "SPECTASHELL_SHELL_INTEGRATION_DIR="+dir)
		return argv, env, true
	}
	return argv, env, false
}

// hasZshFiles says whether the user has any zsh startup file (in ZDOTDIR, else home).
func hasZshFiles(zdotdir, home string) bool {
	base := zdotdir
	if base == "" {
		base = home
	}
	if base == "" {
		return false
	}
	for _, name := range []string{".zshenv", ".zprofile", ".zshrc", ".zlogin"} {
		if _, err := os.Stat(filepath.Join(base, name)); err == nil {
			return true
		}
	}
	return false
}

// lookup is the value of key in env (the last one wins, as for exec).
func lookup(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// withoutKeys returns a copy of env without the given keys.
func withoutKeys(env []string, keys ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, key := range keys {
			drop = drop || k == key
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// privatedir.go
// Author: Kennt Kim
// Created: 2026-10-14 · Last updated: 2026-10-29
//
// Purpose: the folder that holds keepers' sockets and descriptions, and the check that only the
// user can reach it (Docs/DESIGN.md §5.1, §5.3). Anyone who can reach a keeper's socket can type
// into its shell, so a folder that is not the user's own is refused, never used.
//
// Notes:
// - Taken from SiliconDesk's host/internal/paths/paths.go on 2026-10-23 (EnsurePrivateDir, and the
//   reasoning behind RuntimeDir); the rest of that file is SiliconDesk's session and is not here.
// - Linux: <temp dir>/<name>-<uid>/keepers, tmux's place — not $XDG_RUNTIME_DIR, which logind
//   deletes when the user's last login ends unless lingering is enabled (off by default on Ubuntu),
//   taking the sockets of shells that must outlive every login.
// - macOS: ~/Library/Application Support/<app>/keepers. macOS's tmp_cleaner deletes regular files
//   in /tmp untouched for 3 days (a keeper's description is one), and $TMPDIR is a per-user folder
//   for an app yet unset in an SSH login, so a temporary folder is not even one place on a Mac.
// - The environment variable named by Place.Override, when set, names the folder instead.

//go:build unix

package keeper

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// Place names where an application's keepers live. The engine knows no application: its user
// says what the folders are called.
type Place struct {
	// TempName is the Linux folder's name in the temporary directory, before "-<uid>"
	// ("spectashell" gives /tmp/spectashell-1000).
	TempName string
	// AppSupportName is the macOS folder's name in ~/Library/Application Support ("SpectaShell").
	AppSupportName string
	// Override is an environment variable that, when set, names the keepers' folder itself.
	Override string
}

// Dir returns the folder of keepers' sockets, creating it and its parent when needed, after
// checking that only the current user can reach either.
func (p Place) Dir() (string, error) {
	if p.Override != "" {
		if dir := os.Getenv(p.Override); dir != "" {
			return dir, EnsurePrivateDir(dir)
		}
	}
	var base string
	switch runtime.GOOS {
	case "darwin", "ios":
		if p.AppSupportName == "" {
			return "", errors.New("keeper: no Application Support folder name")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("keeper: %w", err)
		}
		support := filepath.Join(home, "Library", "Application Support")
		if err := os.MkdirAll(support, 0o700); err != nil {
			return "", fmt.Errorf("keeper: %w", err)
		}
		base = filepath.Join(support, p.AppSupportName)
	default:
		if p.TempName == "" {
			return "", errors.New("keeper: no temporary folder name")
		}
		// Not os.TempDir's $TMPDIR: the place must be the same for every login of the user.
		base = filepath.Join("/tmp", fmt.Sprintf("%s-%d", p.TempName, os.Getuid()))
	}
	if err := EnsurePrivateDir(base); err != nil {
		return "", err
	}
	dir := filepath.Join(base, "keepers")
	return dir, EnsurePrivateDir(dir)
}

// EnsurePrivateDir creates dir with mode 0700 if it does not exist, then verifies it is a real
// directory (not a symlink) owned by the current user with no group or other permissions.
func EnsurePrivateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("keeper: create %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("keeper: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("keeper: %s is not a directory", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("keeper: cannot read the owner of %s", dir)
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("keeper: %s is owned by uid %d, not by the current user", dir, st.Uid)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("keeper: %s has mode %o; it must not be accessible to group or others", dir, fi.Mode().Perm())
	}
	return nil
}

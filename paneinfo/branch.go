// branch.go
// Author: Kennt Kim
// Created: 2026-10-20 · Last updated: 2026-10-30
//
// Purpose: the git branch of a terminal's directory (Docs/design/02-CONNECTIONS.md §3) — the one
// read that touches the terminal's file system — without running git; and the mounted file systems
// that say whether a directory is on a network share (Linux's mountinfo here, macOS's in
// system_darwin.go).
//
// Notes:
// - Taken from SiliconDesk's host/internal/paneinfo/branch.go on 2026-10-23. Here a Reader's
//   branches serve one keeper's shell, so they are kept to the maxEntries directories asked for
//   last, where SiliconDesk's grew with every directory a session's terminals visited.
// - The repository is found by looking upward from the directory for `.git`: a directory, or a
//   file `gitdir: <path>` (a worktree, a submodule) that is followed. Its HEAD names the branch
//   (`ref: refs/heads/main` gives "main"); a detached HEAD gives its hash's first 7 characters.
// - On a local disk it is read at once and kept per directory, read again only when HEAD's
//   modification time changes (one stat per directory per read).
// - On a network share, or under an automount point (a stat would mount the share), a read is a
//   round trip each time and blocks without end on a dead hard-mounted share: it is read off the
//   caller's goroutine — one read in flight per directory, at most once per networkEvery — and the
//   last answer is given meanwhile. A read stuck on a dead share stays the only one for its
//   directory until it returns: no goroutines pile up behind it.
// - The mounts are read from the kernel's table, never by touching the directory (which is what
//   could block): Linux's /proc/self/mountinfo, macOS's getfsstat with MNT_NOWAIT. A directory's
//   file system is that of the longest mount point above it; of two mounted on the same point the
//   later one, which hides the earlier.
// - Linux's network types are SiliconScope's collector's (agent/collect, netmounts_linux.go: nfs,
//   nfs4, cifs, smb3, fuse.sshfs), and autofs.

package paneinfo

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	networkEvery = time.Minute
	// maxDepth bounds the walk upward for `.git`.
	maxDepth = 64
	// maxEntries bounds the directories whose branch is kept.
	maxEntries = 64
)

// networkFSTypes are Linux's file systems served by another machine (the collector's), and autofs,
// whose mount points mount the share when touched.
var networkFSTypes = map[string]bool{
	"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "fuse.sshfs": true, "autofs": true,
}

// mount is a mounted file system: where, and whether reading under it is a round trip to another
// machine (or mounts one).
type mount struct {
	point   string
	network bool
}

// mounts is the mounted file systems, longest point first.
type mounts []mount

// newMounts orders the kernel's list of mounts (oldest first) for remote: longest point first, and
// of two on the same point the later one first.
func newMounts(list []mount) mounts {
	out := slices.Clone(list)
	slices.Reverse(out)
	slices.SortStableFunc(out, func(a, b mount) int { return len(b.point) - len(a.point) })
	return out
}

// readMountinfo reads Linux's mountinfo (/proc/self/mountinfo); an unreadable one gives none (all
// local).
func readMountinfo(path string) mounts {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var list []mount
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		// id parent major:minor root point options [optional...] - type source superoptions
		before, after, ok := strings.Cut(scanner.Text(), " - ")
		fields := strings.Fields(before)
		typeFields := strings.Fields(after)
		if !ok || len(fields) < 5 || len(typeFields) < 1 {
			continue
		}
		list = append(list, mount{point: unescapeMount(fields[4]), network: networkFSTypes[typeFields[0]]})
	}
	return newMounts(list)
}

// unescapeMount undoes mountinfo's octal escapes (`\040` for a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, ok := octal(s[i+1 : i+4]); ok {
				b.WriteByte(n)
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func octal(s string) (byte, bool) {
	var n int
	for _, c := range s {
		if c < '0' || c > '7' {
			return 0, false
		}
		n = n*8 + int(c-'0')
	}
	return byte(n), n < 256
}

// remote says whether dir is on a network share or under an automount point.
func (m mounts) remote(dir string) bool {
	for _, mount := range m {
		if dir == mount.point || strings.HasPrefix(dir, strings.TrimSuffix(mount.point, "/")+"/") {
			return mount.network
		}
	}
	return false
}

// branchEntry is what is known of one directory's branch.
type branchEntry struct {
	head    string    // the HEAD file found, "" when none
	mod     time.Time // HEAD's modification time when read
	branch  string
	read    time.Time // when it was last read (network)
	reading bool      // a network read is in flight
	used    time.Time // when it was last asked for
}

// branches reads and keeps the branches of directories.
type branches struct {
	mu      sync.Mutex
	entries map[string]*branchEntry
	now     func() time.Time
	reads   sync.WaitGroup // the network reads in flight, for the tests to await
}

// newBranches makes an empty cache.
func newBranches() *branches {
	return &branches{entries: map[string]*branchEntry{}, now: time.Now}
}

// branch is dir's branch ("" for none, or not known yet on a network share).
func (b *branches) branch(dir string, m mounts) string {
	if dir == "" || !filepath.IsAbs(dir) {
		return ""
	}
	b.mu.Lock()
	e := b.entries[dir]
	if e == nil {
		b.prune()
		e = &branchEntry{}
		b.entries[dir] = e
	}
	e.used = b.now()
	if !m.remote(dir) {
		defer b.mu.Unlock()
		if e.head != "" {
			if info, err := os.Stat(e.head); err == nil && info.ModTime().Equal(e.mod) {
				return e.branch
			}
		}
		e.head, e.mod, e.branch = readBranch(dir)
		return e.branch
	}
	if !e.reading && (e.read.IsZero() || b.now().Sub(e.read) >= networkEvery) {
		e.reading = true
		b.reads.Add(1)
		go func() {
			defer b.reads.Done()
			head, mod, branch := readBranch(dir)
			b.mu.Lock()
			defer b.mu.Unlock()
			e.head, e.mod, e.branch = head, mod, branch
			e.read, e.reading = b.now(), false
		}()
	}
	branch := e.branch
	b.mu.Unlock()
	return branch
}

// prune makes room for one more entry, forgetting the directories asked for longest ago. An entry
// with a read in flight is kept: forgetting it would let a second read of its directory start
// while the first may be stuck. Called with mu held.
func (b *branches) prune() {
	for len(b.entries) >= maxEntries {
		var oldest string
		for dir, e := range b.entries {
			if !e.reading && (oldest == "" || e.used.Before(b.entries[oldest].used)) {
				oldest = dir
			}
		}
		if oldest == "" {
			return
		}
		delete(b.entries, oldest)
	}
}

// readBranch finds dir's repository and reads its HEAD: the HEAD file, its modification time and
// the branch.
func readBranch(dir string) (string, time.Time, string) {
	for d, i := dir, 0; i < maxDepth; d, i = filepath.Dir(d), i+1 {
		gitPath := filepath.Join(d, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			gitDir := gitPath
			if !info.IsDir() {
				data, err := os.ReadFile(gitPath)
				if err != nil {
					return "", time.Time{}, ""
				}
				target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
				if !ok {
					return "", time.Time{}, ""
				}
				gitDir = strings.TrimSpace(target)
				if !filepath.IsAbs(gitDir) {
					gitDir = filepath.Join(d, gitDir)
				}
			}
			head := filepath.Join(gitDir, "HEAD")
			info, err := os.Stat(head)
			if err != nil {
				return "", time.Time{}, ""
			}
			data, err := os.ReadFile(head)
			if err != nil {
				return "", time.Time{}, ""
			}
			return head, info.ModTime(), branchOf(strings.TrimSpace(string(data)))
		}
		if d == filepath.Dir(d) {
			break
		}
	}
	return "", time.Time{}, ""
}

// branchOf is the branch a HEAD names, or a detached HEAD's short hash.
func branchOf(head string) string {
	if ref, ok := strings.CutPrefix(head, "ref:"); ok {
		ref = strings.TrimSpace(ref)
		return strings.TrimPrefix(ref, "refs/heads/")
	}
	if len(head) >= 7 {
		return head[:7]
	}
	return head
}

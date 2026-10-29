// paneinfo_test.go
// Author: Kennt Kim
// Created: 2026-10-20 · Last updated: 2026-10-29
//
// Purpose: what runs in a terminal, read through Linux's /proc reader from a /proc made for the
// test (paneinfo.go, procfs.go) — so it runs on any machine: a terminal's processes by session, a
// detached `nohup` job among them and another terminal's left out; the program in front — the
// foreground group's leader, a member when the leader is gone — as its command line, cut when long,
// and a shell by its name alone (`-bash --posix` is "bash"); a command name holding spaces and
// parentheses; the folder — the program's own, spelled as the shell reported it when the same, the
// program's when the report is stale; a shell that ended or is a zombie; ssh's host. And the branch
// (branch.go): a repository found upward, a worktree's `gitdir:` followed, a detached head, the
// cache kept until HEAD changes and bounded, mountinfo's types, escapes and stacked mounts, a
// network share read off the caller once a minute.
//
// Notes:
// - Taken from SiliconDesk's host/internal/paneinfo/paneinfo_test.go on 2026-10-23, and made to
//   read through Reader.Read. The network share's read is awaited (branches.reads), where
//   SiliconDesk's test polled against a deadline.

package paneinfo

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeProc makes /proc entries: pid → stat after the pid, command line, working directory.
func fakeProc(t *testing.T, procs map[string][3]string) string {
	t.Helper()
	root := t.TempDir()
	for pid, p := range procs {
		dir := filepath.Join(root, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "stat"), []byte(pid+" "+p[0]), 0o644)
		os.WriteFile(filepath.Join(dir, "cmdline"), []byte(p[1]), 0o644)
		if p[2] != "" {
			os.Symlink(p[2], filepath.Join(dir, "cwd"))
		}
	}
	return root
}

// procReader is a Reader of the /proc at root.
func procReader(root string) *Reader {
	return &Reader{sys: procfs{root: root}, branches: newBranches()}
}

func TestTheProgramInFront(t *testing.T) {
	home := t.TempDir()
	runs := filepath.Join(home, "runs")
	// Shell 100 (its own session); python 200 in front (group 200); a nohup job 300 of the session
	// reparented to init; another terminal's shell 900.
	root := fakeProc(t, map[string][3]string{
		"100": {"(bash) S 50 100 100 34816 200 0", "-bash\x00--posix\x00", home},
		"200": {"(python train.py) R 100 200 100 34816 200 0", "/usr/bin/python3\x00train.py\x00--epochs\x0010\x00", runs},
		"300": {"(weird (name)) S 1 300 100 0 -1 0", "python\x00main.py\x00", "/srv"},
		"900": {"(zsh) S 1 900 900 34817 900 0", "/bin/zsh\x00", home},
	})
	procs, err := procfs{root: root}.session(100)
	if err != nil {
		t.Fatal(err)
	}
	s := newSnapshot(procs)
	if len(s.pids) != 3 || s.pids[0] != 100 || s.pids[2] != 300 || s.procs[300].comm != "weird (name)" {
		t.Fatalf("session %v %+v", s.pids, s.procs)
	}
	r := procReader(root)
	info, err := r.Read(100, "")
	if err != nil || info.ProgramPID != 200 || info.Program != "python3 train.py --epochs 10" || info.Directory != runs {
		t.Fatalf("front %+v %v", info, err)
	}
	// The folder is where the program in front is: a folder the shell reported before it started
	// (`cd runs && python train.py`, reported at the next prompt) is stale (2026-10-29).
	if info, _ := r.Read(100, home); info.Directory != runs {
		t.Fatalf("stale report %+v", info)
	}
	// The same folder reached through a link is spelled as the shell reported it.
	link := filepath.Join(t.TempDir(), "runs-link")
	if err := os.Symlink(runs, link); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	if info, _ := r.Read(100, link); info.Directory != link || info.ProgramPID != 200 {
		t.Fatalf("reported spelling %+v", info)
	}
	// A program whose folder cannot be read (another user's): the folder the shell reported.
	if got := where(home, "", nil); got != home {
		t.Fatalf("unreadable %q", got)
	}
	// On a network share the two are not compared (a stat could block): the kernel's folder.
	share := mounts{{point: "/", network: false}, {point: "/net/nas", network: true}}
	if got := where("/net/nas/link", "/net/nas/runs", share); got != "/net/nas/runs" {
		t.Fatalf("share %q", got)
	}
	// At its prompt: the shell, a login shell's '-' dropped.
	if info, _ := r.Read(900, ""); info.ProgramPID != 900 || info.Program != "zsh" || info.Directory != home {
		t.Fatalf("prompt %+v", info)
	}
	// A shell is named alone: not the arguments the keeper started it with.
	args := []string{"-bash", "--posix"}
	if name(args, "bash") != "bash" || program(args, "bash") != "bash --posix" {
		t.Fatalf("login shell %q %q", name(args, "bash"), program(args, "bash"))
	}
	// A command line that cannot be read: the kernel's name.
	if name(nil, "sudo") != "sudo" || program(nil, "sudo") != "sudo" || program(nil, "") != "" {
		t.Fatal("kernel name")
	}
	// A command installed as a script is shown as that command, not its interpreter and its path; a
	// script run from a folder of the user's is shown as typed (2026-10-29).
	for args, want := range map[string]string{
		"node /Users/k/.nvm/versions/node/v20/bin/codex review": "codex review",
		"/usr/bin/python3.12 /usr/local/bin/jupyter lab":        "jupyter lab",
		"python3 train.py --epochs 3":                           "python3 train.py --epochs 3",
		"node server.js":                                        "node server.js",
		"node":                                                  "node",
	} {
		if got := program(strings.Fields(args), ""); got != want {
			t.Errorf("program(%q) = %q, want %q", args, got, want)
		}
	}
	// A long command line is cut.
	long := program([]string{"python3", strings.Repeat("é", 200)}, "")
	if r := []rune(long); len(r) != maxProgram || r[maxProgram-1] != '…' {
		t.Fatalf("cut %d %q", len(r), long)
	}

	// The group's leader exited: a member of the group.
	root = fakeProc(t, map[string][3]string{
		"100": {"(bash) S 50 100 100 34816 400 0", "bash\x00", "/home/u"},
		"401": {"(make) S 1 400 100 34816 400 0", "make\x00-j8\x00", "/src"},
	})
	if info, _ := procReader(root).Read(100, ""); info.ProgramPID != 401 || info.Program != "make -j8" {
		t.Fatalf("member %+v", info)
	}
	// A shell that is not there, or a zombie: it ended.
	root = fakeProc(t, map[string][3]string{
		"100": {"(bash) Z 50 100 100 34816 100 0", "", ""},
	})
	for _, pid := range []int{12345, 100} {
		if _, err := procReader(root).Read(pid, ""); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("shell %d: %v", pid, err)
		}
	}
}

func TestTheHostOfSSH(t *testing.T) {
	for args, want := range map[string]string{
		"nas":                           "nas",
		"user@nas":                      "nas",
		"-p 2222 nas":                   "nas",
		"-p2222 -l root nas uptime":     "nas",
		"-vt -i /k/id gpu":              "gpu",
		"-L 8888:localhost:8888 gpu":    "gpu",
		"-o ServerAliveInterval=30 nas": "nas",
		"ssh://root@box.lan:2200/":      "box.lan",
		"-- -weird":                     "-weird",
		"-p":                            "",
	} {
		if got := sshDestination(strings.Fields(args)); got != want {
			t.Errorf("ssh %s: %q, want %q", args, got, want)
		}
	}
	root := fakeProc(t, map[string][3]string{
		"100": {"(bash) S 50 100 100 34816 200 0", "-bash\x00", ""},
		"200": {"(ssh) S 100 200 100 34816 200 0", "/usr/bin/ssh\x00-p\x002222\x00user@nas\x00", ""},
	})
	if info, _ := procReader(root).Read(100, ""); info.Remote != "nas" || info.Program != "ssh -p 2222 user@nas" {
		t.Fatalf("remote %+v", info)
	}
	if remote([]string{"-bash"}) != "" {
		t.Fatal("a shell is not remote")
	}
}

func TestInfoEqual(t *testing.T) {
	a := Info{Program: "python3", Ports: []Port{{Port: 8188, Process: "python3"}}}
	b := a
	b.Ports = []Port{{Port: 8188, Process: "python3"}}
	if !a.Equal(b) {
		t.Fatal("same")
	}
	b.Ports[0].Everywhere = true
	if a.Equal(b) {
		t.Fatal("a port's binding changed")
	}
}

func TestTheBranch(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "proj")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo, "src", "deep"), 0o755)
	head := filepath.Join(repo, ".git", "HEAD")
	os.WriteFile(head, []byte("ref: refs/heads/main\n"), 0o644)
	b := newBranches()
	if got := b.branch(filepath.Join(repo, "src", "deep"), nil); got != "main" {
		t.Fatalf("branch %q", got)
	}
	// Kept until HEAD changes.
	os.WriteFile(head, []byte("ref: refs/heads/feature/x\n"), 0o644)
	later := time.Now().Add(time.Minute)
	os.Chtimes(head, later, later)
	if got := b.branch(filepath.Join(repo, "src", "deep"), nil); got != "feature/x" {
		t.Fatalf("after a checkout %q", got)
	}
	// A worktree: `.git` is a file naming the real git directory; a detached head.
	wt := filepath.Join(home, "wt")
	gitdir := filepath.Join(repo, ".git", "worktrees", "wt")
	os.MkdirAll(gitdir, 0o755)
	os.MkdirAll(wt, 0o755)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644)
	os.WriteFile(filepath.Join(gitdir, "HEAD"), []byte("0123456789abcdef0123456789abcdef01234567\n"), 0o644)
	if got := b.branch(wt, nil); got != "0123456" {
		t.Fatalf("worktree %q", got)
	}
	if got := b.branch(t.TempDir(), nil); got != "" {
		t.Fatalf("no repository %q", got)
	}
	if got := b.branch("relative/dir", nil); got != "" {
		t.Fatalf("relative %q", got)
	}
}

func TestTheBranchesKeptAreBounded(t *testing.T) {
	b := newBranches()
	now := time.Now()
	b.now = func() time.Time { return now }
	root := t.TempDir()
	// A read in flight on a share is never forgotten: a second could start behind a stuck one.
	b.entries["/share"] = &branchEntry{reading: true}
	for i := range maxEntries + 10 {
		now = now.Add(time.Second)
		b.branch(filepath.Join(root, strings.Repeat("d", i+1)), nil)
	}
	if len(b.entries) != maxEntries || b.entries["/share"] == nil {
		t.Fatalf("%d entries, share kept %v", len(b.entries), b.entries["/share"] != nil)
	}
	// The ones asked for last are kept.
	if b.entries[filepath.Join(root, strings.Repeat("d", maxEntries+10))] == nil ||
		b.entries[filepath.Join(root, "d")] != nil {
		t.Fatal("kept the wrong ones")
	}
}

func TestBranchesOnNetworkShares(t *testing.T) {
	share := t.TempDir()
	os.MkdirAll(filepath.Join(share, ".git"), 0o755)
	os.WriteFile(filepath.Join(share, ".git", "HEAD"), []byte("ref: refs/heads/nas\n"), 0o644)
	info := filepath.Join(t.TempDir(), "mountinfo")
	os.WriteFile(info, []byte(
		"22 1 8:1 / / rw - ext4 /dev/sda1 rw\n"+
			"40 22 0:50 / "+share+" rw - nfs4 nas:/home rw\n"+
			"41 22 0:51 / /mnt/my\\040disk rw - ext4 /dev/sdb1 rw\n"+
			// systemd's automount: autofs, then the share mounted over it; and a share hidden by a
			// local file system mounted over it later.
			"50 22 0:60 / /mnt/auto rw - autofs systemd-1 rw\n"+
			"51 50 0:61 / /mnt/auto rw - nfs4 nas:/auto rw\n"+
			"60 22 0:62 / /mnt/over rw - cifs //nas/x rw\n"+
			"61 60 0:63 / /mnt/over rw - tmpfs tmpfs rw\n"), 0o644)
	mounts := readMountinfo(info)
	if !mounts.remote(filepath.Join(share, "x")) || mounts.remote("/home/u") || mounts.remote("/mnt/my disk/a") ||
		!mounts.remote("/mnt/auto/a") || mounts.remote("/mnt/over/a") || mounts.remote("/mnt/overt") {
		t.Fatalf("mounts %+v", mounts)
	}
	b := newBranches()
	now := time.Now()
	b.now = func() time.Time { return now }
	// The first answer is not known yet: the read goes off the caller.
	if got := b.branch(share, mounts); got != "" {
		t.Fatalf("at once %q", got)
	}
	b.reads.Wait()
	if got := b.branch(share, mounts); got != "nas" {
		t.Fatalf("after the read %q", got)
	}
	// Not read again within the minute.
	os.WriteFile(filepath.Join(share, ".git", "HEAD"), []byte("ref: refs/heads/other\n"), 0o644)
	now = now.Add(30 * time.Second)
	if got := b.branch(share, mounts); got != "nas" || b.entries[share].reading {
		t.Fatalf("read again too soon: %q", got)
	}
	// Read again after it.
	now = now.Add(31 * time.Second)
	b.branch(share, mounts)
	b.reads.Wait()
	if got := b.branch(share, mounts); got != "other" {
		t.Fatalf("a minute later %q", got)
	}
}

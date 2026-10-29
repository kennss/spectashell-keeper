// paneinfo.go
// Author: Kennt Kim
// Created: 2026-10-20 · Last updated: 2026-10-29
//
// Purpose: what runs in a terminal (Docs/DESIGN.md §5.1, §5.3) — the program in front and its
// process id, its folder, the git branch, the ports the terminal's processes listen on, the host
// when the program is ssh, and the command that resumes a coding agent running in front. A
// terminal's keeper reads it for its own shell every few seconds (Reader.Read), on the machine the
// shell runs on, and sends it to the app when it changed.
//
// Notes:
// - Taken from SiliconDesk's host/internal/paneinfo/paneinfo.go on 2026-10-23. There one host
//   session read /proc once for every terminal (Snapshot); here each keeper reads its own shell's
//   session, through the system of the machine (system.go): Linux's /proc (procfs.go), macOS's
//   sysctl and libproc (system_darwin.go, libproc_darwin.go). The rules below are SiliconDesk's.
// - A terminal's processes are its session: the keeper starts the shell with setsid and the
//   pseudo-terminal as its controlling terminal, so the shell leads a session of its own and every
//   process started from the terminal is in it — `nohup python main.py &` too, after the subshell
//   that started it ended and it was reparented to init, which a tree of parent ids would lose.
// - The program in front is the leader of the terminal's foreground process group (the shell's
//   tpgid), as its command line (argv[0]'s base name, then the arguments, cut to maxProgram); when
//   the leader has exited while its group runs on, the lowest member of the group. The shell itself
//   at its prompt is its name alone (name).
// - The folder is where the program in front is, as the kernel has it, in the shell's spelling when
//   that is the same folder (where): the shell reports at its prompt, so its report is stale while
//   a program started on the line that changed folder runs.
// - Anything that cannot be read (a process of another user, run with sudo; macOS's libproc in a
//   build without cgo) is left empty. Read fails only when the shell is not found: it ended (a
//   zombie not yet reaped counts as ended).
// - A Reader keeps the branches it read (branch.go) and is used from one goroutine at a time.

package paneinfo

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// maxProgram bounds a program's command line, in characters.
const maxProgram = 120

// Port is a TCP port a terminal's processes listen on.
type Port struct {
	Port       uint16
	Everywhere bool   // bound to every address (other machines reach it), else loopback only
	Process    string // the program holding it ("python3")
}

// Info is what runs in one terminal.
type Info struct {
	Program    string // the program in front as its command line ("claude", "python train.py", "ssh nas"); the shell's name at its prompt
	ProgramPID int    // its process id; the shell's at its prompt
	Directory  string // the program in front's folder (the shell's at its prompt), spelled as the shell reported it when the same (where)
	Branch     string // the git branch of Directory; "" for none
	Ports      []Port // lowest first
	Remote     string // the destination host when the program in front is ssh, without a user; "" otherwise
	Resume     string // the shell command that resumes the coding agent (Claude Code, Codex) running in front, "" for none
}

// Equal says whether two Infos say the same (a keeper sends Info only when it changed).
func (i Info) Equal(o Info) bool {
	return i.Program == o.Program && i.ProgramPID == o.ProgramPID && i.Directory == o.Directory &&
		i.Branch == o.Branch && i.Remote == o.Remote && i.Resume == o.Resume && slices.Equal(i.Ports, o.Ports)
}

// Reader reads what runs in terminals; it keeps what it can between reads (branches per folder).
type Reader struct {
	sys      system
	branches *branches
}

// NewReader makes a Reader for this machine.
func NewReader() *Reader {
	return &Reader{sys: newSystem(), branches: newBranches()}
}

// Read is what runs in the terminal whose shell is shellPID (the session leader the keeper started
// on the pty). reportedDir is the folder the shell last reported (OSC 7), "" when it reported none:
// it spells the folder when it is still where the terminal is (where).
func (r *Reader) Read(shellPID int, reportedDir string) (Info, error) {
	procs, err := r.sys.session(shellPID)
	if err != nil {
		return Info{}, err
	}
	s := newSnapshot(procs)
	front := s.foreground(shellPID)
	args := r.sys.commandLine(front)
	info := Info{ProgramPID: front, Directory: reportedDir, Remote: remote(args)}
	if front == shellPID {
		// At its prompt: the shell's name, not the arguments the keeper started it with
		// (`-bash --posix` for shell integration, which is not in POSIX mode once it has started).
		info.Program = name(args, s.procs[front].comm)
	} else {
		info.Program = program(args, s.procs[front].comm)
	}
	mounts := r.sys.mounts()
	info.Directory = where(reportedDir, r.sys.directory(front), mounts)
	if info.Directory != "" {
		info.Branch = r.branches.branch(info.Directory, mounts)
	}
	info.Ports = r.sys.ports(s)
	if info.Remote == "" {
		info.Resume = resumeCommand(r.sys, s, front)
	}
	return info, nil
}

// where is the terminal's folder: the one the program in front is in (the shell's at its prompt),
// `actual` as the kernel has it, spelled as the shell reported it (OSC 7, `reported`) when that
// names the same folder — a link's name, /tmp for /private/tmp, a project reached through a link.
// The report alone went stale while a program started on the line that changed folder ran (`cd
// ~/x && make`: the shell reports at its next prompt), so the desk and the status line named the
// folder before (2026-10-29). A folder that cannot be read (another user's process, run with sudo)
// leaves the report. On a network share the two are not compared, as a stat could block on a dead
// mount (branch.go), and the kernel's folder is taken.
func where(reported, actual string, m mounts) string {
	switch {
	case actual == "":
		return reported
	case reported == "" || reported == actual:
		return actual
	case m.remote(reported) || m.remote(actual):
		return actual
	}
	a, err := os.Stat(reported)
	if err != nil {
		return actual
	}
	b, err := os.Stat(actual)
	if err != nil || !os.SameFile(a, b) {
		return actual
	}
	return reported
}

// process is what the process table says of one process of a session.
type process struct {
	pgrp  int    // its process group
	tpgid int    // the foreground process group of its controlling terminal; 0 or less for none
	comm  string // the name the kernel keeps (Linux's comm, 15 bytes; macOS's p_comm, 16)
}

// snapshot is one read of a shell's session.
type snapshot struct {
	procs map[int]process // the session's processes, the shell among them
	pids  []int           // the same, lowest first
}

func newSnapshot(procs map[int]process) *snapshot {
	s := &snapshot{procs: procs, pids: make([]int, 0, len(procs))}
	for pid := range procs {
		s.pids = append(s.pids, pid)
	}
	slices.Sort(s.pids)
	return s
}

// group is the session's processes of process group pgid, lowest first: a program in front and
// what it started in its group (`ollama launch claude` and its claude).
func (s *snapshot) group(pgid int) []int {
	var out []int
	for _, pid := range s.pids {
		if s.procs[pid].pgrp == pgid {
			out = append(out, pid)
		}
	}
	return out
}

// foreground is the process in front of the terminal whose shell is shellPID (in the snapshot): its
// foreground group's leader, or the group's lowest member when the leader has exited; the shell
// itself at its prompt.
func (s *snapshot) foreground(shellPID int) int {
	group := s.procs[shellPID].tpgid
	if group <= 0 {
		return shellPID
	}
	if _, ok := s.procs[group]; ok {
		return group
	}
	if members := s.group(group); len(members) > 0 {
		return members[0]
	}
	return shellPID
}

// displayArgs is a command line as shown: argv[0] as its base name without a login shell's '-';
// the kernel's name for the process alone when the command line cannot be read (a process of
// another user, a zombie).
//
// A command installed as a script — npm's and pip's commands are scripts in a bin folder, run by
// their interpreter (`node …/bin/codex review`) — is shown as that command (`codex review`): the
// interpreter and the path to its bin folder say nothing of what runs, and a tab named
// "node /Users/sam/.nvm/versions/…" hid that it was Codex (2026-10-29). A script the user runs
// from a folder of their own (`python3 train.py`) is shown as typed.
func displayArgs(args []string, comm string) []string {
	if len(args) == 0 {
		if comm == "" {
			return nil
		}
		return []string{comm}
	}
	out := slices.Clone(args)
	out[0] = strings.TrimPrefix(filepath.Base(out[0]), "-")
	if len(out) > 1 && interpreters.MatchString(out[0]) && filepath.Base(filepath.Dir(out[1])) == "bin" {
		out = out[1:]
		out[0] = filepath.Base(out[0])
	}
	return out
}

// interpreters are the programs that run an installed command's script: Node's, Bun's, Python's,
// Ruby's and Perl's ("python3.12" too).
var interpreters = regexp.MustCompile(`^(node|nodejs|bun|python[0-9.]*|ruby|perl)$`)

// program is a command line as shown: argv[0]'s base name (a login shell's '-' dropped), then the
// arguments, cut to maxProgram characters.
func program(args []string, comm string) string {
	line := strings.Join(displayArgs(args, comm), " ")
	if r := []rune(line); len(r) > maxProgram {
		line = string(r[:maxProgram-1]) + "…"
	}
	return line
}

// name is a program's name alone: argv[0]'s base name, a login shell's '-' dropped ("bash" for
// `-bash --posix`). A terminal's shell at its prompt is shown by it: the arguments the keeper
// started it with (shell integration's `--posix`, which its script turns off at once) are not the
// user's and would mislead.
func name(args []string, comm string) string {
	if d := displayArgs(args, comm); len(d) > 0 {
		return d[0]
	}
	return ""
}

// remote is the host a command line logs in to when it is `ssh` (its destination, without a user),
// else "".
func remote(args []string) string {
	if len(args) == 0 || filepath.Base(args[0]) != "ssh" {
		return ""
	}
	return sshDestination(args[1:])
}

// sshValueOptions are ssh's options that take a value (OpenSSH 9: `-B -b -c -D -E -e -F -I -i -J -L
// -l -m -O -o -P -p -Q -R -S -W -w`), skipped with it when looking for the destination.
const sshValueOptions = "BbcDEeFIiJLlmOoPpQRSWw"

// sshDestination is the host of ssh's arguments: the first operand, `host`, `user@host` or
// `ssh://[user@]host[:port]`, without the user — so `ssh -p 2222 nas` is "nas", not "2222".
func sshDestination(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 < len(args) {
				return hostOf(args[i+1])
			}
			return ""
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			return hostOf(a)
		}
		// A cluster of flags (`-vt`); the first that takes a value takes the rest of the word, or the
		// next word.
		for j := 1; j < len(a); j++ {
			if strings.IndexByte(sshValueOptions, a[j]) >= 0 {
				if j == len(a)-1 {
					i++
				}
				break
			}
		}
	}
	return ""
}

// hostOf is a destination's host.
func hostOf(dest string) string {
	if rest, ok := strings.CutPrefix(dest, "ssh://"); ok {
		dest = rest
		if i := strings.IndexByte(dest, '/'); i >= 0 {
			dest = dest[:i]
		}
		if i := strings.LastIndexByte(dest, ':'); i >= 0 && !strings.Contains(dest[i:], "]") {
			dest = dest[:i]
		}
	}
	if i := strings.LastIndexByte(dest, '@'); i >= 0 {
		dest = dest[i+1:]
	}
	return strings.Trim(dest, "[]")
}

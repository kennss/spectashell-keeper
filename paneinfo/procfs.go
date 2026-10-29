// procfs.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-23
//
// Purpose: Linux's processes read from /proc (system.go): a shell's session from one pass over
// /proc/<pid>/stat, and a process's command line, environment, folder and open files from its own
// /proc entries. The ports are in procfs_ports.go, the mounts in branch.go.
//
// Notes:
// - Built on every system, used on Linux (system_linux.go): it only reads files under its root, so
//   the tests run it against a /proc made for them on any machine.
// - The rules are SiliconDesk's (host/internal/paneinfo/paneinfo.go, apps/terminal/restart.go):
//   /proc/<pid>/stat is parsed after its last ')', since a command name may hold spaces and
//   parentheses; the command name is what lies between the first '(' and that ')'.
// - A session is found by its id in each stat — every process must be looked at, since nothing
//   lists a session's members (a tree of parent ids loses `nohup … &` once reparented to init).
//   Reading every stat of a few hundred processes takes a few milliseconds (SiliconDesk measured
//   16.7 ms for 376 processes in Python, 2026-10-19).
// - The folder is /proc/<pid>/cwd's link: no file system is read. Only the user's own processes
//   can be read beyond their stat; another user's (sudo's) are left empty.

package paneinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procfs reads the processes under root ("/proc").
type procfs struct{ root string }

// statFields is what paneinfo takes from a stat line.
type statFields struct {
	state         byte
	pgrp, session int
	tpgid         int
	comm          string
}

// parseStat reads a /proc/<pid>/stat line: `pid (comm) state ppid pgrp session tty_nr tpgid …`.
func parseStat(stat string) (statFields, bool) {
	open := strings.IndexByte(stat, '(')
	end := strings.LastIndexByte(stat, ')')
	if open < 0 || end < open {
		return statFields{}, false
	}
	f := strings.Fields(stat[end+1:])
	if len(f) < 6 || len(f[0]) != 1 {
		return statFields{}, false
	}
	pgrp, err1 := strconv.Atoi(f[2])
	session, err2 := strconv.Atoi(f[3])
	tpgid, err3 := strconv.Atoi(f[5])
	if err1 != nil || err2 != nil || err3 != nil {
		return statFields{}, false
	}
	return statFields{state: f[0][0], pgrp: pgrp, session: session, tpgid: tpgid, comm: stat[open+1 : end]}, true
}

// stat reads pid's stat.
func (p procfs) stat(pid string) (statFields, bool) {
	data, err := os.ReadFile(filepath.Join(p.root, pid, "stat"))
	if err != nil {
		return statFields{}, false // it ended meanwhile
	}
	return parseStat(string(data))
}

func (p procfs) session(shellPID int) (map[int]process, error) {
	shell, ok := p.stat(strconv.Itoa(shellPID))
	if !ok || shell.state == 'Z' || shell.state == 'X' {
		return nil, fmt.Errorf("paneinfo: shell %d: %w", shellPID, os.ErrNotExist)
	}
	entries, err := os.ReadDir(p.root)
	if err != nil {
		return nil, err
	}
	procs := map[int]process{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, ok := p.stat(e.Name())
		if !ok || st.session != shell.session {
			continue
		}
		procs[pid] = process{pgrp: st.pgrp, tpgid: st.tpgid, comm: st.comm}
	}
	// The shell may have ended between its own stat and the pass.
	if _, ok := procs[shellPID]; !ok {
		return nil, fmt.Errorf("paneinfo: shell %d: %w", shellPID, os.ErrNotExist)
	}
	return procs, nil
}

func (p procfs) commandLine(pid int) []string {
	data, err := os.ReadFile(filepath.Join(p.root, strconv.Itoa(pid), "cmdline"))
	if err != nil || len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
}

func (p procfs) environ(pid int) map[string]string {
	data, err := os.ReadFile(filepath.Join(p.root, strconv.Itoa(pid), "environ"))
	if err != nil {
		return nil
	}
	return parseEnviron(strings.Split(string(data), "\x00"))
}

// parseEnviron makes a map of `KEY=value` strings (the first of a key repeated wins, as getenv's).
func parseEnviron(kvs []string) map[string]string {
	out := map[string]string{}
	for _, kv := range kvs {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			if _, seen := out[k]; !seen {
				out[k] = v
			}
		}
	}
	return out
}

func (p procfs) directory(pid int) string {
	dir, err := os.Readlink(filepath.Join(p.root, strconv.Itoa(pid), "cwd"))
	if err != nil {
		return ""
	}
	return dir
}

// fdLinks is the targets of pid's open file descriptors (a path, `socket:[inode]`, `pipe:[…]`).
func (p procfs) fdLinks(pid int) []string {
	dir := filepath.Join(p.root, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if target, err := os.Readlink(filepath.Join(dir, e.Name())); err == nil {
			out = append(out, target)
		}
	}
	return out
}

func (p procfs) openFiles(pid int) []string {
	var out []string
	for _, target := range p.fdLinks(pid) {
		if strings.HasPrefix(target, "/") {
			out = append(out, target)
		}
	}
	return out
}

func (p procfs) mounts() mounts {
	return readMountinfo(filepath.Join(p.root, "self", "mountinfo"))
}

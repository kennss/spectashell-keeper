// system_darwin.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-23
//
// Purpose: macOS's system for paneinfo (system.go): a shell's session from sysctl's process table,
// a process's command line and environment from KERN_PROCARGS2, and the mounted file systems from
// getfsstat — in pure Go (golang.org/x/sys/unix) — and a process's folder, open files and
// listening sockets from libproc (libproc_darwin.go, which needs cgo; without it they are not
// known). With cgo the process table is read into a buffer kept between reads (processes).
//
// Notes:
// - kinfo_proc has no session id (its e_sess is zeroed: every process showed 0 on macOS 27,
//   2026-10-23), so a member is confirmed with getsid. The candidates are the processes whose
//   controlling terminal is the shell's (KERN_PROC_TTY: every process of a session that has a
//   terminal carries it, sudo's root children included) — tens, where KERN_PROC_ALL is over a
//   thousand (1302 here: 0.42 ms and 840 KB a read, and 0.32 ms more to getsid them all). A shell
//   without a terminal has every process looked at. KERN_PROC_SESSION is in the headers but the
//   kernel refuses it (ENOENT by name, an error by number, macOS 27).
// - Left out by KERN_PROC_TTY: a process of the session that gave up its terminal with TIOCNOTTY
//   without leaving the session (setsid, daemon(3), forkpty and Python's start_new_session all
//   leave it, so such a process is rare); the session's processes all carry the terminal otherwise.
// - KERN_PROCARGS2 is argc, the executable's path and its padding, argv, then the environment the
//   process started with, ended by an empty string (Apple's own strings follow). Only the user's own
//   processes can be read (a root one gives EINVAL), so a program run with sudo shows its kernel
//   name alone.
// - A network share for the branch (branch.go) is a file system without MNT_LOCAL (smbfs, nfs,
//   afpfs, webdav, macFUSE's sshfs) or autofs (/home's auto_home, /Network/Servers), from
//   getfsstat with MNT_NOWAIT — the kernel's last answer, never a round trip to a server that may
//   be dead (statfs on the directory could block on a hard-mounted share). Mounts of the data
//   volume (/System/Volumes/Data/home) are also seen at their firmlinked paths (/home is a link to
//   it, so a shell's own $PWD names it so).

//go:build darwin

package paneinfo

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	nodev = -1 // e_tdev of a process without a controlling terminal
	sZomb = 5  // p_stat of a zombie (sys/proc.h's SZOMB)
	// dataVolume is where the data volume is mounted; its folders are firmlinked at the root.
	dataVolume = "/System/Volumes/Data"
)

func newSystem() system { return &darwin{} }

// darwin reads macOS's processes. It keeps its buffers for sysctl and getfsstat between reads.
type darwin struct {
	procbuf []byte // kinfo_procs (libproc_darwin.go)
	fsbuf   []unix.Statfs_t
}

func (d *darwin) session(shellPID int) (map[int]process, error) {
	notFound := fmt.Errorf("paneinfo: shell %d: %w", shellPID, os.ErrNotExist)
	shell, err := unix.SysctlKinfoProc("kern.proc.pid", shellPID)
	if err != nil || int(shell.Proc.P_pid) != shellPID || shell.Proc.P_stat == sZomb {
		return nil, notFound
	}
	sid, err := unix.Getsid(shellPID)
	if err != nil {
		return nil, notFound
	}
	candidates, err := d.processes(shell.Eproc.Tdev)
	if err != nil {
		return nil, err
	}
	procs := map[int]process{}
	for i := range candidates {
		k := &candidates[i]
		pid := int(k.Proc.P_pid)
		if s, err := unix.Getsid(pid); err != nil || s != sid {
			continue
		}
		procs[pid] = process{pgrp: int(k.Eproc.Pgid), tpgid: int(k.Eproc.Tpgid),
			comm: unix.ByteSliceToString(k.Proc.P_comm[:])}
	}
	if _, ok := procs[shellPID]; !ok {
		return nil, notFound
	}
	return procs, nil
}

// procargs is pid's KERN_PROCARGS2, nil when it cannot be read.
func procargs(pid int) []byte {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil
	}
	return b
}

// parseProcargs splits a KERN_PROCARGS2 buffer into the command line and, when withEnv, the
// environment.
func parseProcargs(b []byte, withEnv bool) (args, env []string) {
	if len(b) < 4 {
		return nil, nil
	}
	argc := int(binary.LittleEndian.Uint32(b)) // arm64 and x86_64 are little-endian
	rest := b[4:]
	// The executable's path, then the NULs that pad it.
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return nil, nil
	}
	rest = rest[i:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	for len(args) < argc && len(rest) > 0 {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			args = append(args, string(rest))
			return args, nil
		}
		args = append(args, string(rest[:i]))
		rest = rest[i+1:]
	}
	for withEnv && len(rest) > 0 {
		i := bytes.IndexByte(rest, 0)
		if i <= 0 { // the empty string that ends it, or a cut-off buffer
			break
		}
		env = append(env, string(rest[:i]))
		rest = rest[i+1:]
	}
	return args, env
}

func (d *darwin) commandLine(pid int) []string {
	args, _ := parseProcargs(procargs(pid), false)
	return args
}

func (d *darwin) environ(pid int) map[string]string {
	b := procargs(pid)
	if b == nil {
		return nil
	}
	_, env := parseProcargs(b, true)
	return parseEnviron(env)
}

func (d *darwin) directory(pid int) string { return libprocDirectory(pid) }

func (d *darwin) openFiles(pid int) []string { return libprocOpenFiles(pid) }

func (d *darwin) ports(s *snapshot) []Port {
	set := portSet{}
	for _, pid := range s.pids {
		libprocListeners(pid, func(l listener) { set.add(l.ip, l.port, pid) })
	}
	return set.list(func(pid int) string { return name(d.commandLine(pid), s.procs[pid].comm) })
}

func (d *darwin) mounts() mounts {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil || n <= 0 {
		return nil
	}
	if len(d.fsbuf) < n+4 { // room for a few mounted meanwhile
		d.fsbuf = make([]unix.Statfs_t, n+4)
	}
	n, err = unix.Getfsstat(d.fsbuf, unix.MNT_NOWAIT)
	if err != nil {
		return nil
	}
	list := make([]mount, 0, n)
	for i := range d.fsbuf[:n] {
		fs := &d.fsbuf[i]
		list = append(list, darwinMounts(unix.ByteSliceToString(fs.Mntonname[:]),
			unix.ByteSliceToString(fs.Fstypename[:]), fs.Flags&unix.MNT_LOCAL != 0)...)
	}
	return newMounts(list)
}

// darwinMounts is one of getfsstat's file systems as mounts: at its point, and at its firmlinked
// path too when it is in the data volume.
func darwinMounts(point, fsType string, local bool) []mount {
	m := mount{point: point, network: !local || fsType == "autofs"}
	if rest, ok := strings.CutPrefix(point, dataVolume); ok && strings.HasPrefix(rest, "/") {
		return []mount{m, {point: rest, network: m.network}}
	}
	return []mount{m}
}

// listener is a TCP socket listening on ip:port.
type listener struct {
	ip   net.IP
	port uint16
}

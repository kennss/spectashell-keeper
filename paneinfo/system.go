// system.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-23
//
// Purpose: what paneinfo reads from the machine, the same on every system: a shell's session in
// the process table, a process's command line, environment, folder and open files, the ports a
// session's processes listen on, and the mounted file systems. Linux reads /proc (procfs.go),
// macOS sysctl and libproc (system_darwin.go); system_other.go knows nothing.
//
// Notes:
// - Every method but session answers "not known" (nil, "") for what it cannot read, so a process of
//   another user or one that ended meanwhile leaves its part of Info empty.
// - portSet gathers listening sockets the same way on both systems: one Port per port number,
//   Everywhere when any of the session's sockets on it is bound to every address, and the program
//   of the lowest process holding one (named by the system: Linux's comm as in SiliconDesk; on
//   macOS argv[0]'s base name, since p_comm names the file a link resolved to — "python3.12" for
//   `python3`, "claude.exe" for `claude`, seen 2026-10-23).

package paneinfo

import (
	"net"
	"slices"
)

// system reads one machine's processes.
type system interface {
	// session is the processes of shellPID's session, the shell among them; an error wrapping
	// os.ErrNotExist when the shell is not found (it ended).
	session(shellPID int) (map[int]process, error)
	// commandLine is pid's command line as it was given (argv[0] as typed), nil when not readable.
	commandLine(pid int) []string
	// environ is the environment pid started with, nil when not readable.
	environ(pid int) map[string]string
	// directory is pid's working directory, "" when not readable.
	directory(pid int) string
	// openFiles is the paths of the files pid holds open.
	openFiles(pid int) []string
	// ports is the TCP ports s's processes listen on reachable through loopback, lowest first.
	ports(s *snapshot) []Port
	// mounts is the mounted file systems, for telling a network share (branch.go).
	mounts() mounts
}

// portSet gathers the listening sockets of a session by port.
type portSet map[uint16]*heldPort

type heldPort struct {
	everywhere bool
	pid        int // the lowest process holding one of its sockets
}

// add records a socket listening on ip:port held by pid; one bound to another address than
// loopback or every address is left out (it is not reached through loopback).
func (set portSet) add(ip net.IP, port uint16, pid int) {
	if !(ip.IsLoopback() || ip.IsUnspecified()) {
		return
	}
	h := set[port]
	if h == nil {
		h = &heldPort{pid: pid}
		set[port] = h
	}
	h.everywhere = h.everywhere || ip.IsUnspecified()
	h.pid = min(h.pid, pid)
}

// list is the ports, lowest first, each with its holder's name.
func (set portSet) list(nameOf func(pid int) string) []Port {
	if len(set) == 0 {
		return nil
	}
	out := make([]Port, 0, len(set))
	for port, h := range set {
		out = append(out, Port{Port: port, Everywhere: h.everywhere, Process: nameOf(h.pid)})
	}
	slices.SortFunc(out, func(a, b Port) int { return int(a.Port) - int(b.Port) })
	return out
}

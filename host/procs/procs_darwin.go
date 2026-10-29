// procs_darwin.go
// Author: Kennt Kim
// Created: 2026-10-25 · Last updated: 2026-10-29
//
// Purpose: this user's processes and the program each runs, on macOS. The core tells this Mac's
// own keepers from those a connection to it started by their program (bus/keepers.go), and the
// keeper program names the builds whose programs run, so the installer keeps their folders
// (hostkeeper.BuildsInUse, Docs/DESIGN.md §5.3).
//
// Notes:
// - Moved here from core/internal/bus (executable_darwin.go, 2026-10-25) on 2026-10-26, when the
//   keeper program needed it too.
// - kern.procargs2 is argc (4 bytes), then the executable's path as it was run, NUL-terminated,
//   then the arguments; a process of another user, or one gone, cannot be read and gives "".
// - kern.proc.uid lists the processes of an effective user.
// - Moved here from core/internal/procs on 2026-10-29, with the keeper program
//   (Docs/DESIGN.md §3.2).

package procs

import (
	"bytes"
	"os"

	"golang.org/x/sys/unix"
)

// Executable is the path of the program process pid runs; "" when it cannot be read.
func Executable(pid int) string {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 5 {
		return ""
	}
	path, _, _ := bytes.Cut(b[4:], []byte{0})
	return string(path)
}

// Mine is the ids of this user's processes; nil when they cannot be listed.
func Mine() []int {
	list, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Geteuid())
	if err != nil {
		return nil
	}
	pids := make([]int, 0, len(list))
	for _, p := range list {
		pids = append(pids, int(p.Proc.P_pid))
	}
	return pids
}

// procs_linux.go
// Author: Kennt Kim
// Created: 2026-10-25 · Last updated: 2026-10-30
//
// Purpose: this user's processes and the program each runs, on Linux (procs_darwin.go says what
// for): /proc/<pid>/exe and the owners of /proc's folders.
//
// Notes:
// - Moved here from core/internal/bus (executable_linux.go, 2026-10-25) on 2026-10-26.
// - A program removed while it runs reads as its path with " (deleted)" after it.
// - Moved here from core/internal/procs on 2026-10-29, with the keeper program
//   (Docs/design/01-DESIGN.md §3.2).

package procs

import (
	"os"
	"strconv"
	"syscall"
)

// Executable is the path of the program process pid runs; "" when it cannot be read.
func Executable(pid int) string {
	path, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return ""
	}
	return path
}

// Mine is the ids of this user's processes; nil when they cannot be listed.
func Mine() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	uid := uint32(os.Geteuid())
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // gone meanwhile
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid == uid {
			pids = append(pids, pid)
		}
	}
	return pids
}

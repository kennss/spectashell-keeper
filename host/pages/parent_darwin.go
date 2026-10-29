// parent_darwin.go
// Author: Kennt Kim
// Created: 2026-10-25 · Last updated: 2026-10-30
//
// Purpose: a process's parent on macOS, from the kern.proc.pid sysctl (shell.go follows them to a
// keeper's shell).
//
// Notes:
// - Moved here from core/internal/pages on 2026-10-29, with the keeper program
//   (Docs/design/01-DESIGN.md §3.2).

package pages

import "golang.org/x/sys/unix"

// Parent is pid's parent process; 0 when it cannot be read.
func Parent(pid int) int {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info.Proc.P_pid != int32(pid) {
		return 0
	}
	return int(info.Eproc.Ppid)
}

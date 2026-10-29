// parent_linux.go
// Author: Kennt Kim
// Created: 2026-10-25 · Last updated: 2026-10-29
//
// Purpose: a process's parent on Linux, from /proc/<pid>/stat (shell.go follows them to a
// keeper's shell).
//
// Notes:
// - The command's name, in parentheses, may hold spaces and parentheses itself, so the fields are
//   read after the last ")": state, then the parent.
// - Moved here from core/internal/pages on 2026-10-29, with the keeper program
//   (Docs/DESIGN.md §3.2).

package pages

import (
	"os"
	"strconv"
	"strings"
)

// Parent is pid's parent process; 0 when it cannot be read.
func Parent(pid int) int {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	return parentOfStat(string(b))
}

// parentOfStat reads the parent from a /proc/<pid>/stat line.
func parentOfStat(stat string) int {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 2 {
		return 0
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0
	}
	return ppid
}

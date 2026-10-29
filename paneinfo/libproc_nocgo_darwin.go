// libproc_nocgo_darwin.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-23
//
// Purpose: macOS without cgo: libproc's C functions cannot be called, so a process's folder, its
// open files and its listening sockets are not known (libproc_darwin.go reads them with cgo). The
// rest of what runs in a terminal — the program, its process id, the host of ssh, Claude Code's
// resume command and the branch of the folder the shell reports — still is. The process table is
// read through x/sys/unix, which allocates for every process at each read (libproc_darwin.go).
//
// Notes:
// - Also iOS (GOOS=ios satisfies the darwin tag): its SDK has no libproc, and no shell runs on an
//   iPhone or iPad, so nothing here is ever asked there; the core links this package only through
//   the keeper's.

//go:build darwin && (!cgo || ios)

package paneinfo

import "golang.org/x/sys/unix"

// readsProcessFiles says whether this build reads a process's folder, open files and sockets.
const readsProcessFiles = false

// processes is the processes whose controlling terminal is tdev, or every process for nodev.
func (d *darwin) processes(tdev int32) ([]unix.KinfoProc, error) {
	if tdev != nodev {
		return unix.SysctlKinfoProcSlice("kern.proc.tty", int(tdev))
	}
	return unix.SysctlKinfoProcSlice("kern.proc.all")
}

func libprocDirectory(int) string          { return "" }
func libprocOpenFiles(int) []string        { return nil }
func libprocListeners(int, func(listener)) {}

// spawn_unix.go
// Author: Kennt Kim
// Created: 2026-11-07 · Last updated: 2026-11-07
//
// Purpose: starting a keeper where nothing decides who answers for it (spawn, for Start): Linux,
// and a Mac build without cgo, in a session of its own (Setsid).
//
// Notes:
// - A Mac keeper is built with cgo, and spawn_darwin.go disclaims responsibility there; a Mac
//   build without cgo (go vet, a test without a C compiler) starts it as before.

//go:build unix && !(darwin && cgo && !ios)

package keeper

import (
	"os"
	"os/exec"
	"syscall"
)

// spawn starts argv with in, out and errOut as its stdin, stdout and stderr, in a session of its
// own. The caller closes its copies of the three.
func spawn(argv []string, in, out, errOut *os.File) (*os.Process, error) {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, err
	}
	return os.StartProcess(path, argv, &os.ProcAttr{
		Env:   os.Environ(),
		Files: []*os.File{in, out, errOut},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
}

// spawn_darwin.go
// Author: Kennt Kim
// Created: 2026-11-07 · Last updated: 2026-11-07
//
// Purpose: starting a keeper on a Mac (spawn, for Start) responsible for itself: posix_spawn with
// responsibility_spawnattrs_setdisclaim, as iTerm2 and VS Code start their shells, so the system's
// privacy checks (TCC) judge what the keeper's shell opens by the keeper's own identity, not by
// whoever started it.
//
// Notes:
// - Why (2026-11-07, the Mac reached over SSH from itself): a keeper started by `spectashell-keeper
//   start` over SSH had sshd-session as its responsible process. While that connection lived, its
//   shell read ~/Desktop; once the connection ended (the app relaunched, a reconnect), tccd judged
//   the access by a responsible process that was gone, logged "Platform binary prompting is 'Deny'
//   because: is Platform Binary", and `ls ~/Desktop` said "Operation not permitted" for the rest of
//   the shell's life. A keeper outlives the connection that started it by design, so it answers for
//   itself; disclaimed, macOS asks once for "SpectaShell Keeper", whose grant is kept by its bundle
//   id across builds and paths (02-CONNECTIONS.md §1 of SpectaShell has the experiments).
// - responsibility_spawnattrs_setdisclaim is private to libSystem; it is looked up with dlsym, so a
//   macOS without it still starts the keeper, not disclaimed, rather than failing to load the
//   program. responsibility_get_pid_responsible_for_pid, its companion, says whom a process answers
//   to (responsibleFor; the tests check the keeper is its own).
// - posix_spawn, unlike os.StartProcess, gives the attribute: the child gets its own session
//   (POSIX_SPAWN_SETSID, as Setsid did), only stdin, stdout and stderr (POSIX_SPAWN_CLOEXEC_DEFAULT
//   with dup2s) and an empty signal mask (a Go thread may have signals blocked; the keeper must
//   not inherit that). Handlers the runtime installed reset to the default on exec, as ever.
// - Built with cgo (the Mac keeper and SDCore are); without cgo spawn_unix.go starts the keeper as
//   before, not disclaimed.

//go:build darwin && cgo && !ios

package keeper

/*
#include <dlfcn.h>
#include <errno.h>
#include <signal.h>
#include <spawn.h>
#include <stdlib.h>
#include <unistd.h>

#ifndef POSIX_SPAWN_SETSID
#define POSIX_SPAWN_SETSID 0x0400
#endif

typedef int (*sd_disclaim_fn)(posix_spawnattr_t *, int);
typedef pid_t (*sd_responsible_fn)(pid_t);

// sd_spawn starts path with argv and envp, its stdin, stdout and stderr the descriptors given,
// in a session of its own and, when the system has the call, responsible for itself. It returns
// 0 or an errno; *disclaimed says whether responsibility was disclaimed.
static int sd_spawn(const char *path, char *const argv[], char *const envp[], int in, int out, int err,
                    pid_t *pid, int *disclaimed) {
	posix_spawnattr_t attr;
	posix_spawn_file_actions_t files;
	int rc = posix_spawnattr_init(&attr);
	if (rc != 0) return rc;
	rc = posix_spawn_file_actions_init(&files);
	if (rc != 0) { posix_spawnattr_destroy(&attr); return rc; }
	sigset_t none;
	sigemptyset(&none);
	posix_spawnattr_setsigmask(&attr, &none);
	rc = posix_spawnattr_setflags(&attr, POSIX_SPAWN_SETSID | POSIX_SPAWN_CLOEXEC_DEFAULT | POSIX_SPAWN_SETSIGMASK);
	if (rc == 0) rc = posix_spawn_file_actions_adddup2(&files, in, 0);
	if (rc == 0) rc = posix_spawn_file_actions_adddup2(&files, out, 1);
	if (rc == 0) rc = posix_spawn_file_actions_adddup2(&files, err, 2);
	*disclaimed = 0;
	sd_disclaim_fn disclaim = (sd_disclaim_fn)dlsym(RTLD_DEFAULT, "responsibility_spawnattrs_setdisclaim");
	if (rc == 0 && disclaim != NULL && disclaim(&attr, 1) == 0) *disclaimed = 1;
	if (rc == 0) rc = posix_spawn(pid, path, &files, &attr, argv, envp);
	posix_spawn_file_actions_destroy(&files);
	posix_spawnattr_destroy(&attr);
	return rc;
}

// sd_responsible is the process pid answers to for privacy checks; -1 when it is not known.
static pid_t sd_responsible(pid_t pid) {
	sd_responsible_fn f = (sd_responsible_fn)dlsym(RTLD_DEFAULT, "responsibility_get_pid_responsible_for_pid");
	return f == NULL ? -1 : f(pid);
}
*/
import "C"

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

// spawn starts argv with in, out and errOut as its stdin, stdout and stderr, in a session of its own
// and responsible for itself (see the notes above). The caller closes its copies of the three.
func spawn(argv []string, in, out, errOut *os.File) (*os.Process, error) {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, err
	}
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	cargv := cStrings(argv)
	defer freeCStrings(cargv)
	cenv := cStrings(os.Environ())
	defer freeCStrings(cenv)

	var pid C.pid_t
	var disclaimed C.int
	if rc := C.sd_spawn(cpath, &cargv[0], &cenv[0], C.int(in.Fd()), C.int(out.Fd()), C.int(errOut.Fd()), &pid,
		&disclaimed); rc != 0 {
		return nil, fmt.Errorf("posix_spawn %s: %w", path, syscall.Errno(rc))
	}
	return os.FindProcess(int(pid))
}

// responsibleFor is the process pid answers to for the system's privacy checks; -1 when it is not
// known. A disclaimed keeper answers for itself.
func responsibleFor(pid int) int { return int(C.sd_responsible(C.pid_t(pid))) }

// cStrings is ss as a NULL-terminated array of C strings, for freeCStrings to free.
func cStrings(ss []string) []*C.char {
	out := make([]*C.char, len(ss)+1)
	for i, s := range ss {
		out[i] = C.CString(s)
	}
	return out
}

func freeCStrings(cs []*C.char) {
	for _, c := range cs {
		if c != nil {
			C.free(unsafe.Pointer(c))
		}
	}
}

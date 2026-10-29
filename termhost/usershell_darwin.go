// usershell_darwin.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-23
//
// Purpose: the user's login shell from the Mac's user database (getpwuid_r), for LoginShell
// (launch.go).
//
// Notes:
// - The Mac keeps users in Open Directory, not /etc/passwd, so reading that file (the Linux way,
//   usershell_other.go) finds nothing for a real user; the C library's getpwuid_r asks Open
//   Directory.
// - Needs cgo. A build without it (the static keeper) uses usershell_other.go, which then falls
//   back to $SHELL.

//go:build darwin && cgo

package termhost

/*
#include <pwd.h>
#include <stdlib.h>
#include <unistd.h>
*/
import "C"

import "os"

// userShell returns the current user's login shell from the user database, or "".
func userShell() string {
	size := C.sysconf(C._SC_GETPW_R_SIZE_MAX)
	if size <= 0 {
		size = 16384
	}
	buf := C.malloc(C.size_t(size))
	defer C.free(buf)
	var pw C.struct_passwd
	var result *C.struct_passwd
	if C.getpwuid_r(C.uid_t(os.Getuid()), &pw, (*C.char)(buf), C.size_t(size), &result) != 0 || result == nil {
		return ""
	}
	return C.GoString(pw.pw_shell)
}

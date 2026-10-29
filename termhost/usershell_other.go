// usershell_other.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-23
//
// Purpose: the user's login shell from /etc/passwd, for LoginShell (launch.go), where the Mac's
// user database is not reachable (usershell_darwin.go).
//
// Notes:
// - Taken from SiliconDesk's host/internal/termhost/termhost.go (passwdShell) on 2026-10-23.
// - A user known only to NSS (LDAP, SSSD) is not in /etc/passwd; LoginShell then falls back to
//   $SHELL, which an SSH login sets from the same database.

//go:build !(darwin && cgo)

package termhost

import (
	"fmt"
	"os"
	"strings"
)

// userShell returns the current user's login shell from /etc/passwd, or "".
func userShell() string {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	want := fmt.Sprint(os.Getuid())
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Split(line, ":")
		if len(f) >= 7 && f[2] == want {
			return f[6]
		}
	}
	return ""
}

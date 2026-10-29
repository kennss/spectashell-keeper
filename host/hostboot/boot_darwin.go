// boot_darwin.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-30
//
// Purpose: this boot's id on the Mac: kern.boottime as "seconds.microseconds", the microseconds in
// six digits, the form the app's BootID.swift writes for this Mac's own boot. The keeper program
// answers its host's listings with it, and the core its own Mac's (bus/hosts.go).
//
// Notes:
// - Moved here from core/internal/hostboot on 2026-10-29, with the keeper program
//   (Docs/design/01-DESIGN.md §3.2).

package hostboot

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// ID is this machine's boot: another after every restart; "" when it cannot be read.
func ID() string {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d.%06d", tv.Sec, tv.Usec)
}

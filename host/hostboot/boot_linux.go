// boot_linux.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-30
//
// Purpose: this boot's id on Linux, which changes with every restart (/proc's boot_id).
//
// Notes:
// - Moved here from core/internal/hostboot on 2026-10-29, with the keeper program
//   (Docs/design/01-DESIGN.md §3.2).

package hostboot

import (
	"os"
	"strings"
)

// ID is this machine's boot: another after every restart; "" when it cannot be read.
func ID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

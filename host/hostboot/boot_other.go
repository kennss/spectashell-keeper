// boot_other.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-29
//
// Purpose: no boot id where keepers do not run (the app layer's tests build every package for
// every platform).
//
// Notes:
// - Moved here from core/internal/hostboot on 2026-10-29, with the keeper program
//   (Docs/DESIGN.md §3.2).

//go:build !linux && !darwin

package hostboot

// ID is this machine's boot: another after every restart; "" when it cannot be read.
func ID() string { return "" }

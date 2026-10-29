// system_other.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-23
//
// Purpose: paneinfo on a system other than Linux and macOS, where the keeper does not run: nothing
// is known, and Read says so.

//go:build !linux && !darwin

package paneinfo

import (
	"errors"
	"fmt"
)

func newSystem() system { return unsupported{} }

// readsProcessFiles says whether this build reads a process's folder, open files and sockets.
const readsProcessFiles = false

// unsupported reads nothing.
type unsupported struct{}

func (unsupported) session(shellPID int) (map[int]process, error) {
	return nil, fmt.Errorf("paneinfo: shell %d: %w", shellPID, errors.ErrUnsupported)
}
func (unsupported) commandLine(int) []string      { return nil }
func (unsupported) environ(int) map[string]string { return nil }
func (unsupported) directory(int) string          { return "" }
func (unsupported) openFiles(int) []string        { return nil }
func (unsupported) ports(*snapshot) []Port        { return nil }
func (unsupported) mounts() mounts                { return nil }

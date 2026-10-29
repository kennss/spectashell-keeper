// system_linux.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-23
//
// Purpose: Linux's system for paneinfo: /proc (procfs.go, procfs_ports.go), pure Go, so the
// keeper built with CGO_ENABLED=0 reads it.

package paneinfo

func newSystem() system { return procfs{root: "/proc"} }

// readsProcessFiles says whether this build reads a process's folder, open files and sockets.
const readsProcessFiles = true

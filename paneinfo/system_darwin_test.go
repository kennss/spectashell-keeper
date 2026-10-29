// system_darwin_test.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-23
//
// Purpose: macOS's reader (system_darwin.go): KERN_PROCARGS2 split into the command line and the
// environment — the path's padding skipped, an empty argument kept, Apple's strings after the
// environment left out, a cut-off buffer — and this process's own read back; getfsstat's file
// systems as mounts, the data volume's seen at their firmlinked paths, network ones and autofs
// remote; this Mac's own mounts.

//go:build darwin

package paneinfo

import (
	"encoding/binary"
	"os"
	"slices"
	"testing"
)

// procargsOf makes a KERN_PROCARGS2 buffer.
func procargsOf(argc int, rest string) []byte {
	b := binary.LittleEndian.AppendUint32(nil, uint32(argc))
	return append(b, rest...)
}

func TestParseProcargs(t *testing.T) {
	b := procargsOf(3, "/usr/bin/python3\x00\x00\x00\x00python3\x00-m\x00\x00A=1\x00B=x=y\x00\x00ptr_munge=\x00main_stack=\x00")
	args, env := parseProcargs(b, true)
	if !slices.Equal(args, []string{"python3", "-m", ""}) || !slices.Equal(env, []string{"A=1", "B=x=y"}) {
		t.Fatalf("args %q env %q", args, env)
	}
	if args, env := parseProcargs(b, false); len(args) != 3 || env != nil {
		t.Fatalf("without the environment: %q %q", args, env)
	}
	if m := parseEnviron(env); m["B"] != "x=y" || len(m) != 2 {
		t.Fatalf("environ %v", m)
	}
	// Cut off in the arguments; too short; no path's end.
	if args, _ := parseProcargs(procargsOf(3, "/bin/sleep\x00sleep\x0030"), true); !slices.Equal(args, []string{"sleep", "30"}) {
		t.Fatalf("cut off %q", args)
	}
	for _, bad := range [][]byte{nil, {1, 0}, procargsOf(1, "/bin/sleep")} {
		if args, env := parseProcargs(bad, true); args != nil || env != nil {
			t.Fatalf("%q: %q %q", bad, args, env)
		}
	}
}

func TestThisProcessesOwnArguments(t *testing.T) {
	d := &darwin{}
	if got := d.commandLine(os.Getpid()); !slices.Equal(got, os.Args) {
		t.Fatalf("command line %q, want %q", got, os.Args)
	}
	if home := os.Getenv("HOME"); home != "" && d.environ(os.Getpid())["HOME"] != home {
		t.Fatalf("environment %v", d.environ(os.Getpid()))
	}
	if d.commandLine(1) != nil { // launchd is root's
		t.Fatal("another user's process")
	}
}

func TestDarwinMounts(t *testing.T) {
	m := newMounts(slices.Concat(
		darwinMounts("/", "apfs", true),
		darwinMounts("/System/Volumes/Data", "apfs", true),
		darwinMounts("/System/Volumes/Data/home", "autofs", false),
		darwinMounts("/Volumes/home", "smbfs", false),
		darwinMounts("/Volumes/Nvme1T", "hfs", true),
	))
	for dir, want := range map[string]bool{
		"/Users/someone": false, "/home/u": true, "/System/Volumes/Data/home/u": true, "/Volumes/home/x": true,
		"/Volumes/Nvme1T/src": false, "/Volumes/homework": false, "/homework": false,
	} {
		if got := m.remote(dir); got != want {
			t.Errorf("%s: remote %v, want %v", dir, got, want)
		}
	}
	// This Mac's: the root and the temporary folders are local.
	own := (&darwin{}).mounts()
	if len(own) == 0 || own.remote("/") || own.remote(canonical(t, t.TempDir())) {
		t.Fatalf("own mounts %+v", own)
	}
}

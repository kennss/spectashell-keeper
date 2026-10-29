// events_test.go
// Author: Kennt Kim
// Created: 2026-10-29 · Last updated: 2026-10-29
//
// Purpose: what a keeper says of an end nothing else explains (events.go): a signal ends it through
// its End, naming the signal, its event log removed; killed outright, a listing keeps the log's last
// lines in the End it writes for it.

//go:build unix

package keeper

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestASignalEndsAKeeperWithItsNameInItsEnd(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		dir := shortDir(t)
		c := startKeeper(t, dir, "5151515151515151", []string{"/bin/sh"})
		r := record(c)
		r.wait(t, "the replay", func(_ string, replayEnd int, _ *Exit) bool { return replayEnd >= 0 })
		pid := List(dir)[0].KeeperPID
		if err := syscall.Kill(pid, sig); err != nil {
			t.Fatal(err)
		}
		end := endOf(t, dir, "5151515151515151")
		if end.How != EndSignalled || end.Signal != unixName(sig) {
			t.Fatalf("%v: End = %+v", sig, end)
		}
		waitFor(t, "the keeper to end", func() bool { return len(List(dir)) == 0 })
		if _, err := os.Stat(EventsPath(SocketPath(dir, "5151515151515151"))); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%v: the event log outlived an End that says how (%v)", sig, err)
		}
	}
}

func TestAKeeperKilledOutrightLeavesWhatItLastDid(t *testing.T) {
	dir := shortDir(t)
	c := startKeeper(t, dir, "6161616161616161", []string{"/bin/sh"})
	r := record(c)
	r.wait(t, "the replay", func(_ string, replayEnd int, _ *Exit) bool { return replayEnd >= 0 })
	pid := List(dir)[0].KeeperPID
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the keeper to be found gone", func() bool { return len(List(dir)) == 0 })
	end := endOf(t, dir, "6161616161616161")
	if end.How != EndLost || !slices.ContainsFunc(end.Events, func(l string) bool { return strings.Contains(l, "started") }) ||
		!slices.ContainsFunc(end.Events, func(l string) bool { return strings.Contains(l, "a client attached") }) {
		t.Fatalf("End = %+v", end)
	}
	if _, err := os.Stat(EventsPath(SocketPath(dir, "6161616161616161"))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the event log was left beside the End (%v)", err)
	}
}

func unixName(sig syscall.Signal) string {
	return map[syscall.Signal]string{syscall.SIGTERM: "SIGTERM", syscall.SIGHUP: "SIGHUP"}[sig]
}

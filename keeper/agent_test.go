// agent_test.go
// Author: Kennt Kim
// Created: 2026-10-29 · Last updated: 2026-10-29
//
// Purpose: a keeper's agent link (agent.go): pointed by a client at its agent, kept by the client
// that took the keeper over when the one before leaves, taken back by its own client, made only
// while the keeper runs, and removed when the keeper ends.

//go:build unix

package keeper

import (
	"errors"
	"io/fs"
	"os"
	"testing"
)

func TestTheAgentLinkIsTheHoldersAndEndsWithTheKeeper(t *testing.T) {
	dir := shortDir(t)
	c := startKeeper(t, dir, "a1", []string{"/bin/sh"})
	socket := SocketPath(dir, "a1")
	link := AgentPath(socket)
	pointsTo := func() string {
		to, _ := os.Readlink(link)
		return to
	}
	if err := PointAgent(socket, "/tmp/ssh-first/agent.1"); err != nil || pointsTo() != "/tmp/ssh-first/agent.1" {
		t.Fatalf("pointed: %v, %q", err, pointsTo())
	}
	// Another client takes the keeper over: its agent is the link's, and the first, leaving, leaves it.
	if err := PointAgent(socket, "/tmp/ssh-second/agent.2"); err != nil {
		t.Fatal(err)
	}
	UnpointAgent(socket, "/tmp/ssh-first/agent.1")
	if pointsTo() != "/tmp/ssh-second/agent.2" {
		t.Fatalf("the first client took the second's link: %q", pointsTo())
	}
	// Its own client leaving takes it back.
	UnpointAgent(socket, "/tmp/ssh-second/agent.2")
	if _, err := os.Lstat(link); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("still there: %v", err)
	}
	// The keeper ends with its link and lock gone, and none is made for a keeper gone.
	if err := PointAgent(socket, "/tmp/ssh-third/agent.3"); err != nil {
		t.Fatal(err)
	}
	c.Hangup(Who{})
	waitFor(t, "the keeper to end", func() bool { return len(List(dir)) == 0 })
	for _, path := range []string{link, agentLock(socket)} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s outlived its keeper (%v)", path, err)
		}
	}
	if err := PointAgent(socket, "/tmp/ssh-late/agent.4"); err == nil {
		t.Fatal("a link was made for a keeper gone")
	}
	if _, err := os.Lstat(agentLock(socket)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a lock was made for a keeper gone")
	}
}

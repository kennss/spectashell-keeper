// agent.go
// Author: Kennt Kim
// Created: 2026-10-29 · Last updated: 2026-10-29
//
// Purpose: a keeper's agent link (Docs/KEEPER.md §5): the path its terminal's SSH_AUTH_SOCK names,
// beside its socket, which the client holding the keeper points at the SSH agent its own connection
// forwards, and takes back as it leaves.
//
// Notes:
// - Agent forwarding is a connection's (OpenSSH makes a socket for it per connection, gone when the
//   connection goes), and a kept terminal outlives every connection: a variable set once, when the
//   shell started, would name a socket long gone. So the shell is given a path of its own that
//   never changes, `<id>.agent`, a symbolic link the client that takes the keeper over points at
//   the agent its connection forwards — as tmux users point a fixed SSH_AUTH_SOCK at the newest
//   login's. A client watching never points it: the agent is the holder's.
// - The link is replaced in one rename, so a program reading it sees the old agent or the new one,
//   never none; a client leaving takes it back only while it is still its own, so a client that
//   took the keeper over keeps its agent there. A link left by a client that ended without taking
//   it back names a socket gone with its connection: connecting to it fails, as with no agent, until
//   the next client points it again.
// - The keeper removes the link as it ends, and a listing removes it with a dead keeper's socket.

//go:build unix

package keeper

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// AgentPath is a keeper's agent link: the path its terminal's SSH_AUTH_SOCK names.
func AgentPath(socket string) string { return strings.TrimSuffix(socket, ".sock") + ".agent" }

// agentLock is the file whose lock a client holds while it changes the agent link.
func agentLock(socket string) string { return strings.TrimSuffix(socket, ".sock") + ".agentlock" }

// PointAgent points the agent link of the keeper at socket to agent, the socket of the SSH agent
// the client's connection forwards, in place of whatever it pointed at.
func PointAgent(socket, agent string) error {
	return withAgentLock(socket, func(link string) error {
		tmp := link + ".tmp"
		_ = os.Remove(tmp)
		if err := os.Symlink(agent, tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, link); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		return nil
	})
}

// UnpointAgent takes the agent link of the keeper at socket back from agent: removed while it
// still points there, left alone once another client pointed it elsewhere.
func UnpointAgent(socket, agent string) {
	_ = withAgentLock(socket, func(link string) error {
		if to, err := os.Readlink(link); err == nil && to == agent {
			return os.Remove(link)
		}
		return nil
	})
}

// withAgentLock runs change on the agent link holding its lock, so a client leaving never takes
// back a link another client pointed between its look and its removal.
func withAgentLock(socket string, change func(link string) error) error {
	// A keeper gone removed its link and lock as it ended (removeAgent): made again, they would stay.
	if _, err := os.Lstat(socket); err != nil {
		return err
	}
	f, err := os.OpenFile(agentLock(socket), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()
	return change(AgentPath(socket))
}

// removeAgent removes the agent link and its lock of the keeper at socket, which ended.
func removeAgent(socket string) {
	_ = os.Remove(AgentPath(socket))
	_ = os.Remove(agentLock(socket))
}

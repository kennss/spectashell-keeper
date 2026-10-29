// shell.go
// Author: Kennt Kim
// Created: 2026-10-25 · Last updated: 2026-10-30
//
// Purpose: the shell an agent's page command runs in (Docs/design/05-WINDOWS.md §3): the keeper
// holding it, and the socket of the device that holds that keeper now. A command acts on the pages
// beside its own shell only, so it must know which shell that is.
//
// Notes:
// - SDTERM_KEEPER_ID names the keeper in shells started from 2026-10-25 (KeeperVariable), and
//   follows the shell's programs where its parents do not: a tmux server started in the shell is
//   a child of init. Keepers started before set nothing; for them, and when the variable names no
//   keeper running here, the command's parent processes are followed up to a keeper's shell
//   (Description.PID) or the keeper itself (KeeperPID).
// - The keeper's description says which device attached to it last (Meta.ClientID) and whether
//   one is attached now; a shell no device holds has no page to use.
// - Moved here from core/internal/pages on 2026-10-29, with the keeper program
//   (Docs/design/01-DESIGN.md §3.2).

//go:build unix

package pages

import (
	"errors"

	"github.com/kennss/spectashell-keeper/keeper"
)

// KeeperVariable is the variable that names a shell's keeper in its environment.
const KeeperVariable = "SDTERM_KEEPER_ID"

// maxParents is how far up the parent processes are followed.
const maxParents = 64

// errNotKept is a command run outside a kept shell.
var errNotKept = errors.New("this command runs in a shell SpectaShell keeps — a pane of SpectaShell on " +
	"this machine, on a host whose shells are kept — and this is not one")

// Own finds the keeper of the shell process pid runs in, among the keepers in dir. getenv reads
// the process's environment and parent gives a process's parent (0 when not known).
func Own(dir string, getenv func(string) string, pid int, parent func(int) int) (keeper.Description, error) {
	running := map[string]keeper.Description{}
	byPID := map[int]keeper.Description{}
	for _, d := range keeper.List(dir) {
		if d.Exited {
			continue
		}
		running[d.KeeperID] = d
		byPID[d.PID] = d
		byPID[d.KeeperPID] = d
	}
	if id := getenv(KeeperVariable); keeper.ValidID(id) {
		if d, ok := running[id]; ok {
			return d, nil
		}
	}
	for p, steps := pid, 0; p > 1 && steps < maxParents; p, steps = parent(p), steps+1 {
		if d, ok := byPID[p]; ok {
			return d, nil
		}
	}
	return keeper.Description{}, errNotKept
}

// Holder is the socket of the device that holds keeper d now, in the pages folder dir.
func Holder(dir string, d keeper.Description) (string, error) {
	if !d.Attached {
		return "", errors.New("no device has this shell open now: open it in SpectaShell, and the page " +
			"beside it is the one its commands use")
	}
	if d.Meta.ClientID == "" {
		return "", errors.New("the device that has this shell open is a SpectaShell from before pages took " +
			"commands: update it")
	}
	return DeviceSocket(dir, d.Meta.ClientID), nil
}

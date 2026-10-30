// link.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-30
//
// Purpose: the Link — what carries one program's input and output to a terminal
// (Docs/design/01-DESIGN.md §3.2, §5). The terminal knows no transport: a keeper on this machine, a
// keeper reached over SSH, or a plain SSH shell are each a Link, and the engine's user picks one.
//
// Notes:
// - A Link speaks in the keeper protocol's terms (keeper.Events, keeper.Meta), since a keeper is
//   the richest link: output, then the end of a replay, then live output, and the program's exit.
//   A link with no memory sends ReplayEnd before any output (an empty replay) and ignores Meta.
// - A *keeper.Conn is a Link as it is.
// - ReadEvents runs on the terminal's reader goroutine until the link ends, and returns why:
//   keeper.ErrTakenOver when another client took the keeper over.
// - A link that can share its program with other clients is also Joinable (a keeper's,
//   2026-10-26, Docs/design/07-TEAMS.md §1): it names this client and takes the keyboard.

//go:build unix

package termhost

import (
	"time"

	"github.com/kennss/spectashell-keeper/keeper"
)

// Link carries one program's bytes.
type Link interface {
	// ReadEvents delivers the link's events until it ends, and returns the reason it ended.
	ReadEvents(keeper.Events) error
	// Input writes to the program's input.
	Input(b []byte) error
	// Resize sets the program's terminal size.
	Resize(cols, rows int) error
	// SetMeta tells the link's keeper what to remember; a link without one ignores it.
	SetMeta(keeper.Meta) error
	// Hangup ends the program (SIGHUP to its process group) and the link; by is this client, for
	// the keeper's word of how it ended (keeper.End), which a link without a keeper ignores.
	Hangup(by keeper.Who)
	// Close ends the link and leaves the program to its keeper, if it has one.
	Close()
}

// Joinable is a link whose program other clients may watch, and this one too: a keeper's.
type Joinable interface {
	// SayWho names this client to the keeper and says it can watch (keeper.Conn.SayWho).
	SayWho(keeper.Who) error
	// Take takes the keyboard for this client, when it watches (keeper.Conn.Take).
	Take() error
	// Front says this client's device is in front for d, or with 0 that it went away
	// (keeper.Conn.Front; KEEPER.md §2.2).
	Front(d time.Duration) error
}

// LinkInfo is what a link knows of its program when it is attached.
type LinkInfo struct {
	// PID is the program's process id on its machine; 0: not known.
	PID int
	// Columns and Rows are the program's terminal size now.
	Columns, Rows int
	// Meta is what the link's keeper remembers; zero without one.
	Meta keeper.Meta
	// Exited is set when the program ended before the link was attached; its exit follows the
	// replay.
	Exited bool
}

// KeeperInfo is the LinkInfo of a keeper connection, from its Hello.
func KeeperInfo(c *keeper.Conn) LinkInfo {
	return LinkInfo{PID: c.Hello.PID, Columns: c.Hello.Columns, Rows: c.Hello.Rows, Meta: c.Hello.Meta,
		Exited: c.Hello.Exited}
}

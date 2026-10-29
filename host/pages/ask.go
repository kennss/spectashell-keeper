// ask.go
// Author: Kennt Kim
// Created: 2026-10-25 · Last updated: 2026-10-30
//
// Purpose: asking the pages beside the shell this process runs in (Docs/WINDOWS.md §3), for
// the page command (cli.go) and the MCP server (mcp.go): its keeper found, the device holding it,
// the request sent to that device's socket and its reply read.
//
// Notes:
// - Moved here from core/internal/pages on 2026-10-29, with the keeper program
//   (Docs/DESIGN.md §3.2).

//go:build unix

package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// answerWait is how long an asker waits for any reply but a wait's.
const answerWait = 60 * time.Second

// Asker asks the pages beside one process's shell.
type Asker struct {
	// Keepers is the keepers' folder of this machine (keeper.Place.Dir).
	Keepers string
	// Getenv, PID and Parent describe the asking process (os.Getenv, os.Getpid, Parent).
	Getenv func(string) string
	PID    int
	Parent func(int) int
}

// Here is an asker for this process.
func Here(keepers string) Asker {
	return Asker{Keepers: keepers, Getenv: os.Getenv, PID: os.Getpid(), Parent: Parent}
}

// Ask sends command with args to the page at port (0: the one last used) beside the shell. A
// reply carrying an error is returned as an error.
func (a Asker) Ask(command string, port int, args map[string]any) (Reply, error) {
	d, err := Own(a.Keepers, a.Getenv, a.PID, a.Parent)
	if err != nil {
		return Reply{}, err
	}
	dir, err := Dir(a.Keepers)
	if err != nil {
		return Reply{}, err
	}
	socket, err := Holder(dir, d)
	if err != nil {
		return Reply{}, err
	}
	req := Request{Keeper: d.KeeperID, Command: command, Port: port}
	if len(args) > 0 {
		if req.Args, err = json.Marshal(args); err != nil {
			return Reply{}, err
		}
	}
	wait := answerWait
	if ms, ok := args["timeoutMs"].(int); ok {
		wait = time.Duration(ms)*time.Millisecond + 15*time.Second
	}
	reply, err := Ask(socket, req, wait)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			device := d.Meta.Client
			if device == "" {
				device = "the device that has this shell open"
			}
			return Reply{}, fmt.Errorf("SpectaShell on %s does not take page commands now: an iPad or iPhone takes "+
				"them only while the app is on screen", device)
		}
		return Reply{}, err
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}

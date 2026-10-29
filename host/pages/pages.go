// pages.go
// Author: Kennt Kim
// Created: 2026-10-25 · Last updated: 2026-10-29
//
// Purpose: a coding agent's commands to the page beside its shell (Docs/DESIGN.md §6.1.2): what a
// command and its reply are, where a device's socket for them is, and asking one. The agent runs
// the keeper program (`sdterm-keeper page …`, cli.go; `sdterm-keeper mcp`, mcp.go); the command
// finds the keeper of its shell (shell.go) and the device that holds it, and asks that device's
// socket — the core itself on the Mac, a relay on a host (relay.go) — which passes it to the app.
//
// Notes:
// - A socket takes one request: the asker writes a Request as JSON, reads one Reply and closes.
// - A device's socket is named by a digest of its id (DeviceSocket), so the id itself, which
//   tells the device apart from others of its name, is not spread over hosts' folders as names,
//   and the name stays short: a Unix socket's path is at most 104 bytes on macOS.
// - The folder is `pages` in the keepers' folder (keeper.Place), made and checked as private as
//   it (keeper.EnsurePrivateDir): anyone who reaches a device's socket drives its pages.
// - Args are PageAgent.js's arguments (client/sdTerm/Shared), passed to the app as they are.
// - Moved here from core/internal/pages on 2026-10-29, with the keeper program
//   (Docs/DESIGN.md §3.2).

package pages

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/kennss/spectashell-keeper/keeper"
)

// Request is one command to a page.
type Request struct {
	// Keeper is the asking shell's keeper; the core finds its terminal by it.
	Keeper string `json:"keeper"`
	// Command is the verb: open, snapshot, click…
	Command string `json:"command"`
	// Port names the page by its port on the shell's machine; 0: the page the agent last used.
	Port int `json:"port,omitempty"`
	// Args are the command's arguments, as JSON.
	Args json.RawMessage `json:"args,omitempty"`
}

// Reply is a command's answer: an error, or what it found or did.
type Reply struct {
	Error string `json:"error,omitempty"`
	Text  string `json:"text,omitempty"`
	// Image is a screenshot, PNG.
	Image []byte `json:"image,omitempty"`
	// Page is the page the command went to, as it is after it.
	Page *Page `json:"page,omitempty"`
}

// Page is a page as a reply says it.
type Page struct {
	Port  int    `json:"port"`
	Path  string `json:"path,omitempty"`
	Title string `json:"title,omitempty"`
	// Host is the shell's host as the user names it ("GPU Box").
	Host string `json:"host,omitempty"`
}

// Dir is the folder of the devices' sockets in keepers, created when needed and checked to be
// the user's own.
func Dir(keepers string) (string, error) {
	dir := filepath.Join(keepers, "pages")
	return dir, keeper.EnsurePrivateDir(dir)
}

// DeviceSocket is where the device with id takes page commands, in dir.
func DeviceSocket(dir, deviceID string) string { return SocketNamed(dir, DeviceName(deviceID)) }

// SocketNamed is the socket of the device whose socket name (DeviceName) is name, in dir.
func SocketNamed(dir, name string) string { return filepath.Join(dir, name+".sock") }

// DeviceName is the name of a device's socket: 16 hexadecimal characters.
func DeviceName(deviceID string) string {
	sum := sha256.Sum256([]byte("sdTerm pages device " + deviceID))
	return hex.EncodeToString(sum[:8])
}

// ValidName says whether name is a device's socket name as DeviceName makes them.
func ValidName(name string) bool { return keeper.ValidID(name) }

// Ask sends req to the socket at path and returns its reply, waiting at most wait.
func Ask(path string, req Request, wait time.Duration) (Reply, error) {
	conn, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		return Reply{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(wait))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Reply{}, fmt.Errorf("sending the command: %w", err)
	}
	var reply Reply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return Reply{}, fmt.Errorf("reading the answer: %w", err)
	}
	return reply, nil
}

// Handler answers one request: the core's, which passes it to the app.
type Handler func(Request) Reply

// serveConn answers the one request of conn with handle.
func serveConn(conn net.Conn, handle func(Request) Reply) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(Reply{Error: "the command could not be read: " + err.Error()})
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	_ = json.NewEncoder(conn).Encode(handle(req))
}

// readTimeout is how long an asker has to send its request once connected.
const readTimeout = 10 * time.Second

// protocol.go
// Author: Kennt Kim
// Created: 2026-10-14 · Last updated: 2026-10-30
//
// Purpose: the keeper protocol (Docs/KEEPER.md) — how a client talks to the keeper process that
// holds a terminal program. It is version 1 forever: any later build must be able to talk to a
// keeper an earlier build started, so this file only ever grows.
//
// Notes:
// - Taken from SiliconDesk's host/internal/keeper/protocol.go on 2026-10-23; the wire format is
//   SiliconDesk's, unchanged, so the two speak the same protocol. Grown in sdTerm (2026-10-23):
//   TakenOver (keeper -> client), and the description each keeper writes beside its socket
//   (Description). Where these notes say "session", read the client.
// - A frame is a 4-byte big-endian length (of what follows), a 1-byte type and the payload.
// - Keeper -> session: Hello (JSON), Output (bytes) — first the kept output, then ReplayEnd, then
//   live output — and Exit (JSON) once the program ended; TakenOver (empty) just before the keeper
//   closes a connection because another client connected, so the first knows its program still
//   runs. A client from before TakenOver skips it and sees the connection end. Info (JSON, sdTerm,
//   2026-10-23): what runs in the shell (paneinfo), after the replay and whenever it changes.
//   Size (2 + 2 bytes, columns and rows, sdTerm, 2026-10-23): in the replay, before each stretch
//   of kept output, the terminal's size it was written at; the last one is the size now. A client
//   from before Size skips it and draws the whole replay at one size, as it always did.
// - Session -> keeper: Input (bytes), Resize (2 + 2 bytes, columns and rows), Meta (JSON, what the
//   keeper remembers for the next session: owner, persist, title, directory, restoration id, the
//   origin its starting client gave it, its arrangement among others, and for a web app's server its port and token), Hangup, and Stop (added 2026-10-19 for web apps'
//   servers: SIGTERM, SIGKILL after stopGrace, then the keeper ends). Hangup carries the client
//   that hangs up (Who, JSON, sdTerm, 2026-10-26); a keeper from before reads none, and one hung
//   up by an earlier client gets an empty payload and names no one.
// - The end beside the socket (End, <id>.end, sdTerm, 2026-10-26): how a keeper ended — closed by
//   which client, stopped, left after its program ended, or found gone — written as it ends, since
//   its description goes with it, so a device that held the shell can say what became of it.
// - A keeper may hold a terminal program or a web app's server (Docs/apps/webapps.md §2.4): one
//   with Meta.Server set is a server, which no terminal adopts.
// - Joining (sdTerm, 2026-10-26; Docs/design/07-TEAMS.md §1): a keeper also listens on a second
//   socket beside the first (JoinPath, `<id>.join`), and a connection there watches instead of
//   taking the keeper over. Of all the keeper's connections one at most holds the keyboard — its
//   Input reaches the program and its Resize sizes the terminal — and the others watch: they get
//   the output, the exit and Info as the holder does, and Size whenever the terminal's size
//   changes, also after the replay; their Input, Meta and Stop are ignored, their Hangup only
//   detaches them, and their Resize is remembered for when they hold the keyboard. Who (client ->
//   keeper) names a connection and says it can watch; Take (client -> keeper) moves the keyboard to
//   a watcher, and the holder watches from then on if it said Who, else it is TakenOver as before.
//   Present (keeper -> client) tells every connection that said Who, or joined, whether it holds
//   the keyboard, who does, and who watches, whenever that changes. A keeper from before has no
//   `<id>.join`: joining it fails, and never takes it over.
// - Unknown frame types are skipped and unknown JSON keys ignored, so either side may add types
//   or keys later; nothing existing may change meaning.
// - Frames are at most maxFrame bytes; Output and Input are split to stay below it.

package keeper

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Version is the protocol version keepers announce. It never changes (see the file notes).
const Version = 1

const maxFrame = 1 << 20

// Frame types.
const (
	frameHello     = 1
	frameOutput    = 2
	frameReplayEnd = 3
	frameExit      = 4
	frameTakenOver = 5
	frameInfo      = 6
	frameSize      = 7
	framePresent   = 8

	frameInput  = 10
	frameResize = 11
	frameMeta   = 12
	frameHangup = 13
	frameStop   = 14
	frameWho    = 15
	frameTake   = 16
	frameFront  = 17
)

// Hello is what a keeper says first on every connection.
type Hello struct {
	Version  int    `json:"version"`
	KeeperID string `json:"keeper_id"`
	PID      int    `json:"pid"`
	Columns  int    `json:"columns"`
	Rows     int    `json:"rows"`
	Started  int64  `json:"started"` // Unix seconds
	Meta     Meta   `json:"meta"`
	// Set when the program has ended (its Exit follows the replay too).
	Exited bool `json:"exited"`
}

// Meta is what a keeper keeps for the next session.
type Meta struct {
	// The app that owns the terminal, and whether it outlives a session.
	Owner   string `json:"owner"`
	Persist bool   `json:"persist"`
	// The last title and working directory the session saw (the keeper does not parse output).
	Title     string `json:"title"`
	Directory string `json:"directory"`
	// The app's name for the terminal's window (TerminalOpen.restoration_id); added after v1
	// shipped, so keepers started by earlier builds have none.
	RestorationID string `json:"restoration_id,omitempty"`
	// Set when the keeper holds a web app's server rather than a terminal program (added
	// 2026-10-19): what the next session needs to reach it.
	Server *Server `json:"server,omitempty"`
	// Client names the device that attached last ("Sam's MacBook Pro"), as it told the keeper
	// (sdTerm, added 2026-10-23); Description.Attached says whether it still is.
	Client string `json:"client,omitempty"`
	// ClientID tells that device apart from others of the same name (sdTerm, added 2026-10-25):
	// two iPads both say "iPad", since iPadOS gives an app no other name for its device, and each
	// took a shell the other held for its own.
	ClientID string `json:"client_id,omitempty"`
	// Origin is the starting client's own name for where it started the program from (sdTerm:
	// the saved host it reached the keeper through; added 2026-10-27), kept as it was given:
	// whoever attaches later, by whatever address, never changes it, so every client names the
	// terminal alike — two saved hosts of one machine each listed its keepers under its own name.
	Origin string `json:"origin,omitempty"`
	// Arrangement is the client's description of where the terminal sits among others (sdTerm: the
	// desk it is in, as JSON; added 2026-10-30), kept as given and read only by clients. A client
	// watching never sets it, as it sets nothing of Meta.
	Arrangement string `json:"arrangement,omitempty"`
	// StartedBy is the client that started the program, as it names itself (sdTerm, added
	// 2026-10-30), given in the keeper's Config and kept by the keeper itself: a client's Meta
	// never changes it, so a client of an earlier build, which knows no such key, cannot drop it.
	// A client closing a terminal another device started leaves it to its keeper rather than
	// ending it. Nil for a keeper started before, or by a client that gave none.
	StartedBy *Who `json:"started_by,omitempty"`
}

// Server is what a keeper remembers of the web app's server it holds.
type Server struct {
	// The port it listens on, on the host's loopback.
	Port int `json:"port"`
	// The token its pages are opened with (Docs/apps/webapps.md §2.4).
	Token string `json:"token"`
}

// Who is a client as it names itself to a keeper (Meta.Client, Meta.ClientID): sent with Hangup,
// so the keeper's End says who closed it. A zero Who names no one.
type Who struct {
	Client   string `json:"client,omitempty"`
	ClientID string `json:"client_id,omitempty"`
}

// How a keeper ended (End.How).
const (
	// EndClosed: a client hung it up (End.By names it).
	EndClosed = "closed"
	// EndStopped: a client stopped it (a web app's server).
	EndStopped = "stopped"
	// EndExpired: its program had ended, and no client came within exitedIdle.
	EndExpired = "expired"
	// EndLost: a listing found it gone without a word — killed, or its machine went down.
	EndLost = "lost"
	// EndSignalled: a signal ended it (End.Signal names it): the system, or someone with kill
	// (added 2026-10-29; such a keeper was lost before).
	EndSignalled = "signalled"
)

// End is what a keeper leaves beside its socket when it ends (<id>.end, mode 0600), its
// description removed: how it ended, for a client to tell its user what became of the shell it
// held. Ends are kept endKept, then removed by a listing (Ends).
type End struct {
	Version  int    `json:"version"`
	KeeperID string `json:"keeper_id"`
	// Ended is when the keeper ended, Unix seconds; for EndLost, when it was found gone.
	Ended int64  `json:"ended"`
	How   string `json:"how"`
	// By is the client that closed it, for EndClosed; nil when the client named none.
	By *Who `json:"by,omitempty"`
	// Exit is how its program ended, when it did before the keeper: then the program ended by
	// itself, and the keeper was closed or left after it.
	Exit *Exit `json:"exit,omitempty"`
	// Signal is the signal that ended it, for EndSignalled ("SIGTERM").
	Signal string `json:"signal,omitempty"`
	// Events are the last lines of its event log (events.go), for EndLost: what it last did before
	// it was killed. Both added 2026-10-29.
	Events []string `json:"events,omitempty"`
}

// Present is who is connected to a keeper (sdTerm, 2026-10-26): sent to every connection that
// joined or said Who, whenever it changes.
type Present struct {
	// Holding says this connection holds the keyboard.
	Holding bool `json:"holding"`
	// Holder is the connection that holds it, as it named itself (a zero Who when it named none);
	// nil when none does.
	Holder *Who `json:"holder,omitempty"`
	// Watchers are the connections that watch, as they named themselves, in the order they came.
	Watchers []Who `json:"watchers,omitempty"`
}

// ErrTakenOver is what a connection's ReadEvents returns when another client took the keeper over.
var ErrTakenOver = errors.New("keeper: taken over by another client")

// Description is what a keeper writes beside its socket (<id>.json, mode 0600) when it starts,
// when its Meta changes, when its program ends and, at most every previewEvery, when its screen's
// last lines change, and removes when it ends, leaving its End: what a client lists
// without connecting, since connecting takes a keeper over (Docs/KEEPER.md).
type Description struct {
	Version  int    `json:"version"`
	KeeperID string `json:"keeper_id"`
	// KeeperPID is the keeper's own process: a description whose keeper is gone is stale.
	KeeperPID int `json:"keeper_pid"`
	// PID is the program's.
	PID     int   `json:"pid"`
	Started int64 `json:"started"` // Unix seconds
	Meta    Meta  `json:"meta"`
	Exited  bool  `json:"exited"`
	// Build names the keeper program that started it (Config.Build); empty when not given.
	Build string `json:"build,omitempty"`
	// Attached says a client is connected now — Meta.Client, once it told its name — and Exit how
	// the program ended, once it has (added 2026-10-23; absent from earlier keepers).
	Attached bool  `json:"attached,omitempty"`
	Exit     *Exit `json:"exit,omitempty"`
	// Preview is the last lines of the terminal's screen, at most previewLines, its blank ones at
	// the bottom left out and each line's trailing spaces, as the keeper's own emulator draws the
	// output (sdTerm, added 2026-10-25; absent from earlier keepers): what a device that has not
	// opened the shell shows of it.
	Preview []string `json:"preview,omitempty"`
	// FullScreen says the program draws its whole screen (the alternate screen: an editor, top, an
	// agent's interface), so Preview is a piece of that interface rather than output; ScreenTitle
	// is the title the program set (OSC 0 or 2: Claude Code's names its task). Both sdTerm's,
	// added 2026-10-25; absent from earlier keepers.
	FullScreen  bool   `json:"full_screen,omitempty"`
	ScreenTitle string `json:"screen_title,omitempty"`
	// Status is what the program says it is doing, as the keeper's emulator reads its OSC 7501
	// records: its most pressing state ("blocked", "error", "working", "done", "idle") and, with
	// blocked, why ("permission", "question", "auth"). sdTerm's, added 2026-10-29; absent while the
	// program says nothing, and from earlier keepers. A device that has not opened the shell knows
	// from it that the shell waits for the user.
	Status     string `json:"status,omitempty"`
	StatusKind string `json:"status_kind,omitempty"`
	// Watchers are the connections watching the terminal, as they named themselves (sdTerm, added
	// 2026-10-26; absent while none does, and from earlier keepers). Attached and Meta.Client stay
	// the holder's.
	Watchers []Who `json:"watchers,omitempty"`
	// Push says the keeper program tells a user who is away what the program needs (SpectaShell's
	// push, Docs/design/08-PUSH.md §4.5): its connections' Front matters. Absent from earlier
	// keepers, whose shells never notify.
	Push bool `json:"push,omitempty"`
}

// Info is what runs in the shell, as the keeper reads it beside it (Docs/design/02-CONNECTIONS.md
// §3; the engine's paneinfo).
type Info struct {
	Program    string     `json:"program"`
	ProgramPID int        `json:"program_pid"`
	Directory  string     `json:"directory"`
	Branch     string     `json:"branch,omitempty"`
	Ports      []InfoPort `json:"ports,omitempty"`
	Remote     string     `json:"remote,omitempty"`
	Resume     string     `json:"resume,omitempty"`
}

// InfoPort is a TCP port the shell's processes listen on.
type InfoPort struct {
	Port       uint16 `json:"port"`
	Everywhere bool   `json:"everywhere,omitempty"`
	Process    string `json:"process,omitempty"`
}

// Exit is how the program ended.
type Exit struct {
	Code   int    `json:"code"`
	Signal string `json:"signal"`
}

func writeFrame(w io.Writer, kind byte, payload []byte) error {
	f, err := encodeFrame(kind, payload)
	if err != nil {
		return err
	}
	_, err = w.Write(f)
	return err
}

// encodeFrame is a frame as it goes on the wire, in a buffer of its own.
func encodeFrame(kind byte, payload []byte) ([]byte, error) {
	if len(payload)+1 > maxFrame {
		return nil, fmt.Errorf("keeper: frame of %d bytes is too large", len(payload))
	}
	f := make([]byte, 5, 5+len(payload))
	binary.BigEndian.PutUint32(f, uint32(len(payload)+1))
	f[4] = kind
	return append(f, payload...), nil
}

// encodeBytes is data as frames of at most maxFrame-1 bytes each, as writeBytes sends it.
func encodeBytes(kind byte, data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		n := min(len(data), maxFrame-1)
		f, _ := encodeFrame(kind, data[:n])
		out = append(out, f)
		data = data[n:]
	}
	return out
}

// encodeJSON is v as a frame.
func encodeJSON(kind byte, v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return encodeFrame(kind, b)
}

func writeJSON(w io.Writer, kind byte, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFrame(w, kind, b)
}

// writeBytes sends data as frames of at most maxFrame-1 bytes.
func writeBytes(w io.Writer, kind byte, data []byte) error {
	for len(data) > 0 {
		n := min(len(data), maxFrame-1)
		if err := writeFrame(w, kind, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func readFrame(r io.Reader) (byte, []byte, error) {
	head := make([]byte, 5)
	if _, err := io.ReadFull(r, head); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head)
	if n == 0 || n > maxFrame {
		return 0, nil, errors.New("keeper: bad frame length")
	}
	payload := make([]byte, n-1)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return head[4], payload, nil
}

// sizePayload is a Resize's or a Size's payload.
func sizePayload(cols, rows int) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b, uint16(cols))
	binary.BigEndian.PutUint16(b[2:], uint16(rows))
	return b
}

// sizeOf reads a Resize's or a Size's payload; ok is false for a malformed one or a zero size.
func sizeOf(b []byte) (cols, rows int, ok bool) {
	if len(b) != 4 {
		return 0, 0, false
	}
	cols, rows = int(binary.BigEndian.Uint16(b)), int(binary.BigEndian.Uint16(b[2:]))
	return cols, rows, cols > 0 && rows > 0
}

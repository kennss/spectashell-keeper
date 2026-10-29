// client.go
// Author: Kennt Kim
// Created: 2026-10-14 · Last updated: 2026-10-30
//
// Purpose: a client's side of the keeper protocol (Docs/KEEPER.md): starting a keeper for a new
// terminal, connecting to one (new or left from before), reading its output and exit, sending
// input, sizes and what to remember, and listing the keepers in a folder.
//
// Notes:
// - Taken from SiliconDesk's host/internal/keeper/client.go on 2026-10-23. Changed: a connection is
//   any byte stream (NewConn), so a keeper reached over SSH through `attach` is one too
//   (Docs/CONNECTIONS.md §3); ReadEvents returns ErrTakenOver when another client took the keeper
//   over; List reads the keepers' descriptions without connecting to any. Where these notes say
//   "session", read the client.
// - Start runs `command` (the session binary with "keep", or a test helper) in a session of its own
//   (Setsid), so it is not in the session's process group and outlives it; the session reaps it if
//   it ends first.
// - Writes come from the session's loop and its goroutines alike; they are serialised.
// - Closing a Conn without Hangup detaches: the keeper keeps the program for the next session.
// - Joining (2026-10-26, Docs/TEAMS.md §1): DialJoin connects to watch, through the keeper's
//   second socket (JoinPath), and fails with ErrCannotJoin at a keeper from before it; SayWho names
//   the connection and says it can watch, Take moves the keyboard to it, and Events.Present hears
//   who holds and who watches. A watcher hears Size after the replay too, whenever the terminal's
//   size changes.
// - Hangup names the client that hangs up (Who), for the keeper's End (2026-10-26). Ends lists the
//   Ends in a folder and removes those older than endKept; List leaves an End (EndLost) for a
//   keeper it finds gone without one.

//go:build unix

package keeper

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// helloTimeout is how long a new connection waits for the keeper's Hello.
const helloTimeout = 5 * time.Second

// Conn is a client's connection to one keeper.
type Conn struct {
	Hello Hello
	conn  io.ReadWriteCloser
	r     *bufio.Reader
	wmu   sync.Mutex
}

// SocketPath is where the keeper with this id listens in dir.
func SocketPath(dir, id string) string { return filepath.Join(dir, id+".sock") }

// NewID is a new keeper id: 16 hexadecimal characters, short enough for a socket's path (a Unix
// socket's is at most 104 bytes on macOS; Docs/CONNECTIONS.md §1).
func NewID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ValidID says whether id is a keeper id as NewID makes them, so it can name a file safely.
func ValidID(id string) bool {
	if len(id) != 16 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// Start runs a keeper for cfg with command and connects to it.
func Start(command []string, cfg Config, timeout time.Duration) (*Conn, error) {
	in, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin = strings.NewReader(string(in))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start keeper: %w", err)
	}
	go func() { _ = cmd.Wait() }() // reaped if it ends while this session runs

	answer := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(out).ReadString('\n')
		answer <- strings.TrimSpace(line)
	}()
	select {
	case line := <-answer:
		if line != "ready" {
			if line == "" {
				line = "the keeper ended without an answer"
			}
			return nil, errors.New(strings.TrimPrefix(line, "error: "))
		}
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return nil, errors.New("the keeper did not start in time")
	}
	return Dial(cfg.Socket)
}

// Dial connects to a keeper's socket and reads its Hello; the kept output follows on ReadEvents.
func Dial(socket string) (*Conn, error) {
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return nil, err
	}
	return NewConn(conn)
}

// NewConn reads a keeper's Hello from a stream that reaches it — a socket, or a relay such as
// `spectashell-keeper attach` over SSH — and returns the connection; the kept output follows on
// ReadEvents. The stream is closed when no Hello comes within helloTimeout.
func NewConn(rw io.ReadWriteCloser) (*Conn, error) {
	c := &Conn{conn: rw, r: bufio.NewReaderSize(rw, 256*1024)}
	timer := time.AfterFunc(helloTimeout, func() { rw.Close() })
	kind, payload, err := readFrame(c.r)
	if !timer.Stop() && err != nil {
		err = errors.New("keeper: no Hello in time")
	}
	if err == nil && kind != frameHello {
		err = errors.New("keeper: expected Hello")
	}
	if err == nil {
		err = json.Unmarshal(payload, &c.Hello)
	}
	if err != nil {
		rw.Close()
		return nil, err
	}
	return c, nil
}

// ErrCannotJoin is what DialJoin returns for a keeper that runs but cannot be joined: one started by
// a build from before joining, which has no second socket.
var ErrCannotJoin = errors.New("keeper: this shell's keeper is from a build that cannot be joined")

// DialJoin connects to a keeper's socket for joining (JoinPath), to watch without taking it over,
// and reads its Hello; socket is the keeper's first socket.
func DialJoin(socket string) (*Conn, error) {
	c, err := Dial(JoinPath(socket))
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		if _, statErr := os.Stat(socket); statErr == nil {
			return nil, ErrCannotJoin
		}
	}
	return c, err
}

// Events are what ReadEvents delivers, from its goroutine.
type Events struct {
	Output    func([]byte)
	ReplayEnd func()
	Exit      func(Exit)
	// Info receives what runs in the shell; nil ignores it.
	Info func(Info)
	// Size receives, in the replay, the terminal's size the output after it was written at, and,
	// for a connection that watches, the terminal's size whenever it changes; nil ignores it. A
	// keeper of an earlier build sends none.
	Size func(cols, rows int)
	// Present receives who holds the keyboard and who watches, for a connection that joined or
	// said Who; nil ignores it.
	Present func(Present)
}

// ReadEvents reads the keeper's frames until the connection ends; it returns that error.
func (c *Conn) ReadEvents(ev Events) error {
	for {
		kind, payload, err := readFrame(c.r)
		if err != nil {
			return err
		}
		switch kind {
		case frameOutput:
			ev.Output(payload)
		case frameReplayEnd:
			ev.ReplayEnd()
		case frameExit:
			var e Exit
			if json.Unmarshal(payload, &e) == nil {
				ev.Exit(e)
			}
		case frameTakenOver:
			c.conn.Close()
			return ErrTakenOver
		case frameSize:
			if cols, rows, ok := sizeOf(payload); ok && ev.Size != nil {
				ev.Size(cols, rows)
			}
		case frameInfo:
			var info Info
			if ev.Info != nil && json.Unmarshal(payload, &info) == nil {
				ev.Info(info)
			}
		case framePresent:
			var present Present
			if ev.Present != nil && json.Unmarshal(payload, &present) == nil {
				ev.Present(present)
			}
		}
	}
}

func (c *Conn) send(kind byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return writeBytesOrEmpty(c.conn, kind, payload)
}

func writeBytesOrEmpty(w io.Writer, kind byte, payload []byte) error {
	if len(payload) == 0 {
		return writeFrame(w, kind, nil)
	}
	return writeBytes(w, kind, payload)
}

// Input writes to the program's input.
func (c *Conn) Input(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	return c.send(frameInput, b)
}

// Resize sets the pseudo-terminal's size.
func (c *Conn) Resize(cols, rows int) error { return c.send(frameResize, sizePayload(cols, rows)) }

// SetMeta tells the keeper what to remember for the next session.
func (c *Conn) SetMeta(m Meta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return c.send(frameMeta, b)
}

// Hangup ends the terminal: the program gets SIGHUP and the keeper exits, its End naming by as the
// client that closed it (a zero Who names no one).
func (c *Conn) Hangup(by Who) {
	var payload []byte
	if by != (Who{}) {
		payload, _ = json.Marshal(by)
	}
	_ = c.send(frameHangup, payload)
	c.conn.Close()
}

// SayWho names this connection to the keeper and says it can watch: it watches, rather than being
// TakenOver, when another takes the keyboard, and hears Present. A keeper from before skips it.
func (c *Conn) SayWho(who Who) error {
	b, err := json.Marshal(who)
	if err != nil {
		return err
	}
	return c.send(frameWho, b)
}

// Take takes the keyboard for a connection that watches; the one that held it watches, or is
// TakenOver if it cannot. A keeper from before skips it.
func (c *Conn) Take() error { return c.send(frameTake, nil) }

// Stop ends the program gently — SIGTERM, then SIGKILL if it is still there after the keeper's
// grace period — and then the keeper. The connection stays open: the program's Exit comes on it.
func (c *Conn) Stop() error { return c.send(frameStop, nil) }

// Close detaches: the keeper keeps the program for the next session.
func (c *Conn) Close() { c.conn.Close() }

// DescriptionPath is the description file beside a keeper's socket (Description).
func DescriptionPath(socket string) string { return strings.TrimSuffix(socket, ".sock") + ".json" }

// JoinPath is the second socket beside a keeper's first, where a connection joins to watch.
func JoinPath(socket string) string { return strings.TrimSuffix(socket, ".sock") + ".join" }

// EndPath is the file a keeper leaves beside its socket when it ends (End).
func EndPath(socket string) string { return strings.TrimSuffix(socket, ".sock") + ".end" }

// Ends returns the End of every keeper that ended in dir within endKept, and removes older ones.
func Ends(dir string) []End {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	oldest := time.Now().Add(-endKept).Unix()
	var out []End
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".end") || !ValidID(strings.TrimSuffix(name, ".end")) {
			continue
		}
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var end End
		if json.Unmarshal(b, &end) != nil || end.KeeperID+".end" != name || end.Ended < oldest {
			_ = os.Remove(path)
			continue
		}
		out = append(out, end)
	}
	return out
}

// List returns the description of every keeper in dir without connecting to any (connecting
// takes a keeper over). A description whose keeper is gone is stale and removed with its sockets;
// so is a socket left with no description by a keeper that died.
func List(dir string) []Description {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Description
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var d Description
		if json.Unmarshal(b, &d) != nil || d.KeeperPID <= 0 || !alive(d.KeeperPID) {
			// Gone without a word — killed, or its machine went down — unless it said how as it ended.
			if ValidID(d.KeeperID) && d.KeeperID+".json" == name {
				// What it last did, from its event log: all that is left to say why it went.
				events := EventsPath(strings.TrimSuffix(path, ".json") + ".sock")
				writeEnd(filepath.Join(dir, d.KeeperID+".end"), End{Version: Version, KeeperID: d.KeeperID,
					Ended: time.Now().Unix(), How: EndLost, Exit: d.Exit, Events: lastEvents(events, lostEvents)}, false)
				_ = os.Remove(events)
			}
			_ = os.Remove(path)
			_ = os.Remove(strings.TrimSuffix(path, ".json") + ".sock")
			_ = os.Remove(strings.TrimSuffix(path, ".json") + ".join")
			removeAgent(strings.TrimSuffix(path, ".json") + ".sock")
			continue
		}
		out = append(out, d)
	}
	return out
}

// alive reports whether process pid exists.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Discover returns the Hello of every keeper listening in dir; sockets nobody answers on are
// removed. It connects to each — taking each over from whoever held it — and closes the
// connection again: for tests and repair only. Clients list keepers with List.
func Discover(dir string) []Hello {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Hello
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sock") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		c, err := Dial(path)
		if err != nil {
			_ = os.Remove(path)
			continue
		}
		out = append(out, c.Hello)
		c.Close()
	}
	return out
}

// keeper.go
// Author: Kennt Kim
// Created: 2026-10-14 · Last updated: 2026-10-30
//
// Purpose: the keeper process (`spectashell-keeper keep`, Docs/KEEPER.md): it holds one terminal
// program on its pseudo-terminal, keeps the program's recent output, and serves its clients over
// two Unix sockets — one that takes the keeper over, one that joins it to watch — so the program
// outlives the client that started it.
//
// Notes:
// - Taken from SiliconDesk's host/internal/keeper/keeper.go on 2026-10-23. Grown in sdTerm: the
//   description beside the socket (describe), TakenOver before a connection is closed for another,
//   and refreshing the times of the socket, the description and their folders (refreshTimes), which
//   SiliconDesk's session did and here no session does (Docs/design/02-CONNECTIONS.md §3); and what
//   runs in the shell (watchInfo, the engine's paneinfo), read here beside the shell — on whichever
//   machine it is — every infoEvery while a client is connected, and sent as Info when it changed
//   and to every client that connects.
// - A keeper whose socket or description was removed while it runs makes them again (stayReachable):
//   every reachableEvery, and at once on SIGUSR1, as tmux does with its socket. Without its socket
//   no client can reach it again, though it and its program run on. A Mac app cleaner removed the
//   whole ~/Library/Application Support/sdTerm when an earlier build of the app went to the Trash
//   (CleanMyMac, "leftovers" of the same bundle id, 2026-10-24), and three shells were left running
//   that nothing could reach or list. The listener taking the old one's place must not unlink the
//   path as it closes (SetUnlinkOnClose): the path is the new socket's by then.
// - The keeper draws the output as a terminal does (screen, the engine's emulator at the
//   terminal's size, no history) and writes the screen's last lines into its description, at most
//   every previewEvery and only when they changed (writePreviews), so a device that lists the
//   keeper shows what it shows without connecting, which would take it over (Kennt, 2026-10-25:
//   every Running card its last lines, not only those of shells open on the device). Earlier the
//   last screen came only from a pane open on the device, and an emulator in every keeper had been
//   turned down for that one preview.
// - The description says what the program says it is doing too (its OSC 7501 status, as that
//   emulator reads it), so every device's Running marks a shell waiting for the user, not only the
//   device whose pane it is (a review of 2026-10-29: a Claude Code waiting showed amber on its tab
//   and green in Running). A shell's status ends when the shell is back in front, as a session ends
//   it on a pane (watchInfo); a keeper whose program is not a shell never ends it so, since its
//   program is always in front.
// - Where these notes say "session", read the client: whoever is connected.
// - Joining (2026-10-26, Docs/design/07-TEAMS.md §1): a connection on the second socket (JoinPath)
//   watches; one connection at most holds the keyboard (holder), the rest watch (watchers), and a
//   watcher takes the keyboard with Take. A connection on the first socket takes the keyboard from
//   the holder, who is TakenOver and closed as before; watchers stay. Every connection is written
//   from its own queue (peer.go), so a watcher on a slow link never holds the shell back.
// - Start-up: the session writes a Config (JSON) to the keeper's standard input and closes it; the
//   keeper starts the program, listens on Config.Socket and prints "ready" (or "error: <reason>")
//   on its standard output, then points its standard streams at /dev/null.
// - A new connection on the first socket takes over from the holder (a new session replaces an old
//   one). Each connection gets Hello, the kept output, ReplayEnd, the Exit if the program ended,
//   then live output. Output written while no session is connected is only kept.
// - The keeper ends after Hangup (the app closed the terminal: the program's process group gets
//   SIGHUP, as a terminal closing does; only the holder's Hangup ends it), after Stop (SIGTERM, then SIGKILL after stopGrace, to the
//   program's whole process group until it is empty, the program gone or not: a web app's server
//   shuts down its work on SIGTERM, and its children must not outlive it), or after exitedIdle
//   with the program ended and no session connected. A session that merely disconnects — it ended, or it only looked (Discover) — never
//   ends it: the next session may still adopt the terminal and show its exit.
// - Ending, it leaves its End beside the socket (writeEnd) before it removes the socket and its
//   description: how it ended, and for a Hangup the client the frame named. Without it a device
//   whose shell another closed could only say the shell was gone (Kennt, 2026-10-26: the Mac
//   closed two shells, and the iPads' tabs said their programs had ended).
// - The kept output is the last keptBytes bytes, starting at a clean place (CleanStart): cut
//   anywhere, it could begin inside an escape sequence, whose tail the next session's emulator
//   would print as text. That emulator rebuilds the screen (and some history) from it.
// - Modes the program set before the replayed output (bracketed paste, mouse reports, the kitty
//   keyboard flags, the alternate screen, the title) would be lost with the output that set them:
//   the output cut off is fed to an emulator of the keeper's own (modes), and a replay starts with
//   its ModeSequences. The replay starts exactly where that emulator stopped reading. It follows
//   the shell's running command too (shell integration), so a long command's end is still known.
// - The kept output remembers the terminal's size it was written at (sizes, Size frames,
//   2026-10-23): a program writes for the width it was told, and output replayed at another width
//   comes out wrong — zsh's end-of-line mark (PROMPT_SP: "%" and a width of spaces) written at 80
//   columns and replayed at 69 wraps and leaves a "%" above every prompt. A replay sends each
//   stretch of output after the size it was written at, so a client draws it as a client connected
//   all along did; the modes emulator reads it at those sizes too.

//go:build unix

package keeper

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/kennss/spectashell-keeper/paneinfo"
	"github.com/kennss/spectashell-keeper/term"
)

const (
	keptBytes  = 1 << 20
	exitedIdle = 24 * time.Hour
	// endKept is how long a keeper's End stays beside the sockets; a listing removes older ones.
	endKept = 30 * 24 * time.Hour
	// refreshEvery is how often the keeper touches its socket, description and folders, well
	// inside systemd-tmpfiles' shortest common age for /tmp (10 days on Ubuntu, 1 day on some).
	refreshEvery = time.Hour
	// reachableEvery is how often the keeper looks for its socket and description (stayReachable):
	// a look is two lstat calls, and a shell put out of reach comes back within this.
	reachableEvery = 10 * time.Second
	// previewEvery is how often, at most, the screen's last lines are written into the
	// description; a listing reads them every 30 s (the app's Running section).
	previewEvery = 2 * time.Second
	// previewLines is how many of the screen's last lines a description carries, as many as a
	// Running card shows.
	previewLines = 4
	// infoEvery is how often what runs in the shell is read while a client is connected.
	infoEvery = 2 * time.Second
	// stopGrace is how long a program has to end after SIGTERM before it gets SIGKILL (Stop).
	stopGrace = 10 * time.Second
)

// Config is what a session hands a new keeper.
type Config struct {
	Socket   string   `json:"socket"`
	KeeperID string   `json:"keeper_id"`
	Path     string   `json:"path"`
	Argv     []string `json:"argv"`
	Dir      string   `json:"dir"`
	Env      []string `json:"env"`
	Columns  int      `json:"columns"`
	Rows     int      `json:"rows"`
	Meta     Meta     `json:"meta"`
	// Build names the keeper program, for its description (Description.Build).
	Build string `json:"build,omitempty"`
}

type keeper struct {
	cfg     Config
	started time.Time
	ptmx    *os.File
	cmd     *exec.Cmd

	// lnMu guards the endpoints' listeners — replaced when a socket was removed (relisten) — and
	// finishing. The first endpoint takes the keeper over; the second joins it.
	lnMu      sync.Mutex
	endpoints []*endpoint
	finishing bool

	// out guards what sessions see in order: the kept output, the connections and the exit.
	out        sync.Mutex
	kept       []byte
	modes      *term.Terminal // has read kept[:fed] and everything cut off before it
	screen     *term.Terminal // has read all the output, at the terminal's size (the preview)
	ending     bool           // finish began: the description is not written again
	drawn      bool           // the screen read output since the preview was last taken
	preview    []string       // the preview last described
	fullScreen bool           // the alternate screen was on when the preview was taken
	title      string         // the title the program had set then
	status     term.Status    // what the program said it was doing then (OSC 7501)
	fed        int
	sizes      []sizeMark // the size in effect from each offset of kept on; the first is at 0
	holder     *peer      // the connection that holds the keyboard; nil when none does
	watchers   []*peer    // the connections that watch, in the order they came
	exit       *Exit
	cols, rows int
	meta       Meta
	idle       *time.Timer
	infoNow    chan struct{} // asks watchInfo to read at once (a client connected)
	events     *eventLog     // what happened to it, for an end that leaves no End (events.go)
	// observer hears what the keeper notices for a user who is away (observe.go); nil when the
	// keeper program gave none. noticed is the status it was last told of.
	observer Observer
	noticed  term.Status
}

// endpoint is a socket the keeper listens on.
type endpoint struct {
	path string
	// join: a connection here watches, rather than taking the keeper over.
	join bool
	ln   net.Listener
}

// Main runs a keeper process and returns its exit status.
func Main() int { return run(nil) }

// run runs a keeper; observe, when given, makes the observer of what it notices (observe.go).
func run(observe func(View) Observer) int {
	var cfg Config
	if err := json.NewDecoder(os.Stdin).Decode(&cfg); err != nil {
		fmt.Println("error: bad configuration:", err)
		return 2
	}
	k, err := start(cfg)
	if err != nil {
		fmt.Println("error:", err)
		return 1
	}
	if observe != nil {
		k.out.Lock()
		k.observer = observe(view{k})
		k.out.Unlock()
	}
	k.events.note("started: keeper %d, program %d (%s), build %q", os.Getpid(), k.cmd.Process.Pid, cfg.Path, cfg.Build)
	go k.endOnSignals()
	k.out.Lock()
	k.describe()
	k.out.Unlock()
	go k.refreshTimes()
	go k.watchInfo()
	fmt.Println("ready")
	detachStdio()
	k.serve()
	return 0
}

// detachStdio points the standard streams at /dev/null: the session's pipes must not stay open.
func detachStdio() {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return
	}
	for _, fd := range []int{0, 1, 2} {
		_ = unix.Dup2(int(null.Fd()), fd)
	}
	null.Close()
}

func start(cfg Config) (*keeper, error) {
	if cfg.Columns <= 0 || cfg.Rows <= 0 {
		cfg.Columns, cfg.Rows = 80, 24
	}
	endpoints := []*endpoint{{path: cfg.Socket}, {path: JoinPath(cfg.Socket), join: true}}
	closeAll := func() {
		for _, e := range endpoints {
			if e.ln != nil {
				e.ln.Close()
				os.Remove(e.path)
			}
		}
	}
	for _, e := range endpoints {
		_ = os.Remove(e.path)
		ln, err := listen(e.path)
		if err != nil {
			closeAll()
			return nil, err
		}
		e.ln = ln
	}
	cmd := &exec.Cmd{Path: cfg.Path, Args: cfg.Argv, Dir: cfg.Dir, Env: cfg.Env}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cfg.Columns), Rows: uint16(cfg.Rows)})
	if err != nil {
		closeAll()
		return nil, fmt.Errorf("start %s: %w", cfg.Path, err)
	}
	k := &keeper{cfg: cfg, started: time.Now(), ptmx: ptmx, cmd: cmd, endpoints: endpoints,
		cols: cfg.Columns, rows: cfg.Rows, meta: cfg.Meta, modes: newModes(cfg.Columns, cfg.Rows),
		sizes:   []sizeMark{{cols: cfg.Columns, rows: cfg.Rows}},
		infoNow: make(chan struct{}, 1), events: &eventLog{path: EventsPath(cfg.Socket)}}
	// The screen reads the output as it comes: it has the keeper's clock, and tells an observer.
	k.screen = term.New(k.screenOptions(cfg.Columns, cfg.Rows))
	return k, nil
}

// listen listens on a Unix socket at path that only the user may use.
func listen(path string) (net.Listener, error) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func (k *keeper) serve() {
	readDone := make(chan struct{})
	go k.readProgram(readDone)
	go k.waitProgram(readDone)
	go k.stayReachable()
	go k.writePreviews()
	for _, e := range k.endpoints[1:] {
		go k.accept(e)
	}
	k.accept(k.endpoints[0])
}

// accept serves e's connections until finish closes its listener.
func (k *keeper) accept(e *endpoint) {
	for {
		k.lnMu.Lock()
		ln := e.ln
		k.lnMu.Unlock()
		conn, err := ln.Accept()
		if err != nil {
			k.lnMu.Lock()
			replaced := e.ln != ln && !k.finishing
			k.lnMu.Unlock()
			if replaced {
				continue // relisten closed it for a new one
			}
			return // closed by finish
		}
		go k.session(conn, e.join)
	}
}

// stayReachable makes the sockets and the description again when they were removed, every
// reachableEvery and at once on SIGUSR1.
func (k *keeper) stayReachable() {
	again := make(chan os.Signal, 1)
	signal.Notify(again, syscall.SIGUSR1)
	tick := time.NewTicker(reachableEvery)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
		case <-again:
		}
		lost := false
		for _, e := range k.endpoints {
			if _, err := os.Lstat(e.path); errors.Is(err, fs.ErrNotExist) && k.relisten(e) != nil {
				lost = true // the folder cannot be made now; the next look tries again
			}
		}
		if lost {
			continue
		}
		// After the socket: a description is what clients list, and one listed must be reachable.
		k.out.Lock()
		if _, err := os.Lstat(DescriptionPath(k.cfg.Socket)); errors.Is(err, fs.ErrNotExist) {
			k.describe()
		}
		k.out.Unlock()
	}
}

// writePreviews writes the screen's last lines into the description every previewEvery when they
// changed: one small file written while output flows, none while the screen stands still.
func (k *keeper) writePreviews() {
	tick := time.NewTicker(previewEvery)
	defer tick.Stop()
	for range tick.C {
		k.out.Lock()
		if k.drawn {
			k.drawn = false
			lines, full, title := lastLines(k.screen, previewLines), k.screen.AltScreen(), k.screen.Title()
			status := k.screen.Status()
			if !slices.Equal(lines, k.preview) || full != k.fullScreen || title != k.title ||
				!sameStatus(status, k.status) {
				k.preview, k.fullScreen, k.title, k.status = lines, full, title, status
				k.describe()
			}
		}
		k.out.Unlock()
	}
}

// sameStatus says whether two statuses say the same in a description: its state and its kind.
func sameStatus(a, b term.Status) bool { return a.State == b.State && a.Kind == b.Kind }

// active says the program said it is working or waiting for the user: what ends when the shell is
// back in front (watchInfo).
func active(s term.Status) bool { return s.State == "working" || s.State == "blocked" }

// shells are the programs a keeper may start that are shells: at their prompt nothing they ran is
// in front any more.
var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
	"tcsh": true, "csh": true}

// runsAShell says whether argv starts a shell, a login shell's leading '-' aside.
func runsAShell(argv []string) bool {
	return len(argv) > 0 && shells[strings.TrimPrefix(filepath.Base(argv[0]), "-")]
}

// lastLines is the text of the last count lines of t's screen that are not blank at its bottom,
// each without its trailing spaces; a wide character's second cell adds nothing.
func lastLines(t *term.Terminal, count int) []string {
	var out []string
	for y := t.Rows() - 1; y >= 0 && len(out) < count; y-- {
		var b strings.Builder
		for _, c := range t.Line(y).Cells {
			switch {
			case c.Flags&term.CellWideTail != 0:
			case c.IsBlank():
				b.WriteByte(' ')
			default:
				b.WriteString(t.CellText(c))
			}
		}
		line := strings.TrimRight(b.String(), " ")
		if line == "" && len(out) == 0 {
			continue
		}
		out = append(out, line)
	}
	slices.Reverse(out)
	return out
}

// relisten listens on a new socket at e's removed one's path, in its folder made again (only the
// user's, as Place.Dir makes it), and closes the old listener without unlinking the path.
func (k *keeper) relisten(e *endpoint) error {
	dir := filepath.Dir(e.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := EnsurePrivateDir(dir); err != nil {
		return err
	}
	ln, err := listen(e.path)
	if err != nil {
		return err
	}
	k.lnMu.Lock()
	defer k.lnMu.Unlock()
	if k.finishing {
		ln.Close()
		return nil
	}
	old := e.ln
	e.ln = ln
	if socket, ok := old.(*net.UnixListener); ok {
		socket.SetUnlinkOnClose(false)
	}
	return old.Close()
}

// readProgram keeps the program's output and passes it to the connected sessions. It reads no more
// while the holder has more than holderQueued bytes waiting, as a client's socket held the
// program back before; a watcher never holds it back (peer.go).
func (k *keeper) readProgram(done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, 64*1024)
	for {
		n, err := k.ptmx.Read(buf)
		if n > 0 {
			k.out.Lock()
			k.keep(buf[:n])
			k.deliver(encodeBytes(frameOutput, buf[:n])...)
			holder := k.holder
			k.out.Unlock()
			if holder != nil {
				holder.waitBelow(holderQueued)
			}
		}
		if err != nil {
			return // EIO once every holder of the terminal has closed it, or the master was closed
		}
	}
}

// newModes makes the emulator that follows the modes of output cut off: no history, and the
// terminal's size, so a command line typed on the screen reads as it was typed (shell
// integration's running command, which ModeSequences carries too). It has no clock: the output it
// reads was written earlier, so a command's start is known only when the shell gave it.
func newModes(cols, rows int) *term.Terminal { return term.New(modesOptions(cols, rows)) }

func modesOptions(cols, rows int) term.Options {
	host, _ := os.Hostname()
	return term.Options{Columns: cols, Rows: rows, HistoryLimit: -1, Hostname: host}
}

func (k *keeper) keep(b []byte) {
	_, _ = k.screen.Write(b)
	k.drawn = true
	k.kept = append(k.kept, b...)
	if len(k.kept) > 2*keptBytes {
		cut := len(k.kept) - keptBytes
		k.feedModes(cut)
		k.kept = append([]byte(nil), k.kept[cut:]...)
		k.fed -= cut
		k.sizes = sizesFrom(k.sizes, cut)
	}
}

// sizeMark says that the kept output from offset at on was written with the terminal cols × rows.
type sizeMark struct{ at, cols, rows int }

// markSize records that the output kept from now on is written at cols × rows; out is held.
func (k *keeper) markSize(cols, rows int) {
	last := &k.sizes[len(k.sizes)-1]
	switch {
	case last.cols == cols && last.rows == rows:
	case last.at < len(k.kept):
		k.sizes = append(k.sizes, sizeMark{at: len(k.kept), cols: cols, rows: rows})
	case len(k.sizes) > 1 && k.sizes[len(k.sizes)-2].cols == cols && k.sizes[len(k.sizes)-2].rows == rows:
		// Back to the size before, with no output written at the one between: that one goes.
		k.sizes = k.sizes[:len(k.sizes)-1]
	default:
		last.cols, last.rows = cols, rows // no output was written at the previous size
	}
}

// sizesFrom is marks for kept output whose first cut bytes were dropped: the size in effect at
// cut becomes the first mark, at 0.
func sizesFrom(marks []sizeMark, cut int) []sizeMark {
	i := 0
	for i+1 < len(marks) && marks[i+1].at <= cut {
		i++
	}
	out := append([]sizeMark(nil), marks[i:]...)
	for j := range out {
		out[j].at = max(out[j].at-cut, 0)
	}
	return out
}

// sizeAt is the mark in effect at offset off of kept, and the offset where the next one starts
// (past the end of kept when none does).
func (k *keeper) sizeAt(off int) (sizeMark, int) {
	i := 0
	for i+1 < len(k.sizes) && k.sizes[i+1].at <= off {
		i++
	}
	if i+1 < len(k.sizes) {
		return k.sizes[i], k.sizes[i+1].at
	}
	return k.sizes[i], len(k.kept) + 1
}

// feedModes has the modes emulator read the kept output up to offset end, each stretch at the
// size it was written at.
func (k *keeper) feedModes(end int) {
	for k.fed < end {
		size, next := k.sizeAt(k.fed)
		stop := min(end, next)
		k.modes.Resize(size.cols, size.rows)
		k.modes.Write(k.kept[k.fed:stop])
		k.fed = stop
	}
}

// replayPart is a stretch of a replay and the terminal's size it was written at.
type replayPart struct {
	cols, rows int
	out        []byte
}

// keptOutput is what a new session is replayed: the modes set before it, then the last keptBytes
// of output from a clean place (or from where an earlier replay started, if that is later), in
// stretches of one size each. The last part has the terminal's size now, with no output if nothing
// was written since it changed.
func (k *keeper) keptOutput() []replayPart {
	start := 0
	if len(k.kept) > keptBytes {
		start = len(k.kept) - len(CleanStart(k.kept[len(k.kept)-keptBytes:]))
	}
	start = max(start, k.fed)
	k.feedModes(start)
	var parts []replayPart
	for off := start; ; {
		size, next := k.sizeAt(off)
		end := min(next, len(k.kept))
		parts = append(parts, replayPart{cols: size.cols, rows: size.rows, out: k.kept[off:end]})
		if next > len(k.kept) {
			break // the size now
		}
		off = end
	}
	parts[0].out = append(k.modes.ModeSequences(), parts[0].out...)
	return parts
}

// KeptBytes is how much output a keeper keeps (and so replays at most): a replay this long was cut.
const KeptBytes = keptBytes

// cleanSearch is how far CleanStart looks for a clean place.
const cleanSearch = 64 << 10

// CleanStart drops the start of output that was cut at an arbitrary byte, up to the first escape
// or newline — a place where a terminal parser is in its ground state — so that no tail of an
// escape sequence or of a UTF-8 character is shown as text. Without one in the first 64 KiB it
// only drops UTF-8 continuation bytes.
func CleanStart(b []byte) []byte {
	limit := min(len(b), cleanSearch)
	for i := 0; i < limit; i++ {
		if b[i] == 0x1b {
			return b[i:]
		}
		if b[i] == '\n' {
			return b[i+1:]
		}
	}
	i := 0
	for i < len(b) && b[i]&0xc0 == 0x80 {
		i++
	}
	return b[i:]
}

// waitProgram records the exit once the program ended and its output was read (or briefly after:
// a background process may hold the terminal open).
func (k *keeper) waitProgram(readDone <-chan struct{}) {
	exit := exitOf(k.cmd.Wait())
	select {
	case <-readDone:
	case <-time.After(200 * time.Millisecond):
	}
	k.events.note("program ended: code %d %s", exit.Code, exit.Signal)
	k.out.Lock()
	defer k.out.Unlock()
	k.exit = &exit
	k.describe()
	if f, err := encodeJSON(frameExit, exit); err == nil {
		k.deliver(f)
	}
	if !k.connected() {
		k.idle = time.AfterFunc(exitedIdle, func() { k.finish(EndExpired, nil) })
	}
}

func exitOf(err error) Exit {
	if err == nil {
		return Exit{}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return Exit{Code: -1, Signal: unix.SignalName(ws.Signal())}
		}
		return Exit{Code: exit.ExitCode()}
	}
	return Exit{Code: -1}
}

// session serves one connection until it closes or another takes over: on the first socket it
// takes the keyboard from the holder, who is TakenOver; on the second (join) it watches.
func (k *keeper) session(conn net.Conn, join bool) {
	k.out.Lock()
	if !join && k.holder != nil {
		k.takenOver(k.holder)
		k.holder = nil
	}
	if k.idle != nil {
		k.idle.Stop()
		k.idle = nil
	}
	p := newPeer(conn, join)
	frames, err := k.replay()
	if err != nil {
		k.out.Unlock()
		p.close()
		return
	}
	p.push(frames...)
	if join {
		k.watchers = append(k.watchers, p)
		k.events.note("a client joined to watch")
	} else {
		k.holder = p
		k.events.note("a client attached")
	}
	k.describe() // attached, or watched, now
	k.tellPresent()
	k.out.Unlock()
	select {
	case k.infoNow <- struct{}{}:
	default:
	}

	r := bufio.NewReader(conn)
	for {
		kind, payload, err := readFrame(r)
		if err != nil {
			k.out.Lock()
			k.dropConn(p)
			k.out.Unlock()
			return
		}
		k.out.Lock()
		holds := k.holder == p
		k.out.Unlock()
		switch kind {
		case frameInput:
			if holds {
				_, _ = k.ptmx.Write(payload)
			}
		case frameResize:
			if cols, rows, ok := sizeOf(payload); ok {
				// Under out, so the output read after the program was told is kept at the new size.
				k.out.Lock()
				p.cols, p.rows = cols, rows
				if k.holder == p {
					k.resize(cols, rows)
				}
				k.out.Unlock()
			}
		case frameMeta:
			var m Meta
			if holds && json.Unmarshal(payload, &m) == nil {
				k.out.Lock()
				// Who started the program is the keeper's to keep (Meta.StartedBy).
				m.StartedBy = k.meta.StartedBy
				k.meta = m
				k.describe()
				k.out.Unlock()
			}
		case frameHangup:
			if !holds {
				// A watcher hanging up only leaves.
				k.out.Lock()
				k.dropConn(p)
				k.out.Unlock()
				return
			}
			var by *Who
			if len(payload) > 0 {
				var who Who
				if json.Unmarshal(payload, &who) == nil && who != (Who{}) {
					by = &who
				}
			}
			k.finish(EndClosed, by)
			return
		case frameStop:
			if holds {
				go k.stop()
			}
		case frameWho:
			var who Who
			if json.Unmarshal(payload, &who) == nil {
				k.events.note("the client is %q (%s)", who.Client, who.ClientID)
				k.out.Lock()
				p.who, p.aware = who, true
				k.describe()
				k.tellPresent()
				k.out.Unlock()
			}
		case frameTake:
			k.out.Lock()
			k.take(p)
			k.out.Unlock()
		case frameFront:
			k.lease(p, payload)
		}
	}
}

// replay is what a new connection is sent first: Hello, the kept output at the sizes it was
// written at, ReplayEnd, and the Exit if the program ended; out is held.
func (k *keeper) replay() ([][]byte, error) {
	hello := Hello{Version: Version, KeeperID: k.cfg.KeeperID, PID: k.cmd.Process.Pid, Columns: k.cols, Rows: k.rows,
		Started: k.started.Unix(), Meta: k.meta, Exited: k.exit != nil}
	f, err := encodeJSON(frameHello, hello)
	if err != nil {
		return nil, err
	}
	frames := [][]byte{f}
	for _, part := range k.keptOutput() {
		size, _ := encodeFrame(frameSize, sizePayload(part.cols, part.rows))
		frames = append(frames, size)
		frames = append(frames, encodeBytes(frameOutput, part.out)...)
	}
	end, _ := encodeFrame(frameReplayEnd, nil)
	frames = append(frames, end)
	if k.exit != nil {
		if f, err := encodeJSON(frameExit, *k.exit); err == nil {
			frames = append(frames, f)
		}
	}
	return frames, nil
}

// peers are the holder, if any, and the watchers; out is held.
func (k *keeper) peers() []*peer {
	out := make([]*peer, 0, len(k.watchers)+1)
	if k.holder != nil {
		out = append(out, k.holder)
	}
	return append(out, k.watchers...)
}

// connected says some client is connected, holding or watching; out is held.
func (k *keeper) connected() bool { return k.holder != nil || len(k.watchers) > 0 }

// deliver queues frames for every connection, and drops a watcher that has fallen too far behind
// (peer.go); out is held.
func (k *keeper) deliver(frames ...[]byte) {
	var behind []*peer
	for _, p := range k.peers() {
		p.push(frames...)
		if p != k.holder && p.waiting() > watcherQueued {
			behind = append(behind, p)
		}
	}
	for _, p := range behind {
		k.dropConn(p)
	}
}

// resize makes cols × rows the terminal's size, and tells the watchers, who draw at it; out is held.
func (k *keeper) resize(cols, rows int) {
	_ = pty.Setsize(k.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	k.cols, k.rows = cols, rows
	k.markSize(cols, rows)
	k.screen.Resize(cols, rows)
	k.drawn = true
	f, _ := encodeFrame(frameSize, sizePayload(cols, rows))
	for _, w := range k.watchers {
		w.push(f)
	}
}

// take gives p, a watcher, the keyboard. The holder watches from then on if it can, else it is
// TakenOver; the terminal takes p's size, once p asked for one. Out is held.
func (k *keeper) take(p *peer) {
	i := slices.Index(k.watchers, p)
	if i < 0 {
		return // the holder already, or gone
	}
	k.watchers = slices.Delete(k.watchers, i, i+1)
	if old := k.holder; old != nil {
		if old.aware {
			k.watchers = append(k.watchers, old)
		} else {
			k.takenOver(old)
		}
	}
	k.holder = p
	if p.cols > 0 && (p.cols != k.cols || p.rows != k.rows) {
		k.resize(p.cols, p.rows)
	}
	k.describe()
	k.tellPresent()
}

// takenOver tells p another client took the keeper over and closes it once that is written: so a
// client knows its program still runs. A client that stopped reading must not hold the keeper up
// (closeWithin). Out is held.
func (k *keeper) takenOver(p *peer) {
	k.events.note("client %q taken over", p.who.Client)
	f, _ := encodeFrame(frameTakenOver, nil)
	p.push(f)
	p.closeAfter()
}

// tellPresent sends Present to every connection that can watch; out is held.
func (k *keeper) tellPresent() {
	present := Present{Watchers: k.watcherNames()}
	if k.holder != nil {
		who := k.holder.who
		present.Holder = &who
	}
	for _, p := range k.peers() {
		if !p.aware {
			continue
		}
		present.Holding = p == k.holder
		if f, err := encodeJSON(framePresent, present); err == nil {
			p.push(f)
		}
	}
}

// watcherNames are the watchers as they named themselves; out is held.
func (k *keeper) watcherNames() []Who {
	var out []Who
	for _, w := range k.watchers {
		out = append(out, w.who)
	}
	return out
}

// dropConn forgets p and closes it; out is held.
func (k *keeper) dropConn(p *peer) {
	p.close()
	switch i := slices.Index(k.watchers, p); {
	case k.holder == p:
		k.holder = nil
	case i >= 0:
		k.watchers = slices.Delete(k.watchers, i, i+1)
	default:
		return // forgotten already
	}
	k.events.note("client %q gone", p.who.Client)
	k.describe() // no longer attached, or watched
	k.tellPresent()
	if k.exit != nil && k.idle == nil && !k.connected() {
		k.idle = time.AfterFunc(exitedIdle, func() { k.finish(EndExpired, nil) })
	}
}

// stop ends the program and whatever is left in its process group — gently (SIGTERM, then
// SIGKILL after stopGrace) — and then the keeper. The group, not the program, is waited for: a
// server's children outlive it when it dies first (MLflow's uvicorn workers kept serving after its
// main process was killed, 2026-10-19). The program's Exit reaches the connected session before the
// keeper ends.
func (k *keeper) stop() {
	if p := k.cmd.Process; p != nil {
		if !signalGroupUntilGone(p.Pid, syscall.SIGTERM, stopGrace) {
			signalGroupUntilGone(p.Pid, syscall.SIGKILL, 2*time.Second)
		}
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		k.out.Lock()
		reported := k.exit != nil
		k.out.Unlock()
		if reported {
			break
		}
	}
	k.finish(EndStopped, nil)
}

// signalGroupUntilGone sends sig to process group pgid and reports whether the group was empty
// within wait (at once when it already was).
func signalGroupUntilGone(pgid int, sig syscall.Signal, wait time.Duration) bool {
	gone := func() bool { return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) }
	if gone() {
		return true
	}
	_ = syscall.Kill(-pgid, sig)
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if gone() {
			return true
		}
	}
	return gone()
}

// finish hangs up the program and ends the keeper, leaving its End: how, and by whom when a client
// named itself.
func (k *keeper) finish(how string, by *Who) {
	k.finishWith(End{How: how, By: by})
}

// finishWith is finish with end's How, By and Signal.
func (k *keeper) finishWith(end End) {
	// Under out, so a description being written ends first and none is written after the file goes: the program's
	// end, which the hangup brings, wrote it back, and every keeper hung up left its description behind (2026-10-25).
	k.out.Lock()
	if k.ending {
		k.out.Unlock()
		return // ending already, for another reason
	}
	running := k.exit == nil
	k.ending = true
	end.Version, end.KeeperID, end.Ended, end.Exit = Version, k.cfg.KeeperID, time.Now().Unix(), k.exit
	peers := k.peers()
	k.out.Unlock()
	k.events.note("ending: %s", end.How)
	writeEnd(EndPath(k.cfg.Socket), end, true)
	k.events.remove()
	if p := k.cmd.Process; p != nil && running {
		_ = syscall.Kill(-p.Pid, syscall.SIGHUP)
	}
	k.lnMu.Lock()
	k.finishing = true
	for _, e := range k.endpoints {
		e.ln.Close()
		_ = os.Remove(e.path)
	}
	k.lnMu.Unlock()
	_ = os.Remove(DescriptionPath(k.cfg.Socket))
	removeAgent(k.cfg.Socket)
	// What was queued — a stopped program's Exit — reaches the clients before the keeper ends.
	deadline := time.Now().Add(closeWithin)
	for _, p := range peers {
		p.closeAfter()
	}
	for _, p := range peers {
		p.waitClosed(deadline)
	}
	_ = k.ptmx.Close()
	os.Exit(0)
}

// describe writes the keeper's description beside its socket (Description); out is held. It is
// written to a temporary name and renamed, so a reader never sees half of one.
func (k *keeper) describe() {
	if k.ending {
		return
	}
	d := Description{Version: Version, KeeperID: k.cfg.KeeperID, KeeperPID: os.Getpid(), PID: k.cmd.Process.Pid,
		Started: k.started.Unix(), Meta: k.meta, Exited: k.exit != nil, Build: k.cfg.Build, Attached: k.holder != nil,
		Exit: k.exit, Preview: k.preview, FullScreen: k.fullScreen, ScreenTitle: k.title, Watchers: k.watcherNames(),
		Status: k.status.State, StatusKind: k.status.Kind, Push: k.observer != nil}
	b, err := json.Marshal(d)
	if err != nil {
		return
	}
	path := DescriptionPath(k.cfg.Socket)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

// writeEnd writes end to path, under a temporary name renamed into place so a reader never sees
// half of one; unless replace, an End already there stays (a listing finding a keeper gone must
// not write over what the keeper said as it ended).
func writeEnd(path string, end End, replace bool) {
	b, err := json.Marshal(end)
	if err != nil {
		return
	}
	if !replace {
		if _, err := os.Lstat(path); err == nil {
			return
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

// watchInfo reads what runs in the shell while a client is connected, and sends it to each when it
// changed for it, until the program ends. While the program in the shell says it is working or
// waiting for the user, it reads with no client too: once the shell is back in front, whatever said
// so has ended, and the description says so (a client's session ends the same records itself, on
// the Info it is sent).
func (k *keeper) watchInfo() {
	reader := paneinfo.NewReader()
	shell := runsAShell(k.cmd.Args)
	timer := time.NewTimer(infoEvery)
	for {
		select {
		case <-timer.C:
		case <-k.infoNow:
			timer.Stop()
		}
		timer.Reset(infoEvery)
		k.out.Lock()
		connected, directory, ended := k.connected(), k.meta.Directory, k.exit != nil
		busy := shell && active(k.screen.Status())
		k.out.Unlock()
		if ended {
			return
		}
		if !connected && !busy {
			continue
		}
		read, err := reader.Read(k.cmd.Process.Pid, directory)
		if err != nil {
			continue
		}
		if busy && read.ProgramPID == k.cmd.Process.Pid {
			k.out.Lock()
			k.screen.EndActive()
			if status := k.screen.Status(); !sameStatus(status, k.status) {
				k.status = status
				k.describe()
			}
			k.out.Unlock()
		}
		info := infoOf(read)
		f, err := encodeJSON(frameInfo, info)
		if err != nil {
			continue
		}
		k.out.Lock()
		for _, p := range k.peers() {
			if p.lastInfo == nil || !reflect.DeepEqual(*p.lastInfo, info) {
				p.push(f)
				p.lastInfo = &info
			}
		}
		k.out.Unlock()
	}
}

// infoOf is paneinfo's reading as the protocol carries it.
func infoOf(read paneinfo.Info) Info {
	info := Info{Program: read.Program, ProgramPID: read.ProgramPID, Directory: read.Directory, Branch: read.Branch,
		Remote: read.Remote, Resume: read.Resume}
	for _, p := range read.Ports {
		info.Ports = append(info.Ports, InfoPort{Port: p.Port, Everywhere: p.Everywhere, Process: p.Process})
	}
	return info
}

// refreshTimes touches the sockets, the description and the two folders above them every
// refreshEvery, so that age-based cleaning of /tmp (systemd-tmpfiles) never takes them while the
// keeper runs.
func (k *keeper) refreshTimes() {
	dir := filepath.Dir(k.cfg.Socket)
	for {
		now := time.Now()
		for _, p := range []string{k.cfg.Socket, JoinPath(k.cfg.Socket), DescriptionPath(k.cfg.Socket), dir, filepath.Dir(dir)} {
			_ = os.Chtimes(p, now, now)
		}
		time.Sleep(refreshEvery)
	}
}

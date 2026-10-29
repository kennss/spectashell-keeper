// termhost.go
// Author: Kennt Kim
// Created: 2026-10-14 · Last updated: 2026-10-29
//
// Purpose: one live terminal — a program reached through a Link (link.go), the emulator that models
// its screen (package term), and the plumbing between them (Docs/DESIGN.md §3, §5).
//
// Notes:
// - Taken from SiliconDesk's host/internal/termhost/termhost.go on 2026-10-23. Changed: the
//   program is reached through a Link, not only a keeper connection, and is started by the
//   engine's user (launch.go, keeper.Start); a link that ends without the program's exit is
//   reported as lost (OnLost), never as the program's end — SiliconDesk's reported it as an exit
//   with code -1, which made a shell another device took over look ended (DESIGN.md §10).
// - Every link begins with a replay: a keeper's of the program's recent output, which rebuilds
//   the screen in a fresh emulator; a link with no memory (a plain SSH shell) an empty one.
//   Replies the emulator would send for replayed output (cursor reports and the like) are not
//   sent. Each stretch of a keeper's replay is drawn at the size it was written at (its Size
//   events), as a client connected all along drew it; the screen then returns to its size. After
//   the replay a full-screen program (alternate screen) gets its size nudged so it
//   redraws.
// - A resize changes the screen as output does, so it calls Config.OnChange too: a program that
//   answers it with nothing (a shell at its prompt) left the views at the old size until its next
//   output (2026-10-25).
// - Goroutines: a reader feeds the link's output into the emulator and calls Config.OnChange,
//   and reports the exit through Config.OnExit or the loss through Config.OnLost; a writer drains
//   queued input (user input and the emulator's replies) to the link. The emulator is guarded by
//   mu; everything else the owner calls (Input, Resize, Frame, Close, Detach) takes mu too, so the
//   owner needs no locking.
// - OnChange, OnExit, OnLost, OnClipboard, OnNotify, OnBell, OnStatus and OnCommandFinished run on
//   those goroutines (or the caller's, for EndActive) and must not block; OnChange is never called
//   with mu held. Text the program put on the clipboard, and notifications and bells it asked for,
//   while its old output was replayed are not passed on: they belong to an earlier time. Its status
//   is: after the replay, once, as `restored` (state as found, not news — SiliconDesk's SDTERM-2.md
//   §3.2). Commands that ended in the replay are not reported either, and a command begun in it has
//   no known start unless the shell gave one (shell integration).
// - Close hangs the program up (SIGHUP, through the link); Detach leaves it to its keeper, when
//   the link has one, for the next attach.
// - Watching (2026-10-26, Docs/DESIGN.md §6.9): a terminal on a link that joined a keeper
//   (Config.Watch) watches until it takes the keyboard (Take), and one that held it watches once
//   another takes it (the keeper's Present). Watching, its screen has the program's size, which the
//   keeper sends whenever it changes, not the views'; what the user types and the emulator's
//   answers go nowhere; and the views' size is only told to the keeper, for when this terminal
//   holds the keyboard — then the screen takes it. Every Joinable link is told who this client is
//   (SayWho), so a keeper that can share its program lets this one watch rather than take it away.
// - Input that the program does not read piles up to maxPendingInput bytes; beyond that new input
//   is dropped rather than holding memory without bound.

//go:build unix

package termhost

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/kennss/spectashell-keeper/gen/terminal/v1"
	"github.com/kennss/spectashell-keeper/keeper"
	"github.com/kennss/spectashell-keeper/term"
)

const maxPendingInput = 4 << 20

// Config is the owner's part of a terminal: how the emulator answers, and the callbacks.
type Config struct {
	// Client is this device's name, told to the link's keeper (keeper.Meta.Client) so whoever
	// lists it later knows who holds it; empty: nothing is told.
	Client string
	// ClientID tells this device apart from others of its name (keeper.Meta.ClientID).
	ClientID     string
	HistoryLimit int
	// Colors answer the program's colour queries (term.Colors).
	Colors term.Colors
	// Name is how the terminal names itself to a program that asks (term.Options.Name).
	Name string
	// Hostname is the name of the machine the program runs on, to tell its own directories from
	// another host's (OSC 7); empty: this machine's.
	Hostname string
	OnChange func()
	// OnExit reports the program's end; the terminal keeps its last screen.
	OnExit func(code int, signal string)
	// OnLost reports that the link ended without the program's exit: another device took the
	// keeper over (takenOver), or the connection or the keeper went away (err). The program may
	// still run; the owner decides whether to attach again.
	OnLost func(takenOver bool, err error)
	// OnClipboard receives text the program puts on the clipboard (OSC 52); nil ignores it.
	OnClipboard func(text string)
	// OnNotify receives a notification the program asked for (OSC 9, 777, 99); nil ignores it.
	OnNotify func(title, body string)
	// OnBell is called at each bell (BEL) the program rings, in order with the notifications; nil
	// ignores bells.
	OnBell func()
	// OnStatus receives the program's status when it changes (OSC 7501, OSC 9;4), and once after
	// the replay when there is one (restored); nil ignores it.
	OnStatus func(status term.Status, restored bool)
	// OnCommandFinished receives each command shell integration reports ended (OSC 133); nil
	// ignores them.
	OnCommandFinished func(term.CommandFinished)
	// Triggers are looked for from the start, replayed output included (term.Trigger); OnTriggered
	// receives the lines that match one that notifies — never in replayed output.
	Triggers    []term.Trigger
	OnTriggered func(term.Triggered)
	// OnInfo receives what runs in the program's shell, from a link that reads it (a keeper's Info);
	// nil ignores it.
	OnInfo func(keeper.Info)
	// Watch says the link joined its keeper to watch (keeper.DialJoin): the terminal watches until
	// it takes the keyboard.
	Watch bool
	// OnPresent receives who holds the keyboard and who watches, from a keeper that shares its
	// program; nil ignores it.
	OnPresent func(keeper.Present)
}

// Terminal is a running (or exited) program reached through a link.
type Terminal struct {
	mu        sync.Mutex
	emu       *term.Terminal
	link      Link
	info      LinkInfo
	replaying bool
	meta      keeper.Meta
	// who is this client (Config.Client, ClientID), named to the keeper when it hangs up.
	who keeper.Who
	// onChange is Config.OnChange, for the changes the owner makes (Resize).
	onChange func()
	// watching says another client holds the keyboard: input goes nowhere, and the screen has the
	// program's size. Read without mu by reply, which runs with it held.
	watching atomic.Bool
	// viewCols and viewRows are the size the owner last asked for (Resize); 0 before it asked.
	viewCols, viewRows int

	inMu      sync.Mutex
	inCond    *sync.Cond
	inPending [][]byte
	inBytes   int
	inClosed  bool

	closeOnce sync.Once
}

// Attach starts a terminal on a link: the emulator, the reader and the writer. The link's replay
// comes first and rebuilds the screen silently.
func Attach(link Link, info LinkInfo, cfg Config) *Terminal {
	cols, rows := info.Columns, info.Rows
	if cols <= 0 || rows <= 0 {
		cols, rows = term.DefaultColumns, term.DefaultRows
	}
	t := &Terminal{link: link, info: info, replaying: true, meta: info.Meta, onChange: cfg.OnChange,
		who: keeper.Who{Client: cfg.Client, ClientID: cfg.ClientID}}
	t.watching.Store(cfg.Watch)
	if j, ok := link.(Joinable); ok {
		_ = j.SayWho(t.who)
	}
	if !cfg.Watch && cfg.Client != "" && (t.meta.Client != cfg.Client || t.meta.ClientID != cfg.ClientID) {
		// The keeper says who holds it from now on (a link without a keeper ignores it).
		t.meta.Client, t.meta.ClientID = cfg.Client, cfg.ClientID
		_ = link.SetMeta(t.meta)
	}
	t.inCond = sync.NewCond(&t.inMu)
	clipboard := func(text string) {
		// Called from the emulator, under mu.
		if !t.replaying && cfg.OnClipboard != nil {
			cfg.OnClipboard(text)
		}
	}
	notify := func(title, body string) {
		if !t.replaying && cfg.OnNotify != nil {
			cfg.OnNotify(title, body)
		}
	}
	bell := func() {
		if !t.replaying && cfg.OnBell != nil {
			cfg.OnBell()
		}
	}
	var emu *term.Terminal
	status := func() {
		if !t.replaying && cfg.OnStatus != nil {
			cfg.OnStatus(emu.Status(), false)
		}
	}
	finished := func(c term.CommandFinished) {
		if !t.replaying && cfg.OnCommandFinished != nil {
			cfg.OnCommandFinished(c)
		}
	}
	triggered := func(h term.Triggered) {
		if !t.replaying && cfg.OnTriggered != nil {
			cfg.OnTriggered(h)
		}
	}
	// Replayed output was written at some earlier time: a command begun in it has no known start
	// unless the shell gave one (term.Options.Now).
	now := func() time.Time {
		if t.replaying {
			return time.Time{}
		}
		return time.Now()
	}
	host := cfg.Hostname
	if host == "" {
		host, _ = os.Hostname()
	}
	emu = term.New(term.Options{Columns: cols, Rows: rows, HistoryLimit: cfg.HistoryLimit, Colors: cfg.Colors,
		Reply: t.reply, Clipboard: clipboard, Notify: notify, Bell: bell, StatusChanged: status, CommandFinished: finished,
		Now: now, Hostname: host, Name: cfg.Name, Triggered: triggered})
	emu.SetTriggers(cfg.Triggers)
	t.emu = emu
	go t.writeLoop()
	go func() {
		exited := false
		// The replay is gathered and written at its end: a replay of keeper.KeptBytes was cut,
		// and keepers of earlier builds cut it anywhere (keeper.CleanStart).
		var replay []replayPart
		err := link.ReadEvents(keeper.Events{
			Output: func(b []byte) {
				t.mu.Lock()
				if t.replaying {
					if len(replay) == 0 {
						replay = append(replay, replayPart{})
					}
					last := &replay[len(replay)-1]
					last.out = append(last.out, b...)
					t.mu.Unlock()
					return
				}
				t.emu.Write(b)
				t.mu.Unlock()
				cfg.OnChange()
			},
			Size: func(cols, rows int) {
				t.mu.Lock()
				if t.replaying {
					replay = append(replay, replayPart{cols: cols, rows: rows})
					t.mu.Unlock()
					return
				}
				// The program's size changed while this terminal watches.
				changed := validSize(cols, rows) && (cols != t.emu.Columns() || rows != t.emu.Rows())
				if changed {
					t.emu.Resize(cols, rows)
				}
				t.mu.Unlock()
				if changed {
					cfg.OnChange()
				}
			},
			Present: func(p keeper.Present) {
				t.present(p)
				if cfg.OnPresent != nil {
					cfg.OnPresent(p)
				}
				cfg.OnChange()
			},
			ReplayEnd: func() {
				t.mu.Lock()
				t.writeReplay(replay)
				replay = nil
				found := t.emu.Status()
				t.mu.Unlock()
				t.replayed()
				if cfg.OnStatus != nil && (found.State != "" || found.Progress >= 0 || found.Indeterminate) {
					cfg.OnStatus(found, true)
				}
				cfg.OnChange()
			},
			Exit: func(e keeper.Exit) {
				exited = true
				cfg.OnExit(e.Code, e.Signal)
			},
			Info: func(i keeper.Info) {
				if cfg.OnInfo != nil {
					cfg.OnInfo(i)
				}
			},
		})
		t.stopInput()
		if !exited && cfg.OnLost != nil {
			cfg.OnLost(errors.Is(err, keeper.ErrTakenOver), err)
		}
	}()
	return t
}

// Status returns the program's status now (term.Status).
func (t *Terminal) Status() term.Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emu.Status()
}

// EndActive ends the program's working and blocked status records: the owner saw the program
// that set them end (term.Terminal.EndActive). OnStatus follows when that changed anything.
func (t *Terminal) EndActive() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.emu.EndActive()
}

// SetTriggers replaces the patterns looked for in the program's output (term.Trigger).
func (t *Terminal) SetTriggers(triggers []term.Trigger) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.emu.SetTriggers(triggers)
}

// CommandRanElsewhere says the program in front runs on another host (term.Terminal's): true when a
// prompt's mark changed, for a frame to carry it.
func (t *Terminal) CommandRanElsewhere() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emu.CommandRanElsewhere()
}

// PID is the program's process id (the shell, a session leader of its own), or 0 when not known.
func (t *Terminal) PID() int { return t.info.PID }

// replayPart is a stretch of the replay and the terminal's size it was written at (0: not known,
// from a keeper of an earlier build).
type replayPart struct {
	cols, rows int
	out        []byte
}

// writeReplay rebuilds the screen from the replay, each stretch at the size it was written at, as
// a client connected all along drew it (Docs/KEEPER.md §2: output written for one width and drawn
// at another wraps where the program did not mean it to), then returns to the screen's size; mu
// is held.
func (t *Terminal) writeReplay(parts []replayPart) {
	total := 0
	for _, p := range parts {
		total += len(p.out)
	}
	if total >= keeper.KeptBytes && len(parts) > 0 {
		parts[0].out = keeper.CleanStart(parts[0].out)
	}
	cols, rows := t.emu.Columns(), t.emu.Rows()
	for _, p := range parts {
		if validSize(p.cols, p.rows) {
			t.emu.Resize(p.cols, p.rows)
		}
		t.emu.Write(p.out)
	}
	t.emu.Resize(cols, rows)
}

// validSize says whether cols × rows is a size a screen may have.
func validSize(cols, rows int) bool { return cols > 0 && rows > 0 && cols <= 1000 && rows <= 1000 }

// replayed ends the replay: replies flow again, and a full-screen program is made to redraw — by
// the terminal that holds the keyboard; a watcher's sizes are only remembered.
func (t *Terminal) replayed() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.replaying = false
	if t.emu.AltScreen() && !t.watching.Load() {
		cols, rows := t.emu.Columns(), t.emu.Rows()
		_ = t.link.Resize(cols, max(1, rows-1))
		_ = t.link.Resize(cols, rows)
	}
}

// reply is the emulator's answer to the program (called with mu held, from Write).
func (t *Terminal) reply(b []byte) {
	if t.replaying || t.watching.Load() {
		return // an answer to old output would arrive as stray input; a watcher's goes nowhere
	}
	t.queueInput(b)
}

// present follows who holds the keyboard. Taking it, the screen takes the views' size, which the
// keeper has been told; giving it up, the screen keeps the program's size until it changes.
func (t *Terminal) present(p keeper.Present) {
	t.mu.Lock()
	defer t.mu.Unlock()
	was := t.watching.Swap(!p.Holding)
	if was && p.Holding && validSize(t.viewCols, t.viewRows) &&
		(t.viewCols != t.emu.Columns() || t.viewRows != t.emu.Rows()) {
		t.emu.Resize(t.viewCols, t.viewRows)
		_ = t.link.Resize(t.viewCols, t.viewRows)
	}
}

// Watching says another client holds the keyboard.
func (t *Terminal) Watching() bool { return t.watching.Load() }

// Take asks the keeper for the keyboard, when this terminal watches; its Present follows. A link
// that cannot share its program ignores it.
func (t *Terminal) Take() {
	if j, ok := t.link.(Joinable); ok && t.watching.Load() {
		_ = j.Take()
	}
}

func (t *Terminal) writeLoop() {
	for {
		t.inMu.Lock()
		for len(t.inPending) == 0 && !t.inClosed {
			t.inCond.Wait()
		}
		if t.inClosed {
			t.inMu.Unlock()
			return
		}
		batch := t.inPending
		t.inPending, t.inBytes = nil, 0
		t.inMu.Unlock()
		for _, b := range batch {
			if err := t.link.Input(b); err != nil {
				return
			}
		}
	}
}

// queueInput hands bytes to the writer without blocking; a watcher's go nowhere.
func (t *Terminal) queueInput(b []byte) {
	if len(b) == 0 || t.watching.Load() {
		return
	}
	t.inMu.Lock()
	defer t.inMu.Unlock()
	if t.inClosed || t.inBytes+len(b) > maxPendingInput {
		return
	}
	t.inPending = append(t.inPending, append([]byte(nil), b...))
	t.inBytes += len(b)
	t.inCond.Signal()
}

// Write queues bytes for the program's input, unchanged (TerminalWrite).
func (t *Terminal) Write(b []byte) { t.queueInput(b) }

// Paste queues text for the program's input as a paste, bracketed when it asked (TerminalWrite
// with paste).
func (t *Terminal) Paste(text string) {
	t.mu.Lock()
	b := t.emu.EncodePaste(text)
	t.mu.Unlock()
	t.queueInput(b)
}

// Input encodes user input for the program according to its modes (PROTOCOL.md §7.5).
func (t *Terminal) Input(in *pb.TerminalInput) {
	t.mu.Lock()
	var b []byte
	switch k := in.GetKind().(type) {
	case *pb.TerminalInput_Text:
		b = []byte(k.Text)
	case *pb.TerminalInput_Key:
		b = t.emu.EncodeKey(term.Key(k.Key.GetCode()), term.Mod(k.Key.GetModifiers()), k.Key.GetText())
	case *pb.TerminalInput_Paste:
		b = t.emu.EncodePaste(k.Paste)
	case *pb.TerminalInput_Mouse:
		m := k.Mouse
		b = t.emu.EncodeMouse(term.MouseAction(m.GetAction()), term.MouseButton(m.GetButton()),
			int(m.GetColumn()), int(m.GetRow()), term.Mod(m.GetModifiers()))
	case *pb.TerminalInput_Focus:
		b = t.emu.EncodeFocus(k.Focus)
	}
	t.mu.Unlock()
	t.queueInput(b)
}

// Resize changes the pseudo-terminal's size (the program gets SIGWINCH) and the screen's, which
// goes to the views at once (Config.OnChange), whether or not the program writes anything. A
// watcher's only tells the keeper, for when it holds the keyboard; its screen keeps the program's.
func (t *Terminal) Resize(cols, rows int) {
	if !validSize(cols, rows) {
		return
	}
	t.mu.Lock()
	t.viewCols, t.viewRows = cols, rows
	if t.watching.Load() {
		_ = t.link.Resize(cols, rows)
		t.mu.Unlock()
		return
	}
	if cols == t.emu.Columns() && rows == t.emu.Rows() {
		t.mu.Unlock()
		return
	}
	t.emu.Resize(cols, rows)
	_ = t.link.Resize(cols, rows)
	t.mu.Unlock()
	if t.onChange != nil {
		t.onChange()
	}
}

// SetColors sets what the program's colour queries answer.
func (t *Terminal) SetColors(c term.Colors) {
	t.mu.Lock()
	t.emu.SetColors(c)
	t.mu.Unlock()
}

// Info returns the title and working directory the program reported.
func (t *Terminal) Info() (title, dir string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emu.Title(), t.emu.WorkingDirectory()
}

// Remember tells the link's keeper the title and directory to describe the terminal with, when
// they change; a link without a keeper ignores it.
func (t *Terminal) Remember(title, dir string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.watching.Load() || title == t.meta.Title && dir == t.meta.Directory {
		return
	}
	t.meta.Title, t.meta.Directory = title, dir
	_ = t.link.SetMeta(t.meta)
}

// RememberRestorationID tells the link's keeper the owner's name for the terminal's window, when it
// changes.
func (t *Terminal) RememberRestorationID(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.watching.Load() || id == t.meta.RestorationID {
		return
	}
	t.meta.RestorationID = id
	_ = t.link.SetMeta(t.meta)
}

// SyncHeld reports whether the program holds synchronized output (DECSET 2026).
func (t *Terminal) SyncHeld() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emu.SyncOutput()
}

// Close hangs up: the program's process group gets SIGHUP, and its keeper, if it has one, ends.
func (t *Terminal) Close() {
	t.closeOnce.Do(func() {
		t.stopInput()
		t.link.Hangup(t.who)
	})
}

// Detach leaves the program to its keeper for the next attach; a link without a keeper ends it.
func (t *Terminal) Detach() {
	t.closeOnce.Do(func() {
		t.stopInput()
		t.link.Close()
	})
}

func (t *Terminal) stopInput() {
	t.inMu.Lock()
	t.inClosed = true
	t.inPending = nil
	t.inCond.Broadcast()
	t.inMu.Unlock()
}

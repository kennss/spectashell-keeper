// prompts.go
// Author: Kennt Kim
// Created: 2026-10-19 · Last updated: 2026-10-23
//
// Purpose: shell integration (Docs/SDTERM-2.md §4, PROTOCOL.md §7.7): the semantic prompt marks
// a shell prints — OSC 133 A (a prompt), B (the command line typed after it), C (the command runs:
// its output starts), D;exit (it ended) — and VS Code's OSC 633 forms of the same. They become
// marks on the lines where each begins (Line.Mark), for the device to jump between prompts and
// copy a command's output, and a CommandFinished for each command that ends.
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/prompts.go on 2026-10-23.
// - Commands are tracked on a stack, one entry per shell, named by its `aid` option: an inner
//   shell's marks inside an outer command (`ssh` to a host with integration) push an entry; when
//   the outer command ends, every entry above it is closed with it (iTerm2's cascade, because
//   `ssh` ends before the inner shell's D could arrive). Without an aid, a prompt inside a running
//   command is taken as a nested shell too; maxCommands bounds the stack.
// - D after a prompt or B with no C is a cancelled line (Ctrl-C at the prompt): no command, no
//   mark. D with no open entry is ignored, and only the first D after a C ends the command
//   (kitty's rule).
// - A continuation prompt (`k=s`, `k=r`, or OSC 133 P with those kinds) is not a new prompt. OSC
//   133 N and P (kind i) are read as A. A does not move to a fresh line: the screen stays as any
//   terminal shows it, and a command's end carries the column its output stopped at instead.
// - The command line is, first, what the shell said: kitty's `cmdline_url=` (percent-encoded) on C,
//   or VS Code's 633;E; else the screen text between B and C (iTerm2's rule).
// - A command's start is the C's `sdterm_started=<unix ms>` when the shell gave it (SiliconDesk's
//   own scripts do), else the time C arrived (Options.Now). Now returns the zero time when the time
//   is not the output's own — a replay after a session change, a keeper's follower reading output
//   it lets go — and a duration is then unknown rather than wrong.
// - Marks are set on the main screen only; the alternate screen has none. A line's mark is
//   replaced, never changed in place, since a history line shares it.
// - ModeSequences re-emits each running command as a C with its aid, command line and start, so a
//   keeper's follower carries a long command past its 1 MiB cut and a new session can still say
//   "finished — 2 h 13 min" (SDTERM-2.md §4.2).
// - A prompt carries the directory the shell reported before it (bash's script prints OSC 7 in
//   PROMPT_COMMAND, before the prompt), so a relative path in a command's output is read from the
//   directory the command ran in, after any later `cd` (SDTERM-2.md §9.1). A command found running
//   on another host (`ssh nas`, CommandRanElsewhere) has its prompt's directory taken away: what it
//   printed names that host's files, and no path in it is looked up here.

package term

import (
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	// maxCommands bounds the stack of shells (nested `ssh` sessions).
	maxCommands = 8
	// maxCommandLine bounds a command line, in characters.
	maxCommandLine = 1024
)

// Mark is what shell integration says about a line.
type Mark struct {
	// A prompt starts on this line.
	Prompt bool
	// A command's output starts on this line, at OutputColumn; Command is its command line.
	Output       bool
	OutputColumn int
	Command      string
	// A command ended on this line, its output before End.Column; nil: none did.
	End *CommandEnd
	// With Prompt: the directory the shell last reported (OSC 7) as the prompt began, where the
	// command after it runs; "" when none was, or it was on another host.
	Directory string
}

// CommandEnd is how a command ended.
type CommandEnd struct {
	// The column the output stopped at.
	Column int
	// The exit status, when the shell gave it.
	ExitCode    int
	HasExitCode bool
	// How long it ran, when known.
	Duration    time.Duration
	HasDuration bool
}

// CommandFinished is a command that ended (Options.CommandFinished).
type CommandFinished struct {
	Command string
	CommandEnd
}

// The phases of a shell's entry.
const (
	phaseIdle   = iota // between commands (after D)
	phasePrompt        // A seen
	phaseInput         // B seen: the user types the command line
	phaseOutput        // C seen: the command runs
)

// shellEntry is one shell's place in the command stack.
type shellEntry struct {
	aid   string
	phase int
	// Where B left the cursor (absolute line, column), for the command line read from the screen.
	inputLine uint64
	inputCol  int
	hasInput  bool
	// The command line the shell gave (633;E before C, or C's own option).
	given   string
	command string
	started time.Time // zero: not known
	// A continuation prompt came while the command line was typed: its B goes on with that line.
	continued bool
	// The absolute line of its prompt's mark, when it has one.
	promptLine uint64
	hasPrompt  bool
}

// osc133 reads a semantic prompt mark (and OSC 633's A-E, the same marks in VS Code's form).
func (t *Terminal) osc133(arg string, vscode bool) {
	fields := strings.Split(arg, ";")
	cmd := fields[0]
	opts := map[string]string{}
	var positional []string
	for _, f := range fields[1:] {
		if k, v, ok := strings.Cut(f, "="); ok {
			opts[k] = v
		} else {
			positional = append(positional, f)
		}
	}
	switch cmd {
	case "A", "N", "P":
		if k := opts["k"]; k == "s" || k == "r" || k == "c" {
			// A continuation or right prompt: not a new prompt, and the command line goes on.
			if e := t.topShell(); e != nil && e.phase == phaseInput {
				e.continued = true
			}
			return
		}
		t.promptStart(opts["aid"])
	case "B":
		t.inputStart(opts["aid"])
	case "C":
		t.outputStart(opts)
	case "D":
		exit, hasExit := 0, false
		if len(positional) > 0 {
			if n, err := strconv.Atoi(positional[0]); err == nil {
				exit, hasExit = n, true
			}
		}
		t.commandEnd(opts["aid"], exit, hasExit)
	case "E":
		if vscode && len(positional) > 0 {
			if e := t.topShell(); e != nil {
				e.given = vscodeUnescape(positional[0])
			}
		}
	}
}

// absLine is the absolute number of screen row y of the main screen.
func (t *Terminal) absLine(y int) uint64 { return t.scrolled + uint64(y) }

// mainLine is the main screen's or the history's line of absolute number abs, or nil.
func (t *Terminal) mainLine(abs uint64) *Line {
	if abs >= t.scrolled {
		if row := abs - t.scrolled; row < uint64(len(t.mainLines)) {
			return t.mainLines[row]
		}
		return nil
	}
	first := t.scrolled - uint64(t.hist.count)
	if abs < first {
		return nil
	}
	return t.hist.at(int(abs - first))
}

// mark changes the mark of the cursor's line (main screen only).
func (t *Terminal) mark(change func(m *Mark)) {
	if t.alt {
		return
	}
	l := t.lines[t.cy]
	var m Mark
	if l.Mark != nil {
		m = *l.Mark
	}
	change(&m)
	l.Mark = &m
	t.touch(l)
}

func (t *Terminal) topShell() *shellEntry {
	if len(t.shells) == 0 {
		return nil
	}
	return t.shells[len(t.shells)-1]
}

// shellIndex is the entry of aid (searched from the top), or -1.
func (t *Terminal) shellIndex(aid string) int {
	if aid == "" {
		return -1
	}
	for i := len(t.shells) - 1; i >= 0; i-- {
		if t.shells[i].aid == aid {
			return i
		}
	}
	return -1
}

// pushShell adds a shell's entry on top of the stack.
func (t *Terminal) pushShell(e *shellEntry) {
	t.shells = append(t.shells, e)
	if len(t.shells) > maxCommands {
		t.shells = t.shells[len(t.shells)-maxCommands:]
	}
}

// promptStart is A: a new prompt. It ends the working and blocked status records of the command
// before it (OSC 7501's rule).
func (t *Terminal) promptStart(aid string) {
	t.EndActive()
	switch i, top := t.shellIndex(aid), t.topShell(); {
	case i >= 0:
		// That shell's prompt again: whatever ran in it, and above it, is over.
		t.shells = t.shells[:i+1]
		t.shells[i].reset(phasePrompt)
	case top == nil:
		t.pushShell(&shellEntry{aid: aid, phase: phasePrompt})
	case top.phase == phaseOutput:
		// A shell started by the running command (ssh to a host with integration).
		t.pushShell(&shellEntry{aid: aid, phase: phasePrompt})
	default:
		top.aid = aid
		top.reset(phasePrompt)
	}
	dir := t.workingDirectory
	if t.remoteDirectory != "" {
		dir = ""
	}
	t.mark(func(m *Mark) { m.Prompt, m.Directory = true, dir })
	if top := t.topShell(); top != nil && !t.alt {
		top.promptLine, top.hasPrompt = t.absLine(t.cy), true
	}
}

// reset starts the entry's next phase; a command keeps the prompt it was typed at.
func (e *shellEntry) reset(phase int) {
	line, has := e.promptLine, e.hasPrompt
	*e = shellEntry{aid: e.aid, phase: phase}
	if phase != phaseIdle {
		e.promptLine, e.hasPrompt = line, has
	}
}

// CommandRanElsewhere says the program in front runs on another host (an ssh session, found from
// the processes): the innermost running command typed at a prompt of this host has that prompt's
// directory taken away, so the paths its output prints are not looked up here (PROTOCOL.md §7.9).
// True when a mark changed.
func (t *Terminal) CommandRanElsewhere() bool {
	for i := len(t.shells) - 1; i >= 0; i-- {
		e := t.shells[i]
		if e.phase != phaseOutput || !e.hasPrompt {
			continue
		}
		l := t.mainLine(e.promptLine)
		if l == nil || l.Mark == nil || !l.Mark.Prompt {
			return false
		}
		if l.Mark.Directory == "" {
			continue // a prompt of the other host (or with no directory): look further out
		}
		m := *l.Mark // replaced, never changed in place: a history line shares it
		m.Directory = ""
		l.Mark = &m
		t.touch(l)
		return true
	}
	return false
}

// inputStart is B: the command line begins at the cursor.
func (t *Terminal) inputStart(aid string) {
	e := t.topShell()
	if i := t.shellIndex(aid); i >= 0 {
		e = t.shells[i]
	}
	if e == nil || e.phase == phaseOutput {
		return
	}
	if e.phase == phaseInput && e.continued {
		e.continued = false
		return
	}
	e.phase = phaseInput
	e.hasInput = !t.alt
	e.inputLine, e.inputCol = t.absLine(t.cy), t.cx
}

// outputStart is C: the command runs, and its output starts at the cursor.
func (t *Terminal) outputStart(opts map[string]string) {
	aid := opts["aid"]
	var e *shellEntry
	switch i, top := t.shellIndex(aid), t.topShell(); {
	case i >= 0:
		e = t.shells[i]
	case top == nil || (top.phase == phaseOutput && aid != ""):
		// A command with no prompt seen: integration loaded mid-command, or a running command
		// re-emitted before a replay (ModeSequences); with an aid of its own inside a running one,
		// a nested shell's.
		e = &shellEntry{aid: aid}
		t.pushShell(e)
	default:
		e = top
	}
	if e.phase == phaseOutput {
		return // a second C for the same command
	}
	e.phase = phaseOutput
	switch {
	case opts["cmdline_url"] != "":
		if s, err := url.PathUnescape(opts["cmdline_url"]); err == nil {
			e.command = commandLine(s)
		}
	case opts["cmdline"] != "":
		e.command = commandLine(opts["cmdline"])
	case e.given != "":
		e.command = commandLine(e.given)
	case e.hasInput && !t.alt:
		e.command = commandLine(t.mainText(e.inputLine, e.inputCol, t.absLine(t.cy), t.cx))
	}
	e.started = t.now()
	if ms, err := strconv.ParseInt(opts["sdterm_started"], 10, 64); err == nil && ms > 0 {
		e.started = time.UnixMilli(ms)
	}
	command, column := e.command, t.cx
	t.mark(func(m *Mark) { m.Output, m.OutputColumn, m.Command = true, column, command })
}

// commandEnd is D: the command of aid's shell (or the top one) ended.
func (t *Terminal) commandEnd(aid string, exit int, hasExit bool) {
	i := len(t.shells) - 1
	if aid != "" {
		i = t.shellIndex(aid)
	}
	if i < 0 {
		return
	}
	e := t.shells[i]
	if e.phase != phaseOutput {
		if e.phase != phaseIdle {
			e.reset(phaseIdle) // a cancelled line
		}
		return
	}
	// Shells it started (an ssh session) ended before it could say so.
	t.shells = t.shells[:i+1]
	end := CommandEnd{Column: t.cx, ExitCode: exit, HasExitCode: hasExit}
	if now := t.now(); !now.IsZero() && !e.started.IsZero() {
		end.Duration, end.HasDuration = max(now.Sub(e.started), 0), true
	}
	command := e.command
	e.reset(phaseIdle)
	t.mark(func(m *Mark) { m.End = &end })
	if t.onCommand != nil {
		t.onCommand(CommandFinished{Command: command, CommandEnd: end})
	}
}

// now is the time output arrives, or zero when it is not known (Options.Now).
func (t *Terminal) now() time.Time {
	if t.clock == nil {
		return time.Time{}
	}
	return t.clock()
}

// mainText is the text of the main screen and history from (fromLine, fromCol) to (toLine, toCol),
// exclusive: wrapped lines joined, others on lines of their own.
func (t *Terminal) mainText(fromLine uint64, fromCol int, toLine uint64, toCol int) string {
	if toLine < fromLine || toLine-fromLine > 64 {
		return ""
	}
	var b strings.Builder
	for abs := fromLine; abs <= toLine; abs++ {
		l := t.mainLine(abs)
		if l == nil {
			continue
		}
		start, end := 0, len(l.Cells)
		if abs == fromLine {
			start = fromCol
		}
		if abs == toLine {
			end = min(end, toCol)
		}
		var line strings.Builder
		for x := start; x < end && x < len(l.Cells); x++ {
			c := l.Cells[x]
			if c.Flags&CellWideTail != 0 {
				continue
			}
			if text := t.cellText(c); text != "" {
				line.WriteString(text)
			} else {
				line.WriteByte(' ')
			}
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		if abs != toLine && !l.Wrapped {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// commandLine is a command line as shown: whitespace and control characters each run become one
// space, cut to maxCommandLine characters.
func commandLine(s string) string {
	var b strings.Builder
	n, space := 0, false
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == unicode.ReplacementChar {
			space = b.Len() > 0
			continue
		}
		if n == maxCommandLine {
			break
		}
		if space {
			b.WriteByte(' ')
			space = false
			n++
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// vscodeUnescape decodes OSC 633's escaping: `\\` and `\xAB`.
func vscodeUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch {
		case s[i+1] == '\\':
			b.WriteByte('\\')
			i++
		case s[i+1] == 'x' && i+3 < len(s):
			if n, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
			b.WriteByte(s[i])
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// commandSequences re-emits the running commands (for ModeSequences).
func (t *Terminal) commandSequences(b *strings.Builder) {
	for _, e := range t.shells {
		if e.phase != phaseOutput {
			continue
		}
		b.WriteString("\x1b]133;C")
		if e.aid != "" {
			b.WriteString(";aid=" + e.aid)
		}
		if e.command != "" {
			b.WriteString(";cmdline_url=" + url.PathEscape(e.command))
		}
		if !e.started.IsZero() {
			b.WriteString(";sdterm_started=" + strconv.FormatInt(e.started.UnixMilli(), 10))
		}
		b.WriteString("\x07")
	}
}

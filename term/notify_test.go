// notify_test.go
// Author: Kennt Kim
// Created: 2026-10-19 · Last updated: 2026-10-23
//
// Purpose: what a program says through the terminal (notify.go): OSC 9 notifications and ConEmu's
// numbered commands told apart (9;4 progress, never a notification); OSC 777; OSC 99 chunks, base64
// and its query; text kept plain and bounded; OSC 7501 — the probe answered, records kept by id and
// ranked, `clear` by subtree, base64 title and message, records breaking the rules dropped, working
// and blocked ended by a prompt or EndActive, a reset clearing all — and ModeSequences carrying the
// records and progress to a fresh terminal, as a keeper's replay does. Each bell heard, an OSC's
// terminating BEL not one.
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/notify_test.go on 2026-10-23.

package term

import (
	"encoding/base64"
	"strings"
	"testing"
)

type noteFixture struct {
	*fixture
	notes   [][2]string
	changes int
}

func newNoteFixture(t *testing.T) *noteFixture {
	n := &noteFixture{fixture: &fixture{t: t}}
	n.term = New(Options{Columns: 80, Rows: 24,
		Reply:         func(b []byte) { n.replies = append(n.replies, string(b)) },
		Notify:        func(title, body string) { n.notes = append(n.notes, [2]string{title, body}) },
		StatusChanged: func() { n.changes++ }})
	return n
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestNotificationSequences(t *testing.T) {
	n := newNoteFixture(t)
	n.write("\x1b]9;Claude Code: needs your permission\x07")  // iTerm2's, as Claude Code sends it
	n.write("\x1b]9;4;1;40\x07\x1b]9;1;500\x07\x1b]9;12\x07") // ConEmu's: never notifications
	n.write("\x1b]777;notify;Build;done; 3 warnings\x1b\\")   // the body may hold ';'
	n.write("\x1b]777;preexec\x07")                           // not notify
	n.write("\x1b]99;i=7:d=0:p=title;Claude Code\x1b\\")      // kitty, as Claude Code sends it
	n.write("\x1b]99;i=7:p=body;Waiting\x1b\\\x1b]99;i=7:d=1:a=focus;\x1b\\")
	n.write("\x1b]99;e=1;" + b64("Plain title") + "\x1b\\")
	n.write("\x1b]9;\x07") // empty: nothing
	want := [][2]string{{"", "Claude Code: needs your permission"}, {"Build", "done; 3 warnings"},
		{"Claude Code", "Waiting"}, {"Plain title", ""}}
	if len(n.notes) != len(want) {
		t.Fatalf("notes %q", n.notes)
	}
	for i := range want {
		if n.notes[i] != want[i] {
			t.Fatalf("note %d: %q, want %q", i, n.notes[i], want[i])
		}
	}
	if s := n.term.Status(); s.Progress != 40 || s.State != "" {
		t.Fatalf("9;4 progress %+v", s)
	}

	// Plain and bounded: control characters dropped (base64 can carry them), long text cut.
	n.notes = nil
	n.write("\x1b]99;e=1:p=body;" + b64("a\x1b[31mb\nc") + "\x1b\\")
	n.write("\x1b]9;" + strings.Repeat("x", 2000) + "\x07")
	if n.notes[0][1] != "a[31mbc" || len([]rune(n.notes[1][1])) != maxBody {
		t.Fatalf("plain text %q / %d", n.notes[0], len(n.notes[1][1]))
	}

	// The query advertises what is offered, with the query's terminator.
	n.write("\x1b]99;i=q<1>:p=?;\x1b\\")
	if got := n.replies[len(n.replies)-1]; got != "\x1b]99;i=q1:p=?;a=focus:o=always:p=title,body\x1b\\" {
		t.Fatalf("OSC 99 query reply %q", got)
	}
}

func TestProgramStatus(t *testing.T) {
	n := newNoteFixture(t)
	// The probe, as Claude Code sends it, gets its answer before the DA1 fence's.
	n.write("\x1b]7501;?\x07\x1b[c")
	if len(n.replies) != 2 || n.replies[0] != statusProbeReply+"\x07" || !strings.HasPrefix(n.replies[1], "\x1b[?") {
		t.Fatalf("probe replies %q", n.replies)
	}

	n.write("\x1b]7501;state=working:app=claude-code\x07")
	if s := n.term.Status(); s.State != "working" || s.App != "claude-code" || s.Progress != -1 {
		t.Fatalf("working %+v", s)
	}
	n.write("\x1b]7501;state=blocked:app=claude-code:id=main/tool:kind=permission:title=" + b64("Bash") +
		":msg=" + b64("rm -rf build") + "\x07")
	s := n.term.Status()
	if s.State != "blocked" || s.Kind != "permission" || s.Title != "Bash" || s.Message != "rm -rf build" {
		t.Fatalf("blocked outranks working %+v", s)
	}
	// Unpadded base64 is taken; a record breaking a rule is dropped.
	n.write("\x1b]7501;state=done:id=x:title=" + strings.TrimRight(b64("ok!"), "=") + "\x07")
	n.write("\x1b]7501;state=sleeping\x07\x1b]7501;state=done:progress=140\x07\x1b]7501;state=done:title=%%%\x07")
	n.write("\x1b]7501;state=done:title=" + b64(strings.Repeat("t", 200)) + "\x07")
	if len(n.term.records) != 3 {
		t.Fatalf("records %d", len(n.term.records))
	}

	// clear takes an id and those below it.
	n.write("\x1b]7501;state=clear:id=main\x07")
	if s := n.term.Status(); s.State != "working" {
		t.Fatalf("after clearing main/… %+v", s)
	}
	// A prompt ends working and blocked records, not done ones.
	n.write("\x1b]7501;state=blocked:id=b\x07\x1b]133;A\x07")
	if s := n.term.Status(); s.State != "done" || s.Title != "ok!" {
		t.Fatalf("after a prompt %+v", s)
	}
	// So does the program's end, seen by the session.
	n.write("\x1b]7501;state=working:id=w\x07")
	n.term.EndActive()
	if s := n.term.Status(); s.State != "done" {
		t.Fatalf("after EndActive %+v", s)
	}
	// A reset clears everything.
	before := n.changes
	n.write("\x1bc")
	if s := n.term.Status(); s.State != "" || s.Progress != -1 || n.changes == before {
		t.Fatalf("after RIS %+v", s)
	}
}

func TestStatusCarriedByModeSequences(t *testing.T) {
	a := newNoteFixture(t)
	a.write("\x1b]7501;state=working:app=claude-code:progress=30\x07")
	a.write("\x1b]7501;state=blocked:id=t:kind=question:msg=" + b64("Which branch?") + "\x07")
	a.write("\x1b]9;4;3;\x07")
	b := newNoteFixture(t)
	b.write(string(a.term.ModeSequences()))
	if got, want := b.term.Status(), a.term.Status(); got != want {
		t.Fatalf("carried %+v, want %+v", got, want)
	}
	b.term.EndActive()
	if s := b.term.Status(); s.State != "" || !s.Indeterminate {
		t.Fatalf("after the agent ended: %+v", s)
	}
	if len(b.notes) != 0 {
		t.Fatal("re-emitted status notified")
	}
}

func TestEachBellIsHeard(t *testing.T) {
	rang := 0
	term := New(Options{Columns: 80, Rows: 24, Bell: func() { rang++ }})
	term.Write([]byte("one\atwo\a\a\x1b]0;title\a"))
	// The BEL that ends an OSC is its terminator, not a bell.
	if rang != 3 || term.Bells() != 3 {
		t.Fatalf("rang %d, counted %d", rang, term.Bells())
	}
}

// notify.go
// Author: Kennt Kim
// Created: 2026-10-19 · Last updated: 2026-10-23
//
// Purpose: what a program says to the user through the terminal (Docs/SDTERM-2.md §3, PROTOCOL.md
// §7.2): notifications (OSC 9, OSC 777, OSC 99) and its status (the Program Status Protocol, OSC
// 7501, and OSC 9;4 progress), which coding agents such as Claude Code and Codex report.
//
// Notes:
// - Taken from SiliconDesk's host/internal/term/notify.go on 2026-10-23.
// - OSC 9 is iTerm2's notification, but ConEmu gave it numbered sub-commands (9;4 is progress): a
//   payload whose first field is a number is never a notification (iTerm2's rule).
// - OSC 777 is `notify;title;body`; the body may hold ';'.
// - OSC 99 (kitty) is read for title and body, chunked (`d=0` … `d=1`, by `i`) and base64 (`e=1`);
//   buttons, icons, sounds, close and alive are not offered, and a `p=?` query is answered with
//   what is. Pending chunks are bounded (maxPending ids, maxNoteText each).
// - OSC 7501 records are kept by id (at most maxRecords, the oldest dropped), as revision 0.4 says:
//   `state=clear` removes an id and the ids below it, or all; a reset clears them; working and
//   blocked records end at the next prompt (OSC 133 A, prompts.go) and when the session sees the
//   program end (EndActive). `title` and `msg` are base64 of UTF-8 (padding optional), at most 192 and
//   2,048 bytes decoded; a record breaking a rule is dropped. The probe `?` is answered with the
//   states and kinds this terminal knows — before the program's DA1 fence, since replies go out in
//   order (Claude Code takes a reply only before its DA1 answer; checked in 2.1.303).
// - Text is plain: control characters are dropped and it is cut to maxTitle / maxBody characters;
//   it is never read as markup or a link (VS Code's OSC 99 link injection, CVE-2026-102235).
// - ModeSequences re-emits the live records and progress, so a keeper's follower (which reads the
//   output it lets go) carries them past its 1 MiB cut (SDTERM-2.md §3.2).

package term

import (
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	// The longest notification title and body, in characters.
	maxTitle = 256
	maxBody  = 1024
	// OSC 99 chunks held for ids not finished yet, and the text gathered for one.
	maxPending  = 16
	maxNoteText = 8 * 1024
	// OSC 7501 records kept per terminal, and its limits (revision 0.4).
	maxRecords     = 256
	maxStatusOSC   = 4096
	maxRecordTitle = 192
	maxRecordMsg   = 2048
)

// Status is what the program says it is doing: its most pressing OSC 7501 record and its progress.
type Status struct {
	// "blocked", "error", "working", "done", "idle"; "" without a record.
	State string
	// With blocked: "permission", "question", "auth".
	Kind string
	// The program's name for itself, as it reported it.
	App, Title, Message string
	// 0-100, or -1 for none.
	Progress int
	// OSC 9;4 state 3: busy, with no measure.
	Indeterminate bool
}

// record is one OSC 7501 record.
type record struct {
	id, state, kind, app, title, msg string
	progress                         int // -1: none
	seq                              uint64
}

// progressBar is OSC 9;4's state.
type progressBar struct {
	state   int // 0: none; 1 normal, 2 error, 3 indeterminate, 4 paused
	percent int // -1: none
}

type pendingNote struct {
	title, body string
}

var statusRank = map[string]int{"blocked": 5, "error": 4, "working": 3, "done": 2, "idle": 1}

// Status returns the program's status now.
func (t *Terminal) Status() Status {
	var best *record
	for _, r := range t.records {
		if best == nil || statusRank[r.state] > statusRank[best.state] ||
			(statusRank[r.state] == statusRank[best.state] && r.seq > best.seq) {
			best = r
		}
	}
	s := Status{Progress: -1}
	if best != nil {
		s.State, s.Kind, s.App, s.Title, s.Message, s.Progress = best.state, best.kind, best.app, best.title,
			best.msg, best.progress
	}
	if s.Progress < 0 {
		switch t.progress.state {
		case 1, 2, 4:
			s.Progress = t.progress.percent
		case 3:
			s.Indeterminate = true
		}
	}
	return s
}

// EndActive drops the working and blocked records: the program that set them has ended (the
// session saw the terminal's foreground process group return to the shell), or a prompt came.
func (t *Terminal) EndActive() {
	changed := false
	for id, r := range t.records {
		if r.state == "working" || r.state == "blocked" {
			delete(t.records, id)
			changed = true
		}
	}
	if changed {
		t.statusChanged()
	}
}

func (t *Terminal) statusChanged() {
	if t.onStatus != nil {
		t.onStatus()
	}
}

func (t *Terminal) clearStatus() {
	if len(t.records) == 0 && t.progress == (progressBar{}) {
		return
	}
	t.records = nil
	t.progress = progressBar{}
	t.statusChanged()
}

// notify passes a notification on, as plain text within bounds.
func (t *Terminal) notify(title, body string) {
	title, body = plainText(title, maxTitle), plainText(body, maxBody)
	if title == "" && body == "" || t.onNotify == nil {
		return
	}
	t.onNotify(title, body)
}

// plainText drops control characters and cuts s to n characters.
func plainText(s string, n int) string {
	var b strings.Builder
	count := 0
	for _, r := range s {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			continue
		}
		if count == n {
			break
		}
		b.WriteRune(r)
		count++
	}
	return strings.TrimSpace(b.String())
}

// osc9 is iTerm2's notification, or a ConEmu command when its first field is a number.
func (t *Terminal) osc9(arg string) {
	first, rest, _ := strings.Cut(arg, ";")
	if _, err := strconv.Atoi(first); err != nil {
		t.notify("", arg)
		return
	}
	if first != "4" {
		return
	}
	stateText, percentText, _ := strings.Cut(rest, ";")
	state, err := strconv.Atoi(stateText)
	if stateText == "" {
		state, err = 0, nil // a bare 9;4 ends it (iTerm2)
	}
	if err != nil || state < 0 || state > 4 {
		return
	}
	percent := -1
	if p, err := strconv.Atoi(percentText); err == nil {
		percent = min(max(p, 0), 100)
	}
	next := progressBar{state: state, percent: percent}
	switch state {
	case 0:
		next = progressBar{}
	case 2, 4:
		if percent < 0 { // error or paused without a figure keeps the current one
			next.percent = t.progress.percent
		}
	case 3:
		next.percent = -1
	}
	if next != t.progress {
		t.progress = next
		t.statusChanged()
	}
}

// osc777 is rxvt's `notify;title;body`.
func (t *Terminal) osc777(arg string) {
	cmd, rest, _ := strings.Cut(arg, ";")
	if cmd != "notify" {
		return
	}
	title, body, ok := strings.Cut(rest, ";")
	if !ok {
		title, body = "", title
	}
	t.notify(title, body)
}

// osc99 is kitty's desktop-notification protocol: metadata `k=v:k=v`, then the payload.
func (t *Terminal) osc99(arg string, end string) {
	meta, payload, _ := strings.Cut(arg, ";")
	keys := map[string]string{}
	for _, kv := range strings.Split(meta, ":") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			keys[k] = v
		}
	}
	id := sanitizeNoteID(keys["i"])
	kind := keys["p"]
	if kind == "" {
		kind = "title"
	}
	if kind == "?" {
		t.reply([]byte("\x1b]99;i=" + id + ":p=?;a=focus:o=always:p=title,body" + end))
		return
	}
	if kind != "title" && kind != "body" {
		return // close, alive, icon, buttons: not offered
	}
	if keys["e"] == "1" {
		decoded, err := decodeBase64(payload)
		if err != nil {
			return
		}
		payload = decoded
	}
	if t.pendingNotes == nil {
		t.pendingNotes = map[string]*pendingNote{}
	}
	note := t.pendingNotes[id]
	if note == nil {
		if len(t.pendingNotes) >= maxPending {
			return
		}
		note = &pendingNote{}
		t.pendingNotes[id] = note
	}
	if kind == "title" && len(note.title)+len(payload) <= maxNoteText {
		note.title += payload
	} else if kind == "body" && len(note.body)+len(payload) <= maxNoteText {
		note.body += payload
	}
	if keys["d"] != "0" {
		delete(t.pendingNotes, id)
		t.notify(note.title, note.body)
	}
}

// sanitizeNoteID keeps the characters an OSC 99 id may have, so it can be echoed back.
func sanitizeNoteID(id string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_-+.", r)) {
			return r
		}
		return -1
	}, id)
}

func decodeBase64(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
	}
	return string(b), err
}

// The probe's answer: what this terminal knows (revision 0.4's `?` reply, with the optional lists).
const statusProbeReply = "\x1b]7501;?:states=working,blocked,done,error,idle,clear:kinds=permission,question,auth"

// osc7501 is the Program Status Protocol: a record, or the probe.
func (t *Terminal) osc7501(arg string, end string) {
	if strings.HasPrefix(arg, "?") {
		t.reply([]byte(statusProbeReply + end))
		return
	}
	if len(arg) > maxStatusOSC {
		return
	}
	keys := map[string]string{}
	for _, kv := range strings.Split(arg, ":") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return
		}
		keys[k] = v
	}
	r := &record{id: keys["id"], state: keys["state"], kind: keys["kind"], app: plainText(keys["app"], 64),
		progress: -1}
	switch r.state {
	case "clear":
		t.clearRecords(r.id)
		return
	case "working", "blocked", "done", "error", "idle":
	default:
		return
	}
	if p, ok := keys["progress"]; ok {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 100 {
			return
		}
		r.progress = n
	}
	for key, limit := range map[string]int{"title": maxRecordTitle, "msg": maxRecordMsg} {
		v, ok := keys[key]
		if !ok {
			continue
		}
		decoded, err := decodeBase64(v)
		if err != nil || len(decoded) > limit {
			return
		}
		if key == "title" {
			r.title = plainText(decoded, maxRecordTitle)
		} else {
			r.msg = plainText(decoded, maxRecordMsg)
		}
	}
	if t.records == nil {
		t.records = map[string]*record{}
	}
	t.recordSeq++
	r.seq = t.recordSeq
	t.records[r.id] = r
	if len(t.records) > maxRecords {
		var oldest *record
		for _, x := range t.records {
			if oldest == nil || x.seq < oldest.seq {
				oldest = x
			}
		}
		delete(t.records, oldest.id)
	}
	t.statusChanged()
}

// clearRecords removes the record id and the ids below it ("a/b" is below "a"), or every record.
func (t *Terminal) clearRecords(id string) {
	changed := false
	for k := range t.records {
		if id == "" || k == id || strings.HasPrefix(k, id+"/") {
			delete(t.records, k)
			changed = true
		}
	}
	if changed {
		t.statusChanged()
	}
}

// statusSequences re-emits the live records and progress (for ModeSequences).
func (t *Terminal) statusSequences(b *strings.Builder) {
	records := make([]*record, 0, len(t.records))
	for _, r := range t.records {
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].seq < records[j].seq })
	for _, r := range records {
		fields := []string{"state=" + r.state}
		if r.id != "" {
			fields = append(fields, "id="+r.id)
		}
		if r.kind != "" {
			fields = append(fields, "kind="+r.kind)
		}
		if r.app != "" {
			fields = append(fields, "app="+r.app)
		}
		if r.progress >= 0 {
			fields = append(fields, "progress="+strconv.Itoa(r.progress))
		}
		if r.title != "" {
			fields = append(fields, "title="+base64.StdEncoding.EncodeToString([]byte(r.title)))
		}
		if r.msg != "" {
			fields = append(fields, "msg="+base64.StdEncoding.EncodeToString([]byte(r.msg)))
		}
		b.WriteString("\x1b]7501;" + strings.Join(fields, ":") + "\x07")
	}
	if t.progress.state != 0 {
		b.WriteString("\x1b]9;4;" + strconv.Itoa(t.progress.state) + ";")
		if t.progress.percent >= 0 {
			b.WriteString(strconv.Itoa(t.progress.percent))
		}
		b.WriteString("\x07")
	}
}

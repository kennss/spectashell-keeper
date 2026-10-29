// pages_test.go
// Author: Kennt Kim
// Created: 2026-10-25 · Last updated: 2026-10-30
//
// Purpose: the pieces of an agent's page commands (Docs/WINDOWS.md §3) on their own: a command
// finds its shell's keeper by the variable, else by its parents, and the device holding it; a
// device's socket replaces one left at its path, comes back when removed, and leaves a newer one's
// path alone; a relay passes requests and replies through its channel, and tells its askers when
// the channel ends; the command line reads into requests; and the MCP server answers initialize,
// lists its tools and calls them against a device's socket. The end to end, from a shell on a host
// through the core, is the bus's (bus/pages_test.go).
//
// Notes:
// - Moved here from core/internal/pages on 2026-10-29, with the keeper program
//   (Docs/DESIGN.md §3.2).

//go:build unix

package pages

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kennss/spectashell-keeper/keeper"
)

// keepers is a keepers' folder holding descriptions of ds, each of a running keeper (this process).
func keepers(t *testing.T, ds ...keeper.Description) string {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "sdp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	dir := filepath.Join(base, "k")
	if err := keeper.EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		d.KeeperPID = os.Getpid()
		b, _ := json.Marshal(d)
		if err := os.WriteFile(filepath.Join(dir, d.KeeperID+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func noEnv(string) string { return "" }

func TestACommandFindsItsShellByTheVariableElseByItsParents(t *testing.T) {
	a := keeper.Description{KeeperID: "aaaaaaaaaaaaaaaa", PID: 100}
	b := keeper.Description{KeeperID: "bbbbbbbbbbbbbbbb", PID: 200}
	dir := keepers(t, a, b)
	// 500 is in 400, a program of b's shell 200.
	parents := map[int]int{500: 400, 400: 200, 200: 1, 900: 1}
	parent := func(pid int) int { return parents[pid] }

	if d, err := Own(dir, noEnv, 500, parent); err != nil || d.KeeperID != b.KeeperID {
		t.Fatalf("by parents: %v, %v", d.KeeperID, err)
	}
	// The variable wins: a tmux server's programs have no keeper among their parents.
	env := func(k string) string {
		if k == KeeperVariable {
			return a.KeeperID
		}
		return ""
	}
	if d, err := Own(dir, env, 900, parent); err != nil || d.KeeperID != a.KeeperID {
		t.Fatalf("by the variable: %v, %v", d.KeeperID, err)
	}
	// A variable naming no keeper here is passed over.
	gone := func(string) string { return "cccccccccccccccc" }
	if d, err := Own(dir, gone, 500, parent); err != nil || d.KeeperID != b.KeeperID {
		t.Fatalf("a stale variable: %v, %v", d.KeeperID, err)
	}
	if _, err := Own(dir, noEnv, 900, parent); err == nil || !strings.Contains(err.Error(), "this is not one") {
		t.Fatalf("outside a kept shell: %v", err)
	}
}

func TestTheDeviceHoldingTheShellIsAsked(t *testing.T) {
	held := keeper.Description{KeeperID: "aaaaaaaaaaaaaaaa", Attached: true,
		Meta: keeper.Meta{Client: "iPad", ClientID: "ipad-1"}}
	if path, err := Holder("/p", held); err != nil || path != DeviceSocket("/p", "ipad-1") {
		t.Fatalf("held: %q, %v", path, err)
	}
	loose := held
	loose.Attached = false
	if _, err := Holder("/p", loose); err == nil || !strings.Contains(err.Error(), "no device has this shell open") {
		t.Fatalf("held by none: %v", err)
	}
	old := held
	old.Meta.ClientID = ""
	if _, err := Holder("/p", old); err == nil || !strings.Contains(err.Error(), "update it") {
		t.Fatalf("held by an older app: %v", err)
	}
	if name := DeviceName("ipad-1"); !ValidName(name) || name == DeviceName("ipad-2") {
		t.Fatalf("names: %q, %q", name, DeviceName("ipad-2"))
	}
}

// answering is a handler that says the command back.
func answering(r Request) Reply { return Reply{Text: r.Command + " " + r.Keeper} }

func TestASocketTakesThePathAndKeepsIt(t *testing.T) {
	checkEvery = 20 * time.Millisecond
	t.Cleanup(func() { checkEvery = 10 * time.Second })
	dir := filepath.Dir(keepers(t))
	path := filepath.Join(dir, "d.sock")
	// Something left at the path, as a relay that died leaves its socket.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := Listen(path, answering)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if r, err := Ask(path, Request{Keeper: "k", Command: "pages"}, time.Second); err != nil || r.Text != "pages k" {
		t.Fatalf("asked: %+v, %v", r, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	// Removed — an app cleaner — it comes back.
	os.Remove(path)
	waitFor(t, func() bool { _, err := Ask(path, Request{Command: "x"}, time.Second); return err == nil })

	// A newer socket takes the path; the first leaves it alone, and takes it back once it goes.
	second, err := Listen(path, func(Request) Reply { return Reply{Text: "second"} })
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * checkEvery)
	if r, _ := Ask(path, Request{}, time.Second); r.Text != "second" {
		t.Fatalf("the newer socket's path answered %q", r.Text)
	}
	second.Close()
	waitFor(t, func() bool {
		r, err := Ask(path, Request{Command: "c"}, time.Second)
		return err == nil && r.Text == "c "
	})
	first.Close()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("the socket is left after Close: %v", err)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestARelayPassesRequestsThroughItsChannel(t *testing.T) {
	dir := keepers(t)
	name := DeviceName("mac")
	// The channel: the relay's output is the core's input, and the other way round.
	coreIn, relayOut := io.Pipe()
	relayIn, coreOut := io.Pipe()
	relayDone := make(chan error, 1)
	go func() { relayDone <- Relay(dir, name, relayIn, relayOut) }()
	ready := make(chan string, 1)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- ServeRelay(coreIn, coreOut, answering, func(socket string) { ready <- socket })
	}()
	socket := <-ready
	if socket != SocketNamed(filepath.Join(dir, "pages"), name) {
		t.Fatalf("socket %q", socket)
	}
	if r, err := Ask(socket, Request{Keeper: "k1", Command: "snapshot"}, 5*time.Second); err != nil || r.Text != "snapshot k1" {
		t.Fatalf("asked: %+v, %v", r, err)
	}
	// The connection ends: the relay's input ends, it goes, and so does its socket.
	coreOut.Close()
	if err := <-relayDone; err != nil {
		t.Fatalf("relay: %v", err)
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("the socket is left: %v", err)
	}
	relayOut.Close()
	<-serveDone
}

func TestARelayTellsItsAskersWhenTheChannelEnds(t *testing.T) {
	dir := keepers(t)
	name := DeviceName("mac")
	coreIn, relayOut := io.Pipe()
	relayIn, coreOut := io.Pipe()
	go Relay(dir, name, relayIn, relayOut)
	lines := bufio.NewReader(coreIn)
	first, _ := lines.ReadString('\n')
	var ready envelope
	json.Unmarshal([]byte(first), &ready)
	answer := make(chan Reply, 1)
	go func() {
		r, _ := Ask(ready.Ready, Request{Command: "wait"}, 5*time.Second)
		answer <- r
	}()
	lines.ReadString('\n') // the request, which no one answers
	coreOut.Close()
	if r := <-answer; !strings.Contains(r.Error, "ended") {
		t.Fatalf("reply %+v", r)
	}
	relayOut.Close()
}

func TestTheCommandLineReadsIntoRequests(t *testing.T) {
	cases := []struct {
		line    string
		command string
		port    int
		args    map[string]any
	}{
		{"pages", "pages", 0, map[string]any{}},
		{"open http://localhost:3000/app", "open", 0, map[string]any{"url": "http://localhost:3000/app"}},
		{"snapshot -i --port 8188", "snapshot", 8188, map[string]any{"interactive": true}},
		{"snapshot --selector main --max 500", "snapshot", 0, map[string]any{"selector": "main", "maxChars": 500}},
		{"click e12", "click", 0, map[string]any{"target": map[string]any{"ref": "e12"}}},
		{"click button.save --double", "dblclick", 0, map[string]any{"target": map[string]any{"selector": "button.save"}}},
		{"click --text Deploy", "click", 0, map[string]any{"target": map[string]any{"text": "Deploy"}}},
		{"fill e3 sam@example.com", "fill", 0, map[string]any{"target": map[string]any{"ref": "e3"}, "text": "sam@example.com"}},
		{"fill --text Email hi", "fill", 0, map[string]any{"target": map[string]any{"text": "Email"}, "text": "hi"}},
		{"type hello --into e4", "type", 0, map[string]any{"text": "hello", "target": map[string]any{"ref": "e4"}}},
		{"press Enter --on #search", "press", 0, map[string]any{"key": "Enter", "target": map[string]any{"selector": "#search"}}},
		{"select e5 small large", "select", 0, map[string]any{"target": map[string]any{"ref": "e5"}, "values": []string{"small", "large"}}},
		{"scroll --dy -400", "scroll", 0, map[string]any{"dy": -400}},
		{"wait --text Saved --timeout 2.5", "wait", 0, map[string]any{"text": "Saved", "timeoutMs": 2500}},
		{"wait --selector .spinner --gone", "wait", 0, map[string]any{"selector": ".spinner", "gone": true}},
		{"get count li.todo", "get", 0, map[string]any{"what": "count", "selector": "li.todo"}},
		{"get attr e2 --name href", "get", 0, map[string]any{"what": "attr", "target": map[string]any{"ref": "e2"}, "name": "href"}},
		{"eval document.title", "eval", 0, map[string]any{"script": "document.title"}},
		{"screenshot e9 --out /tmp/x.png", "screenshot", 0, map[string]any{"target": map[string]any{"ref": "e9"}, "out": "/tmp/x.png"}},
		{"console --clear", "console", 0, map[string]any{"clear": true}},
	}
	for _, c := range cases {
		command, port, args, err := parse(strings.Fields(c.line), nil)
		if err != nil {
			t.Errorf("%q: %v", c.line, err)
			continue
		}
		if command != c.command || port != c.port || !reflect.DeepEqual(args, c.args) {
			t.Errorf("%q: %q %d %#v; want %q %d %#v", c.line, command, port, args, c.command, c.port, c.args)
		}
	}
	for _, bad := range []string{"click", "fill e3", "wait", "get colour", "get attr e2", "teleport",
		"snapshot --port 0", "wait --text x --timeout 60"} {
		if _, _, _, err := parse(strings.Fields(bad), nil); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
	// eval - reads the script from standard input.
	_, _, args, err := parse([]string{"eval", "-"}, strings.NewReader("return 1 + 1"))
	if err != nil || args["script"] != "return 1 + 1" {
		t.Fatalf("eval -: %v, %v", args, err)
	}
}

// heldShell is a keepers' folder with one shell, held by device "mac", whose socket answers with
// handle; and an asker in that shell.
func heldShell(t *testing.T, handle Handler) Asker {
	t.Helper()
	d := keeper.Description{KeeperID: "aaaaaaaaaaaaaaaa", PID: 100, Attached: true,
		Meta: keeper.Meta{Client: "Mac", ClientID: "mac"}}
	dir := keepers(t, d)
	folder, err := Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(DeviceSocket(folder, "mac"), handle)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ln.Close)
	return Asker{Keepers: dir, Getenv: noEnv, PID: 500, Parent: func(pid int) int {
		return map[int]int{500: 100}[pid]
	}}
}

func TestTheMCPServerCallsTheCommands(t *testing.T) {
	var got []Request
	a := heldShell(t, func(r Request) Reply {
		got = append(got, r)
		switch r.Command {
		case "screenshot":
			return Reply{Text: "GPU Box :3000 /", Image: []byte("\x89PNG")}
		case "click":
			return Reply{Error: "No element e9 on the page now: take a new snapshot."}
		}
		return Reply{Text: "- button \"Deploy\" [ref=e7]"}
	})
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"2"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"page_snapshot","arguments":{"interactive":true,"port":3000}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"page_click","arguments":{"ref":"e9"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"page_screenshot","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"page_fill","arguments":{"name":"Email","text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"page_teleport","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"resources/list"}`,
	}, "\n") + "\n"
	var out strings.Builder
	if err := MCP(a, "test-build", strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	var replies []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		replies = append(replies, r)
	}
	// The notification is not answered: eight replies for eight requests.
	if len(replies) != 8 {
		t.Fatalf("%d replies: %v", len(replies), out.String())
	}
	init := replies[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-06-18" || init["serverInfo"].(map[string]any)["name"] != "spectashell" {
		t.Fatalf("initialize: %v", init)
	}
	tools := replies[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) < 15 || tools[0].(map[string]any)["name"] != "page_open" {
		t.Fatalf("tools: %v", tools)
	}
	text := func(r map[string]any) (string, bool) {
		res := r["result"].(map[string]any)
		content := res["content"].([]any)
		isError, _ := res["isError"].(bool)
		return content[0].(map[string]any)["text"].(string), isError
	}
	if s, isError := text(replies[2]); isError || s != "- button \"Deploy\" [ref=e7]" {
		t.Fatalf("snapshot: %q", s)
	}
	if s, isError := text(replies[3]); !isError || !strings.Contains(s, "No element e9") {
		t.Fatalf("click: %q %v", s, isError)
	}
	shot := replies[4]["result"].(map[string]any)["content"].([]any)
	if len(shot) != 2 || shot[1].(map[string]any)["type"] != "image" || shot[1].(map[string]any)["data"] != "iVBORw==" {
		t.Fatalf("screenshot: %v", shot)
	}
	if replies[5]["error"] != nil {
		t.Fatalf("fill: %v", replies[5])
	}
	for i, want := range []int{-32602, -32601} {
		if e := replies[6+i]["error"].(map[string]any); int(e["code"].(float64)) != want {
			t.Fatalf("reply %d: %v", 6+i, e)
		}
	}
	// What the commands were, as the app got them.
	if len(got) != 4 || got[0].Command != "snapshot" || got[0].Port != 3000 || string(got[0].Args) != `{"interactive":true}` ||
		got[0].Keeper != "aaaaaaaaaaaaaaaa" || string(got[3].Args) != `{"target":{"text":"Email"},"text":"hi"}` {
		t.Fatalf("requests: %+v", got)
	}
}

func TestTheCommandPrintsTheAnswerAndSavesAScreenshot(t *testing.T) {
	a := heldShell(t, func(r Request) Reply {
		if r.Command == "screenshot" {
			if strings.Contains(string(r.Args), "out") {
				return Reply{Error: "the app was told where the file goes"}
			}
			return Reply{Text: "GPU Box :3000 /", Image: []byte("png"), Page: &Page{Port: 3000}}
		}
		return Reply{Error: "Nothing on the page matches \"#nope\"."}
	})
	path := filepath.Join(t.TempDir(), "shot.png")
	var out, errs strings.Builder
	if code := Command(a, []string{"screenshot", "--out", path}, nil, &out, &errs); code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	if b, _ := os.ReadFile(path); string(b) != "png" || !strings.Contains(out.String(), "Saved to "+path) {
		t.Fatalf("saved %q; printed %q", b, out.String())
	}
	out.Reset()
	if code := Command(a, []string{"click", "#nope"}, nil, &out, &errs); code != 1 ||
		!strings.Contains(errs.String(), `Nothing on the page matches "#nope".`) {
		t.Fatalf("exit %d: %q", code, errs.String())
	}
	if code := Command(a, []string{"click"}, nil, &out, &errs); code != 2 {
		t.Fatalf("a usage error exited %d", code)
	}
}

func TestAProcessParentIsRead(t *testing.T) {
	if got := Parent(os.Getpid()); got != os.Getppid() {
		t.Fatalf("Parent = %d, want %d", got, os.Getppid())
	}
	if Parent(1<<30) != 0 {
		t.Fatal("a process that is not there has a parent")
	}
}

// send_test.go
// Author: Kennt Kim
// Created: 2026-10-30 · Last updated: 2026-10-31
//
// Purpose: a host's push registrations and its keeper's sender (cli.go, files.go, send.go,
// Docs/design/08-PUSH.md §3.2, §4) against a relay of the tests' own over TLS: add makes the host's
// key here, keeps it when added again, and only its public half leaves; a folder or a file others
// could touch is refused; a notice goes signed, sealed, at the relay's time, with its interruption;
// nothing goes while an owner looks, for a shell no owner started, for a command shorter than asked;
// a blocked notice answered before a retry is dropped; a burst waits its interval but blocked never
// does; 404 waits an hour and gives up after 7 days; 410 removes the file.

package push

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kennss/spectashell-keeper/keeper"
	"github.com/kennss/spectashell-keeper/term"

	"github.com/kennss/spectashell-keeper/host/pushwire"
)

// fakeRelay checks what a host sends as the relay does, opens it as the device would, and answers
// as it is told.
type fakeRelay struct {
	t        *testing.T
	server   *httptest.Server
	ahead    time.Duration // its clock, ahead of ours
	device   *ecdh.PrivateKey
	mu       sync.Mutex
	keys     map[string]ed25519.PublicKey
	answers  []int // in turn; 202 when none left
	received chan received
}

type received struct {
	status       int
	note         pushwire.Notification
	urgent       bool
	interruption string
}

func newFakeRelay(t *testing.T) *fakeRelay {
	device, _ := ecdh.X25519().GenerateKey(rand.Reader)
	f := &fakeRelay{t: t, ahead: time.Hour, device: device, keys: map[string]ed25519.PublicKey{},
		received: make(chan received, 64)}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeRelay) now() time.Time { return time.Now().Add(f.ahead) }

func (f *fakeRelay) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(pushwire.ServerTime, strconv.FormatInt(f.now().Unix(), 10))
	if !strings.HasPrefix(r.Header.Get("User-Agent"), "spectashell-keeper") {
		f.t.Errorf("a request named %q, not the keeper program", r.Header.Get("User-Agent"))
	}
	if r.URL.Path == "/v1/time" {
		return
	}
	body, _ := io.ReadAll(r.Body)
	ts, _ := strconv.ParseInt(r.Header.Get(pushwire.TimeHeader), 10, 64)
	if d := f.now().Unix() - ts; d > 300 || d < -300 {
		f.t.Errorf("a request at %d, the relay's time %d: the host's clock was not corrected", ts, f.now().Unix())
	}
	f.mu.Lock()
	pub := f.keys[r.Header.Get(pushwire.KeyHeader)]
	status := http.StatusAccepted
	if len(f.answers) > 0 {
		status, f.answers = f.answers[0], f.answers[1:]
	}
	f.mu.Unlock()
	sig, _ := base64.StdEncoding.DecodeString(r.Header.Get(pushwire.SignatureHeader))
	signed := pushwire.Signed{Method: r.Method, Path: r.URL.Path, Time: r.Header.Get(pushwire.TimeHeader), Nonce: r.Header.Get(pushwire.NonceHeader)}
	if !pushwire.Verify(pub, signed, body, sig) {
		f.t.Errorf("a push the relay could not verify")
	}
	var p pushwire.PushRequest
	_ = json.Unmarshal(body, &p)
	raw, _ := base64.StdEncoding.DecodeString(p.Sealed)
	note, err := pushwire.Open(f.device, p.KeyID, raw)
	if err != nil {
		f.t.Errorf("the device cannot open it: %v", err)
	}
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	f.received <- received{status: status, note: note, urgent: p.Urgent, interruption: p.Interruption}
}

func (f *fakeRelay) answer(statuses ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, statuses...)
}

// next is the next push the relay received.
func (f *fakeRelay) next() received {
	f.t.Helper()
	select {
	case r := <-f.received:
		return r
	case <-time.After(10 * time.Second):
		f.t.Fatal("no push came")
		return received{}
	}
}

// fakeView is a keeper as the sender sees it.
type fakeView struct {
	mu        sync.Mutex
	looking   bool
	title     string
	status    term.Status
	startedBy *keeper.Who
}

func (v *fakeView) KeeperID() string { return "keeper-1" }
func (v *fakeView) Title() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.title
}
func (v *fakeView) Program() string { return "python3" }
func (v *fakeView) StartedBy() *keeper.Who {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.startedBy
}
func (v *fakeView) Looking([]string, time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.looking
}
func (v *fakeView) Status() term.Status {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.status
}
func (v *fakeView) set(f func(*fakeView)) {
	v.mu.Lock()
	defer v.mu.Unlock()
	f(v)
}

// passes counts the sender's passes over what it holds; wait returns after the next one.
type passes chan struct{}

func (p passes) wait(t *testing.T) {
	t.Helper()
	select {
	case <-p:
	case <-time.After(10 * time.Second):
		t.Fatal("the sender did not take it")
	}
}

// notice hands n to s and waits until s has taken it — sent or dropped — so what the test changes
// next is not what s looks at for n.
func notice(t *testing.T, s *Sender, p passes, n keeper.Notice) {
	t.Helper()
	s.Notice(n)
	p.wait(t)
}

// setUp makes a push folder of the test's, a relay, and a registration of one device there.
func setUp(t *testing.T, events Events) (*fakeRelay, *fakeView, *Sender, string, passes) {
	dir := filepath.Join(t.TempDir(), "push")
	t.Setenv("SPECTASHELL_PUSH_DIR", dir)
	relay := newFakeRelay(t)
	res, err := Add(AddRequest{DeviceTag: "device-tag-1", Relay: relay.server.URL,
		SealKey: base64.StdEncoding.EncodeToString(relay.device.PublicKey().Bytes()), HostRef: "host-ref",
		Owners: []string{"owner-device"}, Events: events})
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := base64.StdEncoding.DecodeString(res.Public)
	relay.keys[res.KeyID] = pub
	roots := x509.NewCertPool()
	roots.AddCert(relay.server.Certificate())
	view := &fakeView{title: "aurora", status: term.Status{State: "blocked", Kind: "permission"},
		startedBy: &keeper.Who{Client: "iPhone", ClientID: "owner-device"}}
	done := make(passes, 64)
	s := NewSender(view, Options{RootCAs: roots, Interval: 300 * time.Millisecond, Backoff: 50 * time.Millisecond,
		LookEvery: 20 * time.Millisecond, Handled: func() { done <- struct{}{} }})
	return relay, view, s, dir, done
}

var allEvents = Events{Blocked: true, Done: true, Notify: true, CommandSeconds: 20}

func TestAddMakesTheHostsKeyHere(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "push")
	t.Setenv("SPECTASHELL_PUSH_DIR", dir)
	device, _ := ecdh.X25519().GenerateKey(rand.Reader)
	req := AddRequest{DeviceTag: "device-tag-1", Relay: "https://push.example.com",
		SealKey: base64.StdEncoding.EncodeToString(device.PublicKey().Bytes()), HostRef: "h", Events: allEvents}
	first, err := Add(req)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := base64.StdEncoding.DecodeString(first.Public)
	if pushwire.KeyID(pub) != first.KeyID {
		t.Fatal("the key id is not the public key's")
	}
	info, _ := os.Stat(filepath.Join(dir, "device-tag-1.json"))
	folder, _ := os.Stat(dir)
	if info.Mode().Perm() != 0o600 || folder.Mode().Perm() != 0o700 {
		t.Fatalf("modes: file %v, folder %v", info.Mode().Perm(), folder.Mode().Perm())
	}
	// Added again (a setting changed), the key stays; asked, a new one, the old named.
	again, _ := Add(req)
	req.NewKey = true
	renewed, _ := Add(req)
	if again.KeyID != first.KeyID || renewed.KeyID == first.KeyID || renewed.Replaced != first.KeyID {
		t.Fatalf("keys: %s, %s, %s (replaced %s)", first.KeyID, again.KeyID, renewed.KeyID, renewed.Replaced)
	}
	// List says which, never their keys.
	var out strings.Builder
	if err := Command([]string{"list"}, nil, &out); err != nil || strings.Contains(out.String(), "sign_key") ||
		!strings.Contains(out.String(), renewed.KeyID) {
		t.Fatalf("list: %s, %v", out.String(), err)
	}
	if keyID, _ := Remove("device-tag-1"); keyID != renewed.KeyID {
		t.Fatalf("removed %q", keyID)
	}
	if regs, _ := Registrations(); len(regs) != 0 {
		t.Fatal("still registered")
	}
	// A relay that is not https, but on this machine, is refused.
	req.Relay = "http://push.example.com"
	if _, err := Add(req); err == nil {
		t.Fatal("a plain http relay accepted")
	}
}

func TestWhatOthersCouldTouchIsRefused(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	_ = os.Mkdir(real, 0o700)
	link := filepath.Join(base, "push")
	_ = os.Symlink(real, link)
	t.Setenv("SPECTASHELL_PUSH_DIR", link)
	device, _ := ecdh.X25519().GenerateKey(rand.Reader)
	req := AddRequest{DeviceTag: "device-tag-1", Relay: "https://push.example.com",
		SealKey: base64.StdEncoding.EncodeToString(device.PublicKey().Bytes()), HostRef: "h"}
	if _, err := Add(req); err == nil {
		t.Fatal("a folder that is a link accepted")
	}
	dir := filepath.Join(base, "open")
	_ = os.Mkdir(dir, 0o755)
	t.Setenv("SPECTASHELL_PUSH_DIR", dir)
	if _, err := Add(req); err == nil {
		t.Fatal("a folder open to others accepted")
	}
	_ = os.Chmod(dir, 0o700)
	if _, err := Add(req); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(filepath.Join(dir, "device-tag-1.json"), 0o644)
	if regs, _ := Registrations(); len(regs) != 0 {
		t.Fatal("a registration others can read is used")
	}
}

func TestANoticeGoesSignedSealedAtTheRelaysTime(t *testing.T) {
	relay, _, s, _, _ := setUp(t, allEvents)
	s.Notice(keeper.Notice{Kind: "blocked", Status: term.Status{State: "blocked", Kind: "permission",
		App: "Claude Code", Message: "May I run make?"}})
	got := relay.next()
	if got.interruption != pushwire.TimeSensitive || !got.urgent || got.note.State != "blocked" || got.note.Kind != "permission" ||
		got.note.HostRef != "host-ref" || got.note.Keeper != "keeper-1" || got.note.Program != "Claude Code" ||
		got.note.Text != "May I run make?" {
		t.Fatalf("received %+v", got)
	}
	if d := relay.now().Unix() - got.note.At; d > 5 || d < -5 {
		t.Fatalf("sealed at %d, the relay's time %d", got.note.At, relay.now().Unix())
	}
}

func TestNothingGoesThatShouldNot(t *testing.T) {
	relay, view, s, _, p := setUp(t, allEvents)
	// A program's notification while an owner looks: a moment seen where it happened.
	view.set(func(v *fakeView) { v.looking = true })
	notice(t, s, p, keeper.Notice{Kind: "notify", Title: "Build", Body: "done"})
	// A command shorter than asked.
	view.set(func(v *fakeView) { v.looking = false })
	notice(t, s, p, keeper.Notice{Kind: "command", Command: "ls", Duration: 3 * time.Second})
	// A shell a teammate started.
	view.set(func(v *fakeView) { v.startedBy = &keeper.Who{ClientID: "teammate"} })
	notice(t, s, p, keeper.Notice{Kind: "blocked", Status: term.Status{State: "blocked"}})
	// Then one that goes: the first the relay sees, as notices are taken in order.
	view.set(func(v *fakeView) { v.startedBy = &keeper.Who{ClientID: "owner-device"} })
	s.Notice(keeper.Notice{Kind: "command", Command: "make", Duration: 25 * time.Second})
	if got := relay.next(); got.note.State != "command" || got.note.Text != "make" || got.urgent {
		t.Fatalf("the first push was %+v", got)
	}
}

func TestABlockedNoticeAnsweredMeanwhileIsDropped(t *testing.T) {
	relay, view, s, _, p := setUp(t, allEvents)
	relay.answer(http.StatusServiceUnavailable)
	notice(t, s, p, keeper.Notice{Kind: "blocked", Status: term.Status{State: "blocked"}})
	if got := relay.next(); got.status != http.StatusServiceUnavailable {
		t.Fatalf("first: %d", got.status)
	}
	// Answered at the Mac before the retry; then the program finished.
	view.set(func(v *fakeView) { v.status = term.Status{State: "done"} })
	s.Notice(keeper.Notice{Kind: "done", Status: term.Status{State: "done"}})
	if got := relay.next(); got.note.State != "done" {
		t.Fatalf("after the answer, %s was sent, not done alone", got.note.State)
	}
}

func TestWhatComesWhileAnOwnerLooksGoesWhenTheyLeave(t *testing.T) {
	relay, view, s, _, p := setUp(t, allEvents)
	view.set(func(v *fakeView) { v.looking = true })
	// Waiting for permission while the user is at the Mac: held, not dropped.
	notice(t, s, p, keeper.Notice{Kind: "blocked", Status: term.Status{State: "blocked", Kind: "permission"}})
	// Looked at again, still held.
	p.wait(t)
	p.wait(t)
	if len(relay.received) != 0 {
		t.Fatal("it was sent while an owner looked")
	}
	// They lock the Mac and leave, the program still waiting: it goes.
	view.set(func(v *fakeView) { v.looking = false })
	if got := relay.next(); got.note.State != "blocked" || got.note.Kind != "permission" {
		t.Fatalf("after they left: %+v", got)
	}
}

func TestAHeldNoticeThatPassedIsNotSent(t *testing.T) {
	relay, view, s, _, p := setUp(t, allEvents)
	view.set(func(v *fakeView) { v.looking = true })
	notice(t, s, p, keeper.Notice{Kind: "blocked", Status: term.Status{State: "blocked"}})
	// Answered at the Mac, then they left.
	view.set(func(v *fakeView) { v.status = term.Status{State: "working"} })
	p.wait(t)
	view.set(func(v *fakeView) { v.looking = false })
	p.wait(t)
	// The next push the relay sees is the one after it.
	view.set(func(v *fakeView) { v.status = term.Status{State: "done"} })
	s.Notice(keeper.Notice{Kind: "done", Status: term.Status{State: "done"}})
	if got := relay.next(); got.note.State != "done" {
		t.Fatalf("a passed blocked was sent: %+v", got)
	}
}

func TestNotifyAnywaySendsWhileAnOwnerLooks(t *testing.T) {
	relay, view, s, _, _ := setUp(t, Events{Blocked: true, Done: true, Notify: true, WhenLooking: "send"})
	view.set(func(v *fakeView) { v.looking = true })
	s.Notice(keeper.Notice{Kind: "notify", Title: "Build", Body: "done"})
	if got := relay.next(); got.note.State != "notify" {
		t.Fatalf("with Notify Anyway: %+v", got)
	}
}

func TestABurstWaitsButBlockedNever(t *testing.T) {
	relay, view, s, _, _ := setUp(t, allEvents)
	start := time.Now()
	view.set(func(v *fakeView) { v.status = term.Status{State: "done"} })
	s.Notice(keeper.Notice{Kind: "done", Status: term.Status{State: "done"}})
	relay.next()
	view.set(func(v *fakeView) { v.status = term.Status{State: "blocked"} })
	s.Notice(keeper.Notice{Kind: "notify", Title: "Build", Body: "one"})
	s.Notice(keeper.Notice{Kind: "notify", Title: "Build", Body: "two"})
	s.Notice(keeper.Notice{Kind: "blocked", Status: term.Status{State: "blocked"}})
	if got := relay.next(); got.note.State != "blocked" {
		t.Fatalf("a held notice went before blocked: %+v", got)
	}
	got := relay.next()
	if got.note.State != "notify" || got.note.Text != "Build: two" || time.Since(start) < 300*time.Millisecond {
		t.Fatalf("the burst: %+v after %v", got, time.Since(start))
	}
}

func TestAKeyTheRelayDoesNotKnowWaitsAndOneRemovedGoes(t *testing.T) {
	relay, view, s, dir, p := setUp(t, allEvents)
	relay.answer(http.StatusNotFound)
	// The program says what each notice says: one that passed would not be sent.
	view.set(func(v *fakeView) { v.status = term.Status{State: "done"} })
	notice(t, s, p, keeper.Notice{Kind: "done", Status: term.Status{State: "done"}})
	relay.next()
	state := LoadState("device-tag-1")
	if state.UnknownSince == 0 || state.NextTry <= time.Now().Unix() || state.GivenUp {
		t.Fatalf("after 404: %+v", state)
	}
	// Seven days of 404: given up.
	SaveState("device-tag-1", State{UnknownSince: time.Now().Add(-8 * 24 * time.Hour).Unix()})
	relay.answer(http.StatusNotFound)
	view.set(func(v *fakeView) { v.status = term.Status{State: "blocked"} })
	notice(t, s, p, keeper.Notice{Kind: "blocked", Status: term.Status{State: "blocked"}})
	relay.next()
	if !LoadState("device-tag-1").GivenUp {
		t.Fatal("not given up after 7 days")
	}
	// The device writes its file again: taken up again, and a removed key takes the file with it.
	device := relay.device.PublicKey().Bytes()
	if _, err := Add(AddRequest{DeviceTag: "device-tag-1", Relay: relay.server.URL,
		SealKey: base64.StdEncoding.EncodeToString(device), HostRef: "host-ref", Owners: []string{"owner-device"},
		Events: allEvents}); err != nil {
		t.Fatal(err)
	}
	relay.answer(http.StatusGone)
	// It waits out the interval since the last push, so it goes on a later pass: that one is awaited.
	notice(t, s, p, keeper.Notice{Kind: "notify", Body: "x"})
	relay.next()
	p.wait(t)
	if _, err := os.Stat(filepath.Join(dir, "device-tag-1.json")); !os.IsNotExist(err) {
		t.Fatal("the file is still there after 410")
	}
}

func TestAKeyNotKnownYetIsAskedAboutSoonThenHourly(t *testing.T) {
	for _, c := range []struct {
		unknown int64
		want    time.Duration
	}{{0, time.Minute}, {30, time.Minute}, {300, 5 * time.Minute}, {86400, time.Hour}} {
		if got := unknownWait(c.unknown); got != c.want {
			t.Errorf("unknown for %d s: waits %v, not %v", c.unknown, got, c.want)
		}
	}
}

func TestAShellWithNoTitleIsNamedByWhatRunsInIt(t *testing.T) {
	relay, view, s, _, _ := setUp(t, allEvents)
	view.set(func(v *fakeView) { v.title = "" })
	s.Notice(keeper.Notice{Kind: "notify", Title: "Build", Body: "done"})
	if got := relay.next(); got.note.Shell != "python3" {
		t.Fatalf("the shell was named %q", got.note.Shell)
	}
}

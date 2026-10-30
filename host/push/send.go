// send.go
// Author: Kennt Kim
// Created: 2026-10-30 · Last updated: 2026-10-30
//
// Purpose: a keeper's notices sent to the devices that asked for them (Docs/design/08-PUSH.md §4):
// the keeper program's observer of its keeper (keeper.MainObserved). For each registration of this
// host, a notice the device wants, of a shell one of its owners started, goes when no owner is
// looking — sealed to the device's key for this host, signed with this host's key, through the relay
// in the registration.
//
// Notes:
// - One goroutine sends; Notice only hands a notice on, never blocking the keeper's output (it is
//   called under the keeper's lock), and drops it when 64 wait.
// - Per keeper, at most one push per 10 seconds; what comes meanwhile replaces the one held of its
//   kind — except blocked, which goes at once (Attention's rule, 04-APP.md §5).
// - Before every send, first or again, it looks again: nothing while an owner holds a lease; a
//   blocked notice only while the program still says it is blocked (answered at the Mac meanwhile,
//   it would arrive on the iPhone as still waiting, 08-PUSH.md §4.4).
// - While an owner looks (08-PUSH.md §4.2): a blocked or done is held, for up to `holdFor`, and
//   looked at again every `lookEvery`; once no owner looks it goes if the program still says the
//   same. A program's notification or a command's end, a moment seen where it happened, is
//   dropped. A registration whose rules say "send" holds nothing back.
// - Answers: 202 sent; 404 the relay does not know the key — tried again after as long as it has
//   been unknown, from a minute up to an hour, given up after 7 days, until the device writes the
//   file again; 410 the key was removed — the file goes;
//   429 tried after Retry-After; 503, a timeout, no connection: tried with backoff; anything held
//   longer than 10 minutes is dropped.
// - The time sent, in the request and sealed, is the relay's: every answer says it, and before the
//   first push to a relay its time is asked (GET /v1/time). A host whose clock is an hour off is
//   common, and the device drops what seems stale or from the future (§4.4, §6).

package push

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kennss/spectashell-keeper/keeper"

	"github.com/kennss/spectashell-keeper/host/pushwire"
)

// Sender sends a keeper's notices.
type Sender struct {
	view    keeper.View
	notices chan keeper.Notice
	client  *http.Client
	now     func() time.Time

	// interval is the least time between two pushes of the keeper; giveUp, how long a notice may
	// wait to be sent; backoff, the first pause after a relay's trouble; holdFor, how long a notice
	// waits for an owner to stop looking, looked at again every lookEvery.
	interval, giveUp, backoff, holdFor, lookEvery time.Duration

	mu      sync.Mutex
	offsets map[string]int64 // per relay: its clock minus ours, seconds
	known   map[string]bool  // relays whose time was asked or told

	// handled is told after each pass over what is held (tests wait on it).
	handled   func()
	userAgent string
}

// pending is a notice held for one registration.
type pending struct {
	tag      string
	notice   keeper.Notice
	due      time.Time
	first    time.Time
	attempts int
}

// Options set what tests change, and the program's name.
type Options struct {
	// UserAgent names the program to the relay — and to Cloudflare in front of it, whose browser
	// integrity check may refuse a request in Go's default name.
	UserAgent string
	RootCAs   *x509.CertPool
	Interval  time.Duration
	Backoff   time.Duration
	LookEvery time.Duration
	// Handled is told after each pass over what is held: a notice taken and what was due sent.
	Handled func()
}

// Observer makes the keeper program's observer of its keeper; build names the program to the relay.
func Observer(build string) func(keeper.View) keeper.Observer {
	return func(v keeper.View) keeper.Observer {
		return NewSender(v, Options{UserAgent: "spectashell-keeper/" + build})
	}
}

// NewSender makes a sender for v's notices and starts it.
func NewSender(v keeper.View, o Options) *Sender {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: o.RootCAs, MinVersion: tls.VersionTLS12}
	s := &Sender{view: v, notices: make(chan keeper.Notice, 64), now: time.Now,
		client: &http.Client{Transport: transport, Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		interval: 10 * time.Second, giveUp: 10 * time.Minute, backoff: 15 * time.Second,
		holdFor: time.Hour, lookEvery: 5 * time.Second,
		offsets: map[string]int64{}, known: map[string]bool{}, handled: o.Handled, userAgent: o.UserAgent}
	if s.userAgent == "" {
		s.userAgent = "spectashell-keeper"
	}
	if o.Interval > 0 {
		s.interval = o.Interval
	}
	if o.Backoff > 0 {
		s.backoff = o.Backoff
	}
	if o.LookEvery > 0 {
		s.lookEvery = o.LookEvery
	}
	go s.run()
	return s
}

// Notice hands n on; it never blocks.
func (s *Sender) Notice(n keeper.Notice) {
	select {
	case s.notices <- n:
	default:
	}
}

func (s *Sender) run() {
	var held []pending
	var last time.Time // the keeper's last push
	timer := time.NewTimer(time.Hour)
	for {
		now := s.now()
		next := now.Add(time.Hour)
		for _, p := range held {
			if p.due.Before(next) {
				next = p.due
			}
		}
		timer.Reset(next.Sub(now))
		select {
		case n := <-s.notices:
			held = s.hold(held, n, last)
		case <-timer.C:
		}
		now = s.now()
		var keep []pending
		for _, p := range held {
			if p.due.After(now) {
				keep = append(keep, p)
				continue
			}
			if again, ok := s.send(p); ok {
				keep = append(keep, again)
			} else {
				last = now
			}
		}
		held = keep
		if s.handled != nil {
			s.handled()
		}
	}
}

// hold adds n for each registration that wants it, replacing what is held of its kind.
func (s *Sender) hold(held []pending, n keeper.Notice, last time.Time) []pending {
	regs, _ := Registrations()
	now := s.now()
	for _, r := range regs {
		if !wants(r, n) {
			continue
		}
		due := now
		if n.Kind != "blocked" && last.Add(s.interval).After(now) {
			due = last.Add(s.interval)
		}
		held = slices.DeleteFunc(held, func(p pending) bool { return p.tag == r.Tag && p.notice.Kind == n.Kind })
		held = append(held, pending{tag: r.Tag, notice: n, due: due, first: now})
	}
	return held
}

// wants says whether registration r asked for notice n.
func wants(r Registration, n keeper.Notice) bool {
	switch n.Kind {
	case "blocked":
		return r.Events.Blocked
	case "done":
		return r.Events.Done
	case "notify":
		return r.Events.Notify
	case "command":
		return r.Events.CommandSeconds > 0 && n.Duration >= time.Duration(r.Events.CommandSeconds)*time.Second
	}
	return false
}

// send sends p; it returns p to hold again, and true, when it is to be tried later.
func (s *Sender) send(p pending) (pending, bool) {
	now := s.now()
	r, ok := s.registration(p.tag)
	if !ok {
		return p, false
	}
	state := LoadState(r.Tag)
	if state.GivenUp {
		return p, false
	}
	if state.NextTry > now.Unix() {
		return p, false // a key the relay does not know: not before the hour is up
	}
	// Only shells an owner started, and not while an owner looks.
	if by := s.view.StartedBy(); len(r.Owners) > 0 && by != nil && !slices.Contains(r.Owners, by.ClientID) {
		return p, false
	}
	if !r.Events.SendsWhileLooking() && s.view.Looking(r.Owners, now) {
		// Held until no owner looks — a blocked or done only, and not past the hour APNs would
		// keep it (§4.2).
		if (p.notice.Kind == "blocked" || p.notice.Kind == "done") && now.Sub(p.first) < s.holdFor {
			p.due = now.Add(s.lookEvery)
			return p, true
		}
		return p, false
	}
	// What has passed is not sent late: answered meanwhile, or back at work since it was done.
	if (p.notice.Kind == "blocked" || p.notice.Kind == "done") && s.view.Status().State != p.notice.Kind {
		return p, false
	}
	status, retryAfter := s.post(r, p.notice)
	switch {
	case status == http.StatusAccepted:
		if state.UnknownSince != 0 {
			SaveState(r.Tag, State{})
		}
		return p, false
	case status == http.StatusNotFound:
		if state.UnknownSince == 0 {
			state.UnknownSince = now.Unix()
		}
		state.GivenUp = now.Unix()-state.UnknownSince >= 7*24*60*60
		state.NextTry = now.Add(unknownWait(now.Unix() - state.UnknownSince)).Unix()
		SaveState(r.Tag, state)
		return p, false
	case status == http.StatusGone:
		Forget(r.Tag)
		return p, false
	}
	// Later, if not too late.
	p.attempts++
	switch {
	case status == http.StatusTooManyRequests && retryAfter > 0:
		p.due = now.Add(retryAfter)
	case status == http.StatusUnauthorized:
		p.due = now // the clock was corrected by the answer; once more at once
		if p.attempts > 1 {
			return p, false
		}
	case status == 0 || status == http.StatusTooManyRequests || status >= 500:
		p.due = now.Add(s.backoff << min(p.attempts-1, 5))
	default:
		return p, false // refused for good (a bad request): not again
	}
	if p.due.Sub(p.first) > s.giveUp {
		return p, false
	}
	return p, true
}

// shellName names the shell in a notification: its title, or, when a client left it none — a Mac
// that opened it named its pane anew (2026-10-30, "MacbookPro" alone on the iPhone) — what runs in
// it.
func (s *Sender) shellName() string {
	if title := strings.TrimSpace(s.view.Title()); title != "" {
		return title
	}
	return s.view.Program()
}

// unknownWait is how long a key the relay has not known for `unknown` seconds waits before it is
// tried again: as long as it has been unknown, from a minute up to an hour. A device whose
// registration was still being proven gives its key within a minute or two, and its first
// notifications should not wait an hour for it (the Mac, 2026-10-30); a key unknown for days is
// asked about hourly, not every minute.
func unknownWait(unknown int64) time.Duration {
	return min(max(time.Duration(unknown)*time.Second, time.Minute), time.Hour)
}

func (s *Sender) registration(tag string) (Registration, bool) {
	regs, _ := Registrations()
	for _, r := range regs {
		if r.Tag == tag {
			return r, true
		}
	}
	return Registration{}, false
}

// post sends n through r's relay; it returns the relay's status (0 when none came) and, with 429,
// when to try again.
func (s *Sender) post(r Registration, n keeper.Notice) (int, time.Duration) {
	if !s.knowsTime(r.Relay) {
		s.askTime(r.Relay)
	}
	at := s.relayTime()(r.Relay)
	state := n.Kind
	note := pushwire.Notification{V: 1, HostRef: r.HostRef, Keeper: s.view.KeeperID(), Shell: s.shellName(),
		Program: n.Status.App, State: state, At: at}
	switch n.Kind {
	case "blocked":
		note.Kind, note.Text = n.Status.Kind, n.Status.Message
	case "done":
		note.Text = n.Status.Message
	case "notify":
		note.Text = strings.TrimSpace(strings.Trim(n.Title+": "+n.Body, ": "))
	case "command":
		note.Text = n.Command
	}
	sealed, err := pushwire.Seal(r.sealing, r.KeyID, note)
	if err != nil {
		return http.StatusBadRequest, 0
	}
	tag, err := pushwire.Tag(r.signing, s.view.KeeperID())
	if err != nil {
		return http.StatusBadRequest, 0
	}
	interruption := pushwire.Active
	if n.Kind == "blocked" {
		interruption = pushwire.TimeSensitive
	}
	body, _ := json.Marshal(pushwire.PushRequest{KeyID: r.KeyID, Tag: tag, Urgent: n.Kind != "command",
		Interruption: interruption, Sealed: base64.StdEncoding.EncodeToString(sealed)})
	resp, err := s.do(r, http.MethodPost, "/v1/push", body, at)
	if err != nil {
		return 0, 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	var wait time.Duration
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		wait = time.Duration(seconds) * time.Second
	}
	return resp.StatusCode, wait
}

// do sends a signed request and learns the relay's time from its answer.
func (s *Sender) do(r Registration, method, path string, body []byte, at int64) (*http.Response, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	signed := pushwire.Signed{Method: method, Path: path, Time: strconv.FormatInt(at, 10),
		Nonce: base64.StdEncoding.EncodeToString(nonce)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(r.Relay, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set(pushwire.KeyHeader, r.KeyID)
	req.Header.Set(pushwire.TimeHeader, signed.Time)
	req.Header.Set(pushwire.NonceHeader, signed.Nonce)
	req.Header.Set(pushwire.SignatureHeader, base64.StdEncoding.EncodeToString(pushwire.Sign(r.signing, signed, body)))
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	s.learnTime(r.Relay, resp.Header.Get(pushwire.ServerTime))
	// Read the body before the context is cancelled.
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(b))
	return resp, nil
}

func (s *Sender) knowsTime(relay string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.known[relay]
}

// askTime asks a relay its time.
func (s *Sender) askTime(relay string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(relay, "/")+"/v1/time", nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", s.userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
	s.learnTime(relay, resp.Header.Get(pushwire.ServerTime))
}

// learnTime keeps the difference between a relay's clock, as an answer says it, and ours.
func (s *Sender) learnTime(relay, said string) {
	t, err := strconv.ParseInt(said, 10, 64)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offsets[relay] = t - s.now().Unix()
	s.known[relay] = true
}

// relayTime is a function giving a relay's time now, by its offset from ours.
func (s *Sender) relayTime() func(string) int64 {
	return func(relay string) int64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.now().Unix() + s.offsets[relay]
	}
}

// wire_test.go
// Author: Kennt Kim
// Created: 2026-10-30 · Last updated: 2026-10-30
//
// Purpose: the push wire (wire.go, Docs/design/08-PUSH.md §4.5): a signature holds only for its
// method, path, time, nonce and body; a key's id is its own; a tag is stable per keeper and differs per host;
// sealed bytes open with the device's key for that host only, bound to the key id, always one size;
// a program's words come cleaned and cut.

package pushwire

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func TestASignatureHoldsForItsRequestOnly(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	body := []byte(`{"key_id":"a"}`)
	signed := Signed{Method: "POST", Path: "/v1/push", Time: "100", Nonce: "n1"}
	sig := Sign(key, signed, body)
	if !Verify(pub, signed, body, sig) {
		t.Fatal("the request's own signature is refused")
	}
	for _, c := range []struct {
		s    Signed
		body string
	}{
		{Signed{"PUT", "/v1/push", "100", "n1"}, string(body)},
		{Signed{"POST", "/v1/devices", "100", "n1"}, string(body)},
		{Signed{"POST", "/v1/push", "101", "n1"}, string(body)},
		{Signed{"POST", "/v1/push", "100", "n2"}, string(body)},
		{signed, `{"key_id":"b"}`},
	} {
		if Verify(pub, c.s, []byte(c.body), sig) {
			t.Fatalf("a signature passed for %+v %s", c.s, c.body)
		}
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if Verify(other, signed, body, sig) {
		t.Fatal("another key's signature passed")
	}
}

func TestAKeysIDIsItsOwn(t *testing.T) {
	a, _, _ := ed25519.GenerateKey(rand.Reader)
	b, _, _ := ed25519.GenerateKey(rand.Reader)
	if KeyID(a) != KeyID(a) || KeyID(a) == KeyID(b) || len(KeyID(a)) != 32 {
		t.Fatalf("key ids: %q %q", KeyID(a), KeyID(b))
	}
}

func TestATagIsStablePerKeeperAndDiffersPerHost(t *testing.T) {
	_, host1, _ := ed25519.GenerateKey(rand.Reader)
	_, host2, _ := ed25519.GenerateKey(rand.Reader)
	t1, _ := Tag(host1, "k1")
	t1again, _ := Tag(host1, "k1")
	t2, _ := Tag(host1, "k2")
	other, _ := Tag(host2, "k1")
	if t1 != t1again || t1 == t2 || t1 == other || len(t1) != 64 {
		t.Fatalf("tags: %s %s %s %s", t1, t1again, t2, other)
	}
	if id := CollapseID("key", t1); len(id) != 64 || id == CollapseID("other key", t1) {
		t.Fatalf("collapse id: %s", id)
	}
}

func TestASealedNotificationOpensForItsHostOnly(t *testing.T) {
	device, _ := ecdh.X25519().GenerateKey(rand.Reader)
	n := Notification{V: 1, HostRef: "h", Keeper: "k1", Program: "Claude Code", State: "blocked",
		Kind: "permission", Text: "May I run make?", At: 1793342805}
	sealed, err := Seal(device.PublicKey().Bytes(), "key-a", n)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(device, "key-a", sealed)
	if err != nil || got != n {
		t.Fatalf("opened %+v, %v", got, err)
	}
	if _, err := Open(device, "key-b", sealed); err == nil {
		t.Fatal("sealed bytes opened under another key id")
	}
	stranger, _ := ecdh.X25519().GenerateKey(rand.Reader)
	if _, err := Open(stranger, "key-a", sealed); err == nil {
		t.Fatal("another device's key opened it")
	}
	// One size whatever it says.
	short, _ := Seal(device.PublicKey().Bytes(), "key-a", Notification{V: 1, Keeper: "k", State: "done", At: 1})
	if len(short) != len(sealed) {
		t.Fatalf("sizes differ: %d and %d", len(short), len(sealed))
	}
}

func TestALongNotificationIsCutToFit(t *testing.T) {
	device, _ := ecdh.X25519().GenerateKey(rand.Reader)
	n := Notification{V: 1, HostRef: strings.Repeat("h", 100), Keeper: strings.Repeat("k", 64),
		Shell: strings.Repeat("s", 60), Program: strings.Repeat("p", 60), State: "notify",
		Text: strings.Repeat("가", 200), At: 1}
	sealed, err := Seal(device.PublicKey().Bytes(), "key", n)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(device, "key", sealed)
	if err != nil || got.Text == "" || len(got.Text) > TextLimit || !strings.HasPrefix(n.Text, got.Text) {
		t.Fatalf("text %q, %v", got.Text, err)
	}
}

func TestAProgramsWordsAreCleaned(t *testing.T) {
	cases := map[string]string{
		"Needs\x1b[31m you\r\n  now": "Needs[31m you now",
		"evil‮gnp.exe":               "evilgnp.exe",
		"  \t  ":                     "",
	}
	for in, want := range cases {
		if got := CleanText(in); got != want {
			t.Errorf("CleanText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := CleanText(strings.Repeat("é", 200)); len(got) > TextLimit || !strings.HasPrefix(strings.Repeat("é", 200), got) {
		t.Fatalf("cut inside a character: %q", got)
	}
}

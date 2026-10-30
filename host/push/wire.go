// wire.go
// Author: Kennt Kim
// Created: 2026-10-30 · Last updated: 2026-10-30
//
// Purpose: what a host and SpectaShell's push relay say to each other, and how a notification is
// sealed for the device (Docs/design/08-PUSH.md §4.5, §5.1): the request a keeper sends, the bytes
// every request is signed over, a host key's id, the tag a keeper's notifications collapse under,
// and the notification itself, sealed with HPKE to the key the device made for this host. The
// keeper program sends with it (send.go) and the relay checks with it, so the two never differ.
//
// Notes:
// - Nothing here names a server: the relay's address is in the host's file, put there by the device
//   (08-PUSH.md §3.2). The engine is public.
// - Signed bytes: Domain, method, path, the time and the nonce (the headers TimeHeader and
//   NonceHeader), each followed by NUL, then the body exactly as sent — empty for a GET or a DELETE,
//   which the time and the nonce still bind. A signature is checked on those raw bytes before the
//   body is parsed (08-PUSH.md §5.1).
// - Sealed: HPKE base mode, DHKEM(X25519, HKDF-SHA256), HKDF-SHA256, ChaCha20-Poly1305 —
//   CryptoKit's Curve25519_SHA256_ChachaPoly — with info Domain, NUL, the host's key id, so sealed
//   bytes cannot pass as another key's. The result is the 32-byte encapsulated key, then the
//   ciphertext. The plaintext is padded with spaces after its JSON to PaddedSize, so its length
//   does not tell the state or the program.

package push

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/hpke"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Domain separates SpectaShell's push signatures and sealed payloads from anything else made with
// the same keys.
const Domain = "SpectaShell push v1"

// PaddedSize is the size, in bytes, every notification's plaintext is padded to before sealing.
const PaddedSize = 512

// TextLimit is the most bytes of a program's own words a notification carries.
const TextLimit = 180

// EncapsulatedKeySize is the length of the HPKE encapsulated key at the start of sealed bytes.
const EncapsulatedKeySize = 32

// The headers every signed request carries, and every answer's time.
const (
	TimeHeader      = "X-Push-Time"      // the request's time, Unix seconds, corrected by the relay's clock
	NonceHeader     = "X-Push-Nonce"     // 16 random bytes, base64 (std, padded), never sent twice
	SignatureHeader = "X-Push-Signature" // Ed25519 over SigningBytes, base64 (std, padded)
	KeyHeader       = "X-Push-Key"       // a push's host key id, so its signature is checked before its body is read
	ServerTime      = "X-Push-Server-Time"
)

// Interruption levels a notification may ask for (APNs's interruption-level).
const (
	TimeSensitive = "time-sensitive" // a permission request or a question: comes through a Focus
	Active        = "active"
	Passive       = "passive" // no sound, no lit screen
)

// PushRequest is the body of POST /v1/push (08-PUSH.md §4.5).
type PushRequest struct {
	KeyID        string `json:"key_id"`
	Tag          string `json:"tag"`          // Tag(sign key, keeper id): the keeper's notifications collapse under it
	Urgent       bool   `json:"urgent"`       // blocked, done, a program's notification: APNs priority 10
	Interruption string `json:"interruption"` // TimeSensitive, Active or Passive
	Sealed       string `json:"sealed"`       // Seal(…), base64 (std, padded)
}

// Notification is what is sealed: what the device shows, in its own words, of what happened.
type Notification struct {
	V       int    `json:"v"`
	HostRef string `json:"host_ref"` // the device's id for the host (§3.2)
	Keeper  string `json:"keeper"`
	Shell   string `json:"shell,omitempty"`
	Program string `json:"program,omitempty"`
	State   string `json:"state"`          // blocked, done, notify, command
	Kind    string `json:"kind,omitempty"` // with blocked: permission, question, auth
	Text    string `json:"text,omitempty"` // a program's own words, cleaned (CleanText)
	At      int64  `json:"at"`             // Unix seconds, corrected by the relay's clock
}

// Signed names what a request is signed over besides its body: its method and path, and the values
// of its time and nonce headers as sent.
type Signed struct {
	Method, Path, Time, Nonce string
}

// SigningBytes is what a request is signed over: Domain, method, path, time and nonce, each followed
// by NUL, then the raw body.
func SigningBytes(s Signed, body []byte) []byte {
	b := make([]byte, 0, len(Domain)+len(s.Method)+len(s.Path)+len(s.Time)+len(s.Nonce)+len(body)+5)
	for _, part := range []string{Domain, s.Method, s.Path, s.Time, s.Nonce} {
		b = append(b, part...)
		b = append(b, 0)
	}
	return append(b, body...)
}

// Sign signs a request with a host's or a device's key.
func Sign(key ed25519.PrivateKey, s Signed, body []byte) []byte {
	return ed25519.Sign(key, SigningBytes(s, body))
}

// Verify checks a request's signature.
func Verify(pub ed25519.PublicKey, s Signed, body, signature []byte) bool {
	return len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, SigningBytes(s, body), signature)
}

// KeyID is the id of a host's (or a device's) public key: the hex of the first 16 bytes of its
// SHA-256, so the same key always has the same id and adding it again changes nothing.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:16])
}

// Tag is the id a keeper's notifications collapse under: an HMAC of its keeper id under a key
// derived from the host's signing key — not the signing key itself, which signs (08-PUSH.md §4.5).
// The relay hashes it with the key id, so a host collapses only its own.
func Tag(signKey ed25519.PrivateKey, keeperID string) (string, error) {
	tagKey, err := hkdf.Key(sha256.New, signKey.Seed(), nil, Domain+" tag", 32)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, tagKey)
	mac.Write([]byte(keeperID))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// CollapseID is the APNs collapse id of a notification: the hex of SHA-256(key id, NUL, tag) — 64
// characters, APNs's limit.
func CollapseID(keyID, tag string) string {
	sum := sha256.Sum256([]byte(keyID + "\x00" + tag))
	return hex.EncodeToString(sum[:])
}

// suite returns the HPKE algorithms both sides use.
func suite() (hpke.KDF, hpke.AEAD) { return hpke.HKDFSHA256(), hpke.ChaCha20Poly1305() }

func info(keyID string) []byte { return []byte(Domain + "\x00" + keyID) }

// Seal pads n's JSON to PaddedSize and seals it to the device's X25519 public key for this host
// (32 bytes), bound to keyID.
func Seal(sealKey []byte, keyID string, n Notification) ([]byte, error) {
	pub, err := ecdh.X25519().NewPublicKey(sealKey)
	if err != nil {
		return nil, fmt.Errorf("seal key: %w", err)
	}
	pk, err := hpke.NewDHKEMPublicKey(pub)
	if err != nil {
		return nil, err
	}
	plain, err := pad(n)
	if err != nil {
		return nil, err
	}
	kdf, aead := suite()
	enc, sender, err := hpke.NewSender(pk, kdf, aead, info(keyID))
	if err != nil {
		return nil, err
	}
	sealed, err := sender.Seal(nil, plain)
	if err != nil {
		return nil, err
	}
	return append(enc, sealed...), nil
}

// Open is Seal's reverse, as the device's extension does it; here for tests and tools.
func Open(sealPrivate *ecdh.PrivateKey, keyID string, sealed []byte) (Notification, error) {
	var n Notification
	if len(sealed) <= EncapsulatedKeySize {
		return n, errors.New("sealed: too short")
	}
	sk, err := hpke.NewDHKEMPrivateKey(sealPrivate)
	if err != nil {
		return n, err
	}
	kdf, aead := suite()
	r, err := hpke.NewRecipient(sealed[:EncapsulatedKeySize], sk, kdf, aead, info(keyID))
	if err != nil {
		return n, err
	}
	plain, err := r.Open(nil, sealed[EncapsulatedKeySize:])
	if err != nil {
		return n, err
	}
	err = json.Unmarshal([]byte(strings.TrimRight(string(plain), " ")), &n)
	return n, err
}

// pad is n's JSON followed by spaces up to PaddedSize; text is cut further if it would not fit.
func pad(n Notification) ([]byte, error) {
	n.Text = CleanText(n.Text)
	for {
		b, err := json.Marshal(n)
		if err != nil {
			return nil, err
		}
		if len(b) <= PaddedSize {
			return append(b, []byte(strings.Repeat(" ", PaddedSize-len(b)))...), nil
		}
		if n.Text == "" {
			return nil, fmt.Errorf("notification: %d bytes without text, more than %d", len(b), PaddedSize)
		}
		n.Text = cut(n.Text, len(n.Text)-(len(b)-PaddedSize)-1)
	}
}

// CleanText is a program's words as a notification may carry them: control and bidirectional
// override characters taken out (any output can raise a notification, 04-APP.md §5), runs of space
// made one, cut to TextLimit bytes on a character's edge.
func CleanText(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == utf8.RuneError, unicode.IsControl(r), isBidiControl(r):
			continue
		case unicode.IsSpace(r):
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(r)
	}
	return cut(strings.TrimSpace(b.String()), TextLimit)
}

// isBidiControl reports the characters that reorder text as it is shown (U+202A–U+202E,
// U+2066–U+2069, and the marks U+200E, U+200F, U+061C).
func isBidiControl(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0x200E || r == 0x200F ||
		r == 0x061C
}

// cut is s cut to at most n bytes, never inside a character.
func cut(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

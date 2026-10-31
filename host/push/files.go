// files.go
// Author: Kennt Kim
// Created: 2026-10-30 · Last updated: 2026-10-31
//
// Purpose: a host's push registrations (Docs/design/08-PUSH.md §3.2): one file per device that
// turned notifications on for this machine, holding the key this host signs with — made here, never
// sent anywhere — the device's seal key (one per device, the same on every host), its name for the
// host, its owners and the events it wants; and beside each, what the keepers learned sending with
// it (a key the relay does not know since when, given up on, when to try again).
//
// Notes:
// - The folder is ~/.local/share/spectashell-push (SPECTASHELL_PUSH_DIR in tests), beside the
//   keeper program's folder and not in it: its installer removes every folder there but its builds
//   (core/internal/install, removeOld). It is 0700, a real folder owned by the user (lstat), as the
//   keepers' folder is; files are 0600, read with O_NOFOLLOW, refused with group or other bits.
// - Written to a temporary name and renamed, so a keeper never reads half of one. Several keepers
//   write a registration's state; the last write wins, which costs at most a retry.

package push

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/kennss/spectashell-keeper/host/pushwire"
)

// Registration is a device's file on this host.
type Registration struct {
	Version int      `json:"version"`
	Relay   string   `json:"relay"`
	KeyID   string   `json:"key_id"`
	SignKey string   `json:"sign_key"` // the Ed25519 seed, base64: made here (Add)
	SealKey string   `json:"seal_key"` // the device's X25519 public key (one per device), base64
	HostRef string   `json:"host_ref"`
	Owners  []string `json:"owners,omitempty"`
	Events  Events   `json:"events"`
	Tag     string   `json:"-"` // the device's tag: the file's name
	signing ed25519.PrivateKey
	sealing []byte
}

// Events are the notifications the device wants from this host: the user's rules (08-PUSH.md
// §3.3).
type Events struct {
	Blocked        bool `json:"blocked"`
	Done           bool `json:"done"`
	Notify         bool `json:"notify"`
	CommandSeconds int  `json:"command_seconds"` // 0: never
	// WhenLooking is what a notice that comes while an owner looks becomes: "hold" (or empty, a
	// file from before it) keeps a blocked or done until no owner looks; "send" sends it anyway.
	WhenLooking string `json:"when_looking,omitempty"`
}

// SendsWhileLooking says whether an owner looking keeps nothing back.
func (e Events) SendsWhileLooking() bool { return e.WhenLooking == "send" }

// State is what keepers learned sending with a registration.
type State struct {
	UnknownSince int64 `json:"unknown_since,omitempty"` // the relay has answered 404 since then
	NextTry      int64 `json:"next_try,omitempty"`      // nothing is sent before then
	GivenUp      bool  `json:"given_up,omitempty"`      // 404 for 7 days: nothing is sent until the device writes the file again
}

// Dir is the folder of this host's registrations.
func Dir() (string, error) {
	if d := os.Getenv("SPECTASHELL_PUSH_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "spectashell-push"), nil
}

var tagPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// checkDir makes dir (0700) if it is not there, and refuses one that is a link, not a folder, not
// the user's, or open to others.
func checkDir(dir string, make bool) error {
	if make {
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return err
		}
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !info.IsDir():
		return fmt.Errorf("%s is not a folder", dir)
	case !ok || int(st.Uid) != os.Getuid():
		return fmt.Errorf("%s is not the user's", dir)
	case info.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("%s is open to others (%v)", dir, info.Mode().Perm())
	}
	return nil
}

// readPrivate reads a file that must be the user's alone: no link, no group or other bits.
func readPrivate(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is not private", path)
	}
	return io.ReadAll(io.LimitReader(f, 64<<10))
}

// writePrivate writes b to path (0600) under a temporary name renamed into place.
func writePrivate(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// parse reads a registration's keys; an error for one that cannot be used.
func (r *Registration) parse() error {
	seed, err := base64.StdEncoding.DecodeString(r.SignKey)
	if err != nil || len(seed) != ed25519.SeedSize {
		return errors.New("sign key")
	}
	r.signing = ed25519.NewKeyFromSeed(seed)
	r.sealing, err = base64.StdEncoding.DecodeString(r.SealKey)
	if err != nil || len(r.sealing) != 32 {
		return errors.New("seal key")
	}
	if r.KeyID != pushwire.KeyID(r.signing.Public().(ed25519.PublicKey)) {
		return errors.New("key id")
	}
	if err := checkRelay(r.Relay); err != nil {
		return err
	}
	return nil
}

// checkRelay accepts an https address, and plain http to this machine alone (a relay of tests).
func checkRelay(relay string) error {
	switch {
	case strings.HasPrefix(relay, "https://"):
		return nil
	case strings.HasPrefix(relay, "http://127.0.0.1:"), strings.HasPrefix(relay, "http://localhost:"):
		return nil
	}
	return fmt.Errorf("relay %q is not https", relay)
}

// Registrations are this host's, each with its state; files that cannot be used are left out.
func Registrations() ([]Registration, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	if err := checkDir(dir, false); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var out []Registration
	for _, path := range paths {
		tag := strings.TrimSuffix(filepath.Base(path), ".json")
		if !tagPattern.MatchString(tag) {
			continue
		}
		b, err := readPrivate(path)
		if err != nil {
			continue
		}
		var r Registration
		if json.Unmarshal(b, &r) != nil || r.Version != 1 || r.parse() != nil {
			continue
		}
		r.Tag = tag
		out = append(out, r)
	}
	return out, nil
}

func statePath(dir, tag string) string { return filepath.Join(dir, tag+".state") }

// LoadState is a registration's state; empty when none was written.
func LoadState(tag string) State {
	var s State
	dir, err := Dir()
	if err != nil {
		return s
	}
	if b, err := readPrivate(statePath(dir, tag)); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// SaveState writes a registration's state.
func SaveState(tag string, s State) {
	dir, err := Dir()
	if err != nil {
		return
	}
	b, _ := json.Marshal(s)
	_ = writePrivate(statePath(dir, tag), b)
}

// Forget deletes a registration and its state (the relay answered 410).
func Forget(tag string) {
	dir, err := Dir()
	if err != nil {
		return
	}
	os.Remove(filepath.Join(dir, tag+".json"))
	os.Remove(statePath(dir, tag))
}

// cli.go
// Author: Kennt Kim
// Created: 2026-10-30 · Last updated: 2026-10-30
//
// Purpose: the keeper program's push commands, which the app runs over the user's own SSH login
// (Docs/design/08-PUSH.md §3.2): `push add` writes a device's registration, making the key this
// host signs with here — it never leaves the host; only its public half is printed, for the device
// to give the relay — `push remove` deletes one, and `push list` says which there are, without
// their keys.
//
// Notes:
// - add, given a device's tag that has a file already, keeps that file's key (the device changed
//   its settings) unless new_key is asked; adding the same public key to the relay again changes
//   nothing. It clears the registration's state: a device writing its file again is how a host
//   given up on (404 for 7 days) is taken up again.

package push

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kennss/spectashell-keeper/host/pushwire"
)

// AddRequest is what `push add` reads on standard input.
type AddRequest struct {
	DeviceTag string   `json:"device_tag"`
	Relay     string   `json:"relay"`
	SealKey   string   `json:"seal_key"`
	HostRef   string   `json:"host_ref"`
	Owners    []string `json:"owners"`
	Events    Events   `json:"events"`
	NewKey    bool     `json:"new_key,omitempty"`
}

// AddResult is what it prints.
type AddResult struct {
	KeyID  string `json:"key_id"`
	Public string `json:"public"`
	// Replaced is the key id the file had, when new_key made another: the device removes it at the
	// relay.
	Replaced string `json:"replaced,omitempty"`
}

// ListEntry is one registration as `push list` prints it.
type ListEntry struct {
	DeviceTag string `json:"device_tag"`
	KeyID     string `json:"key_id"`
	HostRef   string `json:"host_ref"`
	Relay     string `json:"relay"`
	GivenUp   bool   `json:"given_up,omitempty"`
}

// Command runs `push <args>`, reading standard input and writing its JSON answer to out.
func Command(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: push add | push remove <device tag> | push list")
	}
	var result any
	var err error
	switch args[0] {
	case "add":
		var req AddRequest
		if err = json.NewDecoder(io.LimitReader(in, 64<<10)).Decode(&req); err == nil {
			result, err = Add(req)
		}
	case "remove":
		if len(args) != 2 {
			return errors.New("usage: push remove <device tag>")
		}
		var keyID string
		keyID, err = Remove(args[1])
		result = map[string]string{"key_id": keyID}
	case "list":
		result, err = List()
	default:
		return fmt.Errorf("push: unknown command %q", args[0])
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}

// Add writes a device's registration.
func Add(req AddRequest) (AddResult, error) {
	var result AddResult
	if !tagPattern.MatchString(req.DeviceTag) {
		return result, errors.New("device tag")
	}
	if seal, err := base64.StdEncoding.DecodeString(req.SealKey); err != nil || len(seal) != 32 {
		return result, errors.New("seal key")
	}
	if err := checkRelay(req.Relay); err != nil {
		return result, err
	}
	if req.HostRef == "" {
		return result, errors.New("host ref")
	}
	dir, err := Dir()
	if err != nil {
		return result, err
	}
	if err := checkDir(dir, true); err != nil {
		return result, err
	}
	path := filepath.Join(dir, req.DeviceTag+".json")
	var seed []byte
	if b, err := readPrivate(path); err == nil {
		var had Registration
		if json.Unmarshal(b, &had) == nil && had.parse() == nil {
			if req.NewKey {
				result.Replaced = had.KeyID
			} else {
				seed = had.signing.Seed()
			}
		}
	}
	if seed == nil {
		seed = make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return result, err
		}
	}
	key := ed25519.NewKeyFromSeed(seed)
	public := key.Public().(ed25519.PublicKey)
	r := Registration{Version: 1, Relay: req.Relay, KeyID: pushwire.KeyID(public), SignKey: base64.StdEncoding.EncodeToString(seed),
		SealKey: req.SealKey, HostRef: req.HostRef, Owners: req.Owners, Events: req.Events}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return result, err
	}
	if err := writePrivate(path, b); err != nil {
		return result, err
	}
	os.Remove(statePath(dir, req.DeviceTag))
	result.KeyID, result.Public = r.KeyID, base64.StdEncoding.EncodeToString(public)
	return result, nil
}

// Remove deletes a device's registration; it says the key id it had, for the device to remove at
// the relay, or "" when there was none.
func Remove(tag string) (string, error) {
	if !tagPattern.MatchString(tag) {
		return "", errors.New("device tag")
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	var keyID string
	if b, err := readPrivate(filepath.Join(dir, tag+".json")); err == nil {
		var r Registration
		if json.Unmarshal(b, &r) == nil {
			keyID = r.KeyID
		}
	}
	Forget(tag)
	return keyID, nil
}

// List is this host's registrations, without their keys.
func List() ([]ListEntry, error) {
	regs, err := Registrations()
	if err != nil {
		return nil, err
	}
	out := []ListEntry{}
	for _, r := range regs {
		out = append(out, ListEntry{DeviceTag: r.Tag, KeyID: r.KeyID, HostRef: r.HostRef, Relay: r.Relay,
			GivenUp: LoadState(r.Tag).GivenUp})
	}
	return out, nil
}

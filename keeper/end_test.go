// end_test.go
// Author: Kennt Kim
// Created: 2026-10-26 · Last updated: 2026-10-26
//
// Purpose: what a keeper leaves when it ends (End, Docs/KEEPER.md §4): hung up, it says so and
// names the client the Hangup named, or no one; hung up after its program ended, it keeps that
// exit; found gone without a word, a listing says it was lost and keeps the exit its description
// had; and Ends lists them, removing those older than endKept and those that are not an End.

//go:build unix

package keeper

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// endOf waits for keeper id's End in dir.
func endOf(t *testing.T, dir, id string) End {
	t.Helper()
	var found End
	waitFor(t, "the End of "+id, func() bool {
		for _, e := range Ends(dir) {
			if e.KeeperID == id {
				found = e
				return true
			}
		}
		return false
	})
	return found
}

// Kennt, 2026-10-26: the Mac closed two shells, and the iPads' tabs could only say their programs
// had ended.
func TestAKeeperHungUpSaysWhoClosedIt(t *testing.T) {
	dir := shortDir(t)
	const id = "0123456789abcdef"
	c := startKeeper(t, dir, id, []string{"/bin/sh"})
	r := record(c)
	r.wait(t, "the replay", func(_ string, replayEnd int, _ *Exit) bool { return replayEnd >= 0 })
	before := time.Now().Unix()
	c.Hangup(Who{Client: "MacBook Pro", ClientID: "mac-id"})
	end := endOf(t, dir, id)
	if end.How != EndClosed || end.By == nil || *end.By != (Who{Client: "MacBook Pro", ClientID: "mac-id"}) ||
		end.Exit != nil || end.Ended < before || end.Version != Version {
		t.Fatalf("End = %+v (by %+v)", end, end.By)
	}
	// Its description and socket went; only its End stays.
	if len(List(dir)) != 0 {
		t.Fatalf("listed after it ended: %+v", List(dir))
	}
	if _, err := os.Stat(SocketPath(dir, id)); err == nil {
		t.Fatal("the socket outlived the keeper")
	}
	info, err := os.Stat(EndPath(SocketPath(dir, id)))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the End file: %v %v", info, err)
	}
}

// A client from before Who sends Hangup with nothing: the End names no one.
func TestAKeeperHungUpByAnUnnamedClientNamesNoOne(t *testing.T) {
	dir := shortDir(t)
	const id = "1123456789abcdef"
	c := startKeeper(t, dir, id, []string{"/bin/sh"})
	r := record(c)
	r.wait(t, "the replay", func(_ string, replayEnd int, _ *Exit) bool { return replayEnd >= 0 })
	c.Hangup(Who{})
	if end := endOf(t, dir, id); end.How != EndClosed || end.By != nil {
		t.Fatalf("End = %+v (by %+v)", end, end.By)
	}
}

// A shell that ended by itself, then removed by a client: the End keeps how its program ended.
func TestAKeeperClosedAfterItsProgramEndedKeepsTheExit(t *testing.T) {
	dir := shortDir(t)
	const id = "2123456789abcdef"
	c := startKeeper(t, dir, id, []string{"/bin/sh"})
	r := record(c)
	r.wait(t, "the replay", func(_ string, replayEnd int, _ *Exit) bool { return replayEnd >= 0 })
	_ = c.Input([]byte("exit 3\n"))
	r.wait(t, "the exit", func(_ string, _ int, exit *Exit) bool { return exit != nil })
	c.Hangup(Who{Client: "iPad", ClientID: "ipad-id"})
	end := endOf(t, dir, id)
	if end.How != EndClosed || end.Exit == nil || end.Exit.Code != 3 || end.By == nil || end.By.ClientID != "ipad-id" {
		t.Fatalf("End = %+v (exit %+v, by %+v)", end, end.Exit, end.By)
	}
}

// A keeper killed — or its machine gone down — says nothing; the listing that finds it gone says it
// was lost, with the exit its description had, and does not write over an End a keeper left.
func TestAKeeperFoundGoneIsLost(t *testing.T) {
	dir := shortDir(t)
	const id = "3123456789abcdef"
	c := startKeeper(t, dir, id, []string{"/bin/sh"})
	r := record(c)
	r.wait(t, "the replay", func(_ string, replayEnd int, _ *Exit) bool { return replayEnd >= 0 })
	pid := List(dir)[0].KeeperPID
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the keeper to die", func() bool { return !alive(pid) })
	if len(List(dir)) != 0 {
		t.Fatal("a dead keeper was listed")
	}
	if end := endOf(t, dir, id); end.How != EndLost || end.By != nil {
		t.Fatalf("End = %+v", end)
	}

	// A description left by a keeper gone, whose End is already there: the End stays as it was.
	const other = "4123456789abcdef"
	said := End{Version: Version, KeeperID: other, Ended: time.Now().Unix(), How: EndClosed, By: &Who{Client: "Mac"}}
	writeEnd(filepath.Join(dir, other+".end"), said, true)
	os.WriteFile(filepath.Join(dir, other+".json"),
		[]byte(`{"version":1,"keeper_id":"`+other+`","keeper_pid":`+strconv.Itoa(pid)+`}`), 0o600)
	List(dir)
	if end := endOf(t, dir, other); end.How != EndClosed || end.By == nil || end.By.Client != "Mac" {
		t.Fatalf("the End was written over: %+v", end)
	}
}

func TestEndsRemovesOldAndBadOnes(t *testing.T) {
	dir := shortDir(t)
	now := time.Now()
	fresh := End{Version: Version, KeeperID: "5123456789abcdef", Ended: now.Unix(), How: EndClosed}
	old := End{Version: Version, KeeperID: "6123456789abcdef", Ended: now.Add(-endKept - time.Hour).Unix(), How: EndClosed}
	writeEnd(filepath.Join(dir, fresh.KeeperID+".end"), fresh, true)
	writeEnd(filepath.Join(dir, old.KeeperID+".end"), old, true)
	// Not an End, and an End under another keeper's name.
	os.WriteFile(filepath.Join(dir, "7123456789abcdef.end"), []byte("not json"), 0o600)
	writeEnd(filepath.Join(dir, "8123456789abcdef.end"), fresh, true)

	ends := Ends(dir)
	if len(ends) != 1 || ends[0].KeeperID != fresh.KeeperID {
		t.Fatalf("Ends = %+v", ends)
	}
	for _, gone := range []string{old.KeeperID, "7123456789abcdef", "8123456789abcdef"} {
		if _, err := os.Stat(filepath.Join(dir, gone+".end")); err == nil {
			t.Errorf("%s.end was kept", gone)
		}
	}
}

// peer_test.go
// Author: Kennt Kim
// Created: 2026-10-29 · Last updated: 2026-10-29
//
// Purpose: a connection whose client went with output unread (peer.go): the keeper's write fails,
// and what the client sent before it went — its Hangup — is still read.

//go:build unix

package keeper

import (
	"bufio"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestAHangupSentBeforeTheClientWentIsReadThoughAWriteFailed(t *testing.T) {
	path := filepath.Join(shortDir(t), "p.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	p := newPeer(server, false)
	// The client hangs up and goes, reading nothing.
	if err := writeFrame(client, frameHangup, nil); err != nil {
		t.Fatal(err)
	}
	client.Close()
	// Output for it fails to be written.
	frame, _ := encodeFrame(frameOutput, make([]byte, 64<<10))
	closed := func() bool {
		p.qmu.Lock()
		defer p.qmu.Unlock()
		return p.closed
	}
	for !closed() {
		p.push(frame)
		p.waitClosed(time.Now().Add(100 * time.Millisecond))
	}
	kind, _, err := readFrame(bufio.NewReader(server))
	if err != nil || kind != frameHangup {
		t.Fatalf("read %d, %v: the Hangup was lost", kind, err)
	}
	p.close()
}

// procfs_ports_test.go
// Author: Kennt Kim
// Created: 2026-10-17 · Last updated: 2026-10-23
//
// Purpose: the ports a terminal's processes listen on, through Linux's /proc reader from a /proc
// made for the test (procfs_ports.go) — so it runs on any machine: loopback and every-address
// listeners kept (IPv4 and IPv6), one bound to another address, connected sockets and other
// processes' listeners left out, a port in both tables listed once with how the session's sockets
// are bound, the program of the lowest process holding it; nothing for a session without sockets.
//
// Notes:
// - Taken from SiliconDesk's host/internal/ports/ports_test.go on 2026-10-23 (TestListeners); its
//   probing tests went with Services, which is not here.

package paneinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fakeNet writes /proc/net/tcp and tcp6 under root.
func fakeNet(t *testing.T, root string) {
	t.Helper()
	os.MkdirAll(filepath.Join(root, "net"), 0o755)
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	line := func(addr, state, inode string) string {
		return fmt.Sprintf("   0: %s 00000000:0000 %s 00000000:00000000 00:00000000 00000000  1000        0 %s 1 0 100 0 0 10 0\n",
			addr, state, inode)
	}
	tcp := header +
		line("0100007F:1FFC", "0A", "111") + // 127.0.0.1:8188
		line("00000000:22B8", "0A", "222") + // 0.0.0.0:8888
		line("1401A8C0:0050", "0A", "333") + // 192.168.1.20:80: another address
		line("0100007F:1FFC", "01", "444") + // an established connection
		line("0100007F:0BB8", "0A", "777") // 127.0.0.1:3000, another terminal's
	tcp6 := header +
		line("00000000000000000000000001000000:1F90", "0A", "555") + // [::1]:8080
		line("00000000000000000000000000000000:22B8", "0A", "666") // [::]:8888, again
	os.WriteFile(filepath.Join(root, "net", "tcp"), []byte(tcp), 0o644)
	os.WriteFile(filepath.Join(root, "net", "tcp6"), []byte(tcp6), 0o644)
}

// fds gives pid open descriptors pointing at targets.
func fds(t *testing.T, root, pid string, targets ...string) {
	t.Helper()
	dir := filepath.Join(root, pid, "fd")
	os.MkdirAll(dir, 0o755)
	for i, target := range targets {
		if err := os.Symlink(target, filepath.Join(dir, fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestThePortsASessionListensOn(t *testing.T) {
	root := t.TempDir()
	fakeNet(t, root)
	fds(t, root, "100", "/dev/null", "socket:[111]", "socket:[333]", "socket:[444]", "pipe:[9]")
	fds(t, root, "200", "socket:[666]", "socket:[555]")
	fds(t, root, "201", "socket:[666]") // a worker that inherited its parent's socket
	fds(t, root, "900", "socket:[777]") // not in the session
	s := newSnapshot(map[int]process{
		100: {comm: "python3"}, 200: {comm: "jupyter-lab"}, 201: {comm: "jupyter-worker"}, 300: {comm: "bash"},
	})
	got := procfs{root: root}.ports(s)
	want := []Port{{Port: 8080, Process: "jupyter-lab"}, {Port: 8188, Process: "python3"},
		{Port: 8888, Everywhere: true, Process: "jupyter-lab"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	// Only the session's own sockets say how a port is bound: 8888 on loopback here, though another
	// process listens on it everywhere.
	fds(t, root, "400", "socket:[222]")
	other := newSnapshot(map[int]process{100: {comm: "python3"}})
	if got := (procfs{root: root}).ports(other); len(got) != 1 || got[0].Port != 8188 || got[0].Everywhere {
		t.Fatalf("session's own %+v", got)
	}
	// No sockets: none, and no table read (there is none to read here).
	if got := (procfs{root: t.TempDir()}).ports(newSnapshot(map[int]process{1: {}})); got != nil {
		t.Fatalf("no sockets %+v", got)
	}
}

func TestParseAddress(t *testing.T) {
	for in, want := range map[string]string{
		"0100007F:1FFC":                         "127.0.0.1:8188",
		"00000000000000000000000001000000:1F90": "[::1]:8080",
		"0000000000000000FFFF00000100007F:0016": "127.0.0.1:22",
	} {
		ip, port, err := parseAddress(in)
		if err != nil || fmt.Sprintf("%s:%d", ip, port) != want && fmt.Sprintf("[%s]:%d", ip, port) != want {
			t.Errorf("%s: %s %d %v, want %s", in, ip, port, err, want)
		}
	}
	for _, bad := range []string{"", "0100007F", "XX00007F:0016", "0100007F:ZZZZ", "01007F:0016"} {
		if _, _, err := parseAddress(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

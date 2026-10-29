// procfs_ports.go
// Author: Kennt Kim
// Created: 2026-10-17 · Last updated: 2026-10-23
//
// Purpose: the TCP ports a terminal's processes listen on, on Linux (system.go's ports): the
// listening sockets of /proc/net/tcp and tcp6 whose inodes the session's processes hold open,
// reachable through loopback, each with the program holding it.
//
// Notes:
// - Taken from SiliconDesk's host/internal/ports/ports.go on 2026-10-23: its reading of
//   /proc/net/tcp (sockets, parseAddress) and its lookup by process (HeldBy). Its probing of web
//   services (Services) is SiliconDesk's sdweb's and is not here.
// - Listening sockets are state 0A. Only those bound to loopback or to every address are kept: a
//   port is opened through 127.0.0.1. One bound to every address is Everywhere: other machines on
//   the network reach it too.
// - Narrower than SiliconDesk's, which read the tables first for every terminal at once: here the
//   session's descriptors are read first (`readlink /proc/<pid>/fd/*`, 472 descriptors of 27
//   processes in 1.7 ms on SiliconDesk's box), and the tables only when the session holds a socket
//   at all, keeping only the lines of its sockets — so Everywhere says how the session's own
//   sockets are bound, not another process's on the same port.
// - The program is the comm of the lowest process holding one of the port's sockets. Only the
//   user's own processes can be read, so a process run with sudo is not seen. Docker's published
//   ports belong to docker-proxy, a root process outside the session, and are not listed.

package paneinfo

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const stateListen = "0A"

func (p procfs) ports(s *snapshot) []Port {
	// The socket inodes the session's processes hold, each with the lowest process holding it.
	holders := map[string]int{}
	for _, pid := range s.pids {
		for _, target := range p.fdLinks(pid) {
			inode, ok := strings.CutPrefix(target, "socket:[")
			if !ok {
				continue
			}
			inode = strings.TrimSuffix(inode, "]")
			if _, seen := holders[inode]; !seen {
				holders[inode] = pid
			}
		}
	}
	if len(holders) == 0 {
		return nil
	}
	set := portSet{}
	for _, name := range []string{"tcp", "tcp6"} {
		p.listening(filepath.Join(p.root, "net", name), func(ip net.IP, port uint16, inode string) {
			if pid, ok := holders[inode]; ok {
				set.add(ip, port, pid)
			}
		})
	}
	return set.list(func(pid int) string { return s.procs[pid].comm })
}

// listening calls found with each listening socket of a /proc/net/tcp table: its address, port
// and inode. A table that cannot be read (no IPv6) gives none.
func (p procfs) listening(table string, found func(ip net.IP, port uint16, inode string)) {
	f, err := os.Open(table)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Scan() // header
	for scanner.Scan() {
		// sl local_address rem_address st tx_queue:rx_queue tr:tm->when retrnsmt uid timeout inode …
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[3] != stateListen {
			continue
		}
		ip, port, err := parseAddress(fields[1])
		if err != nil {
			continue
		}
		found(ip, port, fields[9])
	}
}

// parseAddress reads /proc/net/tcp's "0100007F:20FB" (each 32-bit word of the address in host byte
// order — little-endian on every Linux this runs on — and the port in hex).
func parseAddress(s string) (net.IP, uint16, error) {
	host, portHex, ok := strings.Cut(s, ":")
	if !ok {
		return nil, 0, fmt.Errorf("bad address %q", s)
	}
	raw, err := hex.DecodeString(host)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return nil, 0, fmt.Errorf("bad address %q", s)
	}
	for i := 0; i < len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return nil, 0, fmt.Errorf("bad port %q", s)
	}
	return net.IP(raw), uint16(port), nil
}

// main.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-11-02
//
// Purpose: spectashell-keeper, the program that holds a shell (Docs/design/02-CONNECTIONS.md §1,
// 02-CONNECTIONS.md §3; the keeper itself is the engine's, engine/keeper). The Mac app carries it
// for its local shells; the core puts it on hosts that allow it and drives it there over SSH exec
// channels.
//
// Usage:
//   spectashell-keeper keep
//       the keeper itself: a keeper.Config on standard input (keeper.Main)
//   spectashell-keeper start
//       a StartRequest (JSON) on standard input; starts a keeper for it in a session of its own and
//       prints {"keeper_id": …} — or {"error": …}
//   spectashell-keeper attach <id> [--agent]
//       copies standard input and output to that keeper's socket, unchanged: the keeper protocol
//       travels through an exec channel; with --agent, points the keeper's agent link at
//       SSH_AUTH_SOCK, the agent forwarded to this channel, while it holds the keeper
//   spectashell-keeper join <id>
//       the same through the keeper's socket for joining, to watch without taking it over
//       (Docs/design/07-TEAMS.md §1); exits 3, saying so, when the keeper is from a build that
//       cannot be joined
//   spectashell-keeper list
//       prints {"boot": …, "keepers": [descriptions]} without connecting to any keeper (connecting
//       would take it over)
//   spectashell-keeper version
//       prints "spectashell-keeper <build>", for the installer
//   spectashell-keeper builds
//       prints {"builds": […]}: the builds a keeper here, or any other program of this user's, runs
//       from, whose folders the installer keeps (hostkeeper.BuildsInUse)
//   spectashell-keeper page <cmd>
//       a coding agent's command to the page beside its shell in SpectaShell (host/pages, cli.go;
//       Docs/design/05-WINDOWS.md §3)
//   spectashell-keeper mcp
//       the same commands as an MCP server on standard input and output
//   spectashell-keeper serial [options] <device>, spectashell-keeper telnet <host> [port]
//       a serial port, or a telnet session, as the program a keeper runs where a shell would be
//       (console.go; Docs/design/02-CONNECTIONS.md §6)
//   spectashell-keeper serial-ports
//       prints {"ports": […]}: this machine's serial ports
//   spectashell-keeper pages-relay <device>
//       run by the core over an exec channel: the device's socket for page commands on this host,
//       passed through the channel (pages/relay.go)
//   spectashell-keeper push add | push remove <device tag> | push list
//       a device's push registration on this host (host/push, cli.go; SpectaShell's
//       Docs/design/08-PUSH.md §3.2): add reads it on standard input, makes this host's signing key
//       here and prints its public half; remove deletes one; list prints them without their keys
//
// Notes:
// - build is set at link time (-ldflags "-X main.build=…") by scripts/build-keeper.sh.
// - The keepers' folder is the engine's keeper.Place: /tmp/spectashell-<uid>/keepers on Linux,
//   ~/Library/Application Support/SpectaShell/keepers on macOS, or SPECTASHELL_RUNTIME_DIR; it must be
//   the user's own, or nothing starts (02-CONNECTIONS.md §3).
// - start prepares the program as the Mac app's core does for a local shell (termhost.Prepare):
//   the login shell with the terminal's variables, LANG from the request, and shell integration,
//   whose scripts it writes beside this binary (shell-integration/), from the binary itself.
// - A shell started with the agent forwarded (StartRequest.Agent) has SSH_AUTH_SOCK name the
//   keeper's agent link, which `attach <id> --agent` points at the agent forwarded to its channel
//   while it holds the keeper (keeper/agent.go): a kept shell outlives every connection, and each
//   forwards an agent of its own (Docs/design/02-CONNECTIONS.md §3).
// - start puts the new keeper's id in the shell's environment (SPECTASHELL_KEEPER_ID), so a page
//   command run in it, or in a program the shell started and its parents no longer lead to (a tmux
//   server), finds its shell (pages/shell.go); and the keepers' folder, when
//   SPECTASHELL_RUNTIME_DIR put it elsewhere, since the shell starts from its login session's
//   variables only.
// - boot is what changes when the host restarts (Linux's boot_id, the Mac's kern.boottime;
//   host/hostboot), so the app can tell keepers that ended with a restart from keepers
//   that were closed (02-CONNECTIONS.md §3).
// - Moved here from core/cmd/spectashell-keeper on 2026-10-29, with the keeper program
//   (Docs/design/01-DESIGN.md §3.2).

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/kennss/spectashell-keeper/host/hostboot"
	"github.com/kennss/spectashell-keeper/host/hostkeeper"
	"github.com/kennss/spectashell-keeper/host/pages"
	"github.com/kennss/spectashell-keeper/host/push"
	"github.com/kennss/spectashell-keeper/keeper"
	"github.com/kennss/spectashell-keeper/termhost"
)

// The program's messages are hostkeeper's, so the core reads exactly what is printed here.
type (
	StartRequest = hostkeeper.StartRequest
	StartAnswer  = hostkeeper.StartAnswer
	ListAnswer   = hostkeeper.ListAnswer
)

var build = "dev"

// place is where this host's keepers are.
var place = keeper.Place{TempName: "spectashell", AppSupportName: "SpectaShell", Override: "SPECTASHELL_RUNTIME_DIR"}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "keep":
		// The keeper tells a device that asked, and is away, what its program needs (host/push).
		os.Exit(keeper.MainObserved(push.Observer(build)))
	case "push":
		if err := push.Command(os.Args[2:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "push:", err)
			os.Exit(1)
		}
	case "start":
		answer := start(os.Stdin)
		_ = json.NewEncoder(os.Stdout).Encode(answer)
		if answer.Error != "" {
			os.Exit(1)
		}
	case "attach":
		agent := len(os.Args) == 4 && os.Args[3] == hostkeeper.AgentFlag
		if len(os.Args) != 3 && !agent {
			usage()
		}
		if err := attach(os.Args[2], agent, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "spectashell-keeper attach:", err)
			os.Exit(1)
		}
	case "join":
		if len(os.Args) != 3 {
			usage()
		}
		if err := join(os.Args[2], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "spectashell-keeper join:", err)
			if errors.Is(err, keeper.ErrCannotJoin) {
				os.Exit(hostkeeper.ExitCannotJoin)
			}
			os.Exit(1)
		}
	case "list":
		_ = json.NewEncoder(os.Stdout).Encode(list())
	case "version":
		fmt.Printf("spectashell-keeper %s\n", build)
	case "builds":
		dir, err := place.Dir()
		if err != nil {
			_ = json.NewEncoder(os.Stdout).Encode(hostkeeper.BuildsAnswer{Builds: []string{}, Error: err.Error()})
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(hostkeeper.BuildsInUse(dir))
	case "page":
		dir, err := place.Dir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "spectashell-keeper page:", err)
			os.Exit(1)
		}
		os.Exit(pages.Command(pages.Here(dir), os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "mcp":
		dir, err := place.Dir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "spectashell-keeper mcp:", err)
			os.Exit(1)
		}
		if err := pages.MCP(pages.Here(dir), build, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "spectashell-keeper mcp:", err)
			os.Exit(1)
		}
	case "serial":
		os.Exit(serial(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "telnet":
		os.Exit(telnetCommand(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "serial-ports":
		serialPorts(os.Stdout)
	case "pages-relay":
		if len(os.Args) != 3 {
			usage()
		}
		dir, err := place.Dir()
		if err == nil {
			err = pages.Relay(dir, os.Args[2], os.Stdin, os.Stdout)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "spectashell-keeper pages-relay:", err)
			os.Exit(1)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: spectashell-keeper start | attach <id> [--agent] | join <id> | list | version | builds | keep | page <command> | mcp | "+
		"serial [options] <device> | telnet <host> [port] | serial-ports")
	os.Exit(2)
}

// startTimeout is how long a new keeper has to say it is ready.
const startTimeout = 10 * time.Second

func start(in io.Reader) StartAnswer {
	var req StartRequest
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return StartAnswer{Error: "reading the request: " + err.Error()}
	}
	dir, err := place.Dir()
	if err != nil {
		return StartAnswer{Error: err.Error()}
	}
	self, err := os.Executable()
	if err != nil {
		return StartAnswer{Error: err.Error()}
	}
	id, err := keeper.NewID()
	if err != nil {
		return StartAnswer{Error: err.Error()}
	}
	extra := map[string]string{pages.KeeperVariable: id}
	// The keepers' folder, when it was put elsewhere: a page command in the shell looks for its keeper
	// there. The shell keeps nothing else of this process's environment (termhost.TerminalEnv).
	if dir := os.Getenv(place.Override); dir != "" {
		extra[place.Override] = dir
	}
	for k, v := range req.Env {
		extra[k] = v
	}
	// The agent forwarded to whichever device holds the shell, through the keeper's agent link: a
	// path that stays, pointed by each attach (keeper.AgentPath).
	if req.Agent {
		extra["SSH_AUTH_SOCK"] = keeper.AgentPath(keeper.SocketPath(dir, id))
	}
	launch := termhost.Launch{Argv: hostkeeper.KeeperProgram(req.Argv, self), Dir: req.Dir, Env: os.Environ(),
		TermProgram: req.TermProgram,
		Extra:       extra}
	if req.ShellIntegration {
		launch.IntegrationDir = filepath.Join(filepath.Dir(self), "shell-integration")
	}
	prog, err := termhost.Prepare(launch)
	if err != nil {
		return StartAnswer{Error: err.Error()}
	}
	conn, err := keeper.Start([]string{self, "keep"}, keeper.Config{Socket: keeper.SocketPath(dir, id), KeeperID: id,
		Path: prog.Path, Argv: prog.Argv, Dir: prog.Dir, Env: prog.Env, Columns: req.Columns, Rows: req.Rows,
		Meta: req.Meta, Build: build}, startTimeout)
	if err != nil {
		return StartAnswer{Error: err.Error()}
	}
	conn.Close() // the client attaches with attach; the program runs on
	return StartAnswer{KeeperID: id}
}

// attach joins in and out to keeper id's socket until either side ends. With agent, the agent
// forwarded to this channel (SSH_AUTH_SOCK, which sshd set) is the keeper's agent while it holds
// it: its agent link points there, and is taken back as it leaves unless another took it since.
func attach(id string, agent bool, in io.Reader, out io.Writer) error {
	if !keeper.ValidID(id) {
		return fmt.Errorf("%q is not a keeper id", id)
	}
	dir, err := place.Dir()
	if err != nil {
		return err
	}
	socket := keeper.SocketPath(dir, id)
	if forwarded := os.Getenv("SSH_AUTH_SOCK"); agent && forwarded != "" {
		// Pointed before the keeper is taken over, so the device it was taken from finds the link
		// already another's and leaves it.
		if err := keeper.PointAgent(socket, forwarded); err == nil {
			defer keeper.UnpointAgent(socket, forwarded)
		}
	}
	return relay(socket, id, in, out)
}

// join joins in and out to keeper id's socket for joining (keeper.JoinPath) until either side
// ends; keeper.ErrCannotJoin when the keeper runs but has none.
func join(id string, in io.Reader, out io.Writer) error {
	if !keeper.ValidID(id) {
		return fmt.Errorf("%q is not a keeper id", id)
	}
	dir, err := place.Dir()
	if err != nil {
		return err
	}
	socket := keeper.SocketPath(dir, id)
	if _, err := os.Stat(keeper.JoinPath(socket)); errors.Is(err, fs.ErrNotExist) {
		if _, err := os.Stat(socket); err == nil {
			return keeper.ErrCannotJoin
		}
	}
	return relay(keeper.JoinPath(socket), id, in, out)
}

// relay copies in to the socket at path and the socket to out until either side ends.
func relay(path, id string, in io.Reader, out io.Writer) error {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return fmt.Errorf("keeper %s is not running: %w", id, err)
	}
	defer conn.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(conn, in)
		// The client is gone (its channel closed): the keeper sees the connection end, as a local
		// client's would, and keeps the program.
		conn.Close()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(out, conn)
		done <- struct{}{}
	}()
	<-done
	return nil
}

func list() ListAnswer {
	dir, err := place.Dir()
	if err != nil {
		return ListAnswer{Boot: hostboot.ID(), Keepers: []hostkeeper.Listed{}, Error: err.Error()}
	}
	return hostkeeper.ListFolder(dir)
}

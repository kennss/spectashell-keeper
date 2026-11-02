// hostkeeper.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-11-02
//
// Purpose: what spectashell-keeper's commands on a host read and print
// (Docs/design/02-CONNECTIONS.md §3) — start a keeper for a terminal, list the keepers there with
// the host's boot, the builds in use — and doing the listing, which the program does for its host
// and the core for the Mac's own local shells (ListFolder).
//
// Notes:
// - The binary is at ~/.local/share/spectashell/<build>/spectashell-keeper (Programs; the core's installer
//   puts it there). Its folder's name is a build checked to be plain characters (IsPlain), so a
//   command line can name it without quoting.
// - The program's types are here so the keeper program (cmd/spectashell-keeper) and the core, which
//   runs its commands over SSH, agree on them in one place.
// - BuildsInUse is what the installer keeps: the builds of the keepers here, and of every program
//   of this user running from the installer's folder — an attach, a relay, another device's
//   connection's commands — since a keeper's build alone left those out.
// - Moved here from core/internal/hostkeeper on 2026-10-29, with the keeper program
//   (Docs/design/01-DESIGN.md §3.2).

package hostkeeper

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/kennss/spectashell-keeper/host/hostboot"
	"github.com/kennss/spectashell-keeper/host/procs"
	"github.com/kennss/spectashell-keeper/keeper"
	"github.com/kennss/spectashell-keeper/paneinfo"
)

// Itself, as the first word of a program a keeper runs, names the keeper program on that machine
// (KeeperProgram): a console — `spectashell-keeper serial …`, `spectashell-keeper telnet …`
// (Docs/design/02-CONNECTIONS.md §6) — runs from wherever that machine has it, which the app does
// not know.
const Itself = "spectashell-keeper"

// KeeperProgram is argv with Itself, as its first word, made path: the keeper program here.
func KeeperProgram(argv []string, path string) []string {
	if len(argv) == 0 || argv[0] != Itself {
		return argv
	}
	return append([]string{path}, argv[1:]...)
}

// StartRequest is what `spectashell-keeper start` reads: a terminal's program and how to hold it.
type StartRequest struct {
	// Argv is the program; empty: the user's login shell. A first word Itself is this keeper program.
	Argv []string `json:"argv,omitempty"`
	// Dir is its folder; empty: home.
	Dir string `json:"dir,omitempty"`
	// Env is added to the terminal's environment (LANG, TERM_PROGRAM_VERSION).
	Env map[string]string `json:"env,omitempty"`
	// TermProgram is TERM_PROGRAM, as the app gives it: SpectaShell's apps give "sdTerm", the name
	// their code keeps, from before the product was named.
	TermProgram string `json:"term_program,omitempty"`
	// ShellIntegration injects the scripts into a login shell.
	ShellIntegration bool `json:"shell_integration,omitempty"`
	// Agent gives the terminal the keeper's agent link as SSH_AUTH_SOCK (keeper.AgentPath), which
	// each client attaching with the agent forwarded points at its own (Attach; added 2026-10-29).
	Agent   bool `json:"agent,omitempty"`
	Columns int  `json:"columns,omitempty"`
	Rows    int  `json:"rows,omitempty"`
	// Meta is what the keeper remembers and describes itself with.
	Meta keeper.Meta `json:"meta"`
}

// StartAnswer is what start prints.
type StartAnswer struct {
	KeeperID string `json:"keeper_id,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ListAnswer is what `spectashell-keeper list` prints.
type ListAnswer struct {
	// Boot changes with every restart of the host.
	Boot    string   `json:"boot"`
	Keepers []Listed `json:"keepers"`
	// Error says why the keepers' folder cannot be used.
	Error string `json:"error,omitempty"`
	// Home is the login's home folder, for the app to write folders from ~ (added 2026-10-25: a
	// listing made with the listing key reads nothing else of the host).
	Home string `json:"home,omitempty"`
	// Ended is how the keepers that ended there within 30 days ended (keeper.Ends, added
	// 2026-10-26), for a device to say what became of a shell it held.
	Ended []keeper.End `json:"ended,omitempty"`
}

// Listed is one keeper as list prints it: its description, and what runs in its shell now.
type Listed struct {
	keeper.Description
	// Program is the program in front of the keeper's terminal now, as its command line ("btop");
	// the shell's name at its prompt; empty when it ended or could not be read, and from a keeper
	// program before 2026-10-23. Read when listed, not kept: a shell no client holds changes too.
	Program string `json:"program,omitempty"`
}

// ListFolder lists the keepers in dir as `spectashell-keeper list` prints them, with this machine's boot
// and what runs in each shell now, without connecting to any (connecting would take it over).
func ListFolder(dir string) ListAnswer {
	answer := ListAnswer{Boot: hostboot.ID(), Keepers: []Listed{}}
	if home, err := os.UserHomeDir(); err == nil {
		answer.Home = home
	}
	reader := paneinfo.NewReader()
	for _, d := range keeper.List(dir) {
		listed := Listed{Description: d}
		if !d.Exited {
			// What runs there now, so the user can tell two shells in one folder apart.
			if info, err := reader.Read(d.PID, d.Meta.Directory); err == nil {
				listed.Program = info.Program
			}
		}
		answer.Keepers = append(answer.Keepers, listed)
	}
	// After the list, which leaves an end for each keeper it found gone without one.
	answer.Ended = keeper.Ends(dir)
	return answer
}

var plain = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)

// IsPlain says s is only letters, digits and "._+-": a build or a word a command line and a path
// can carry without quoting.
func IsPlain(s string) bool { return plain.MatchString(s) }

// Programs is the folder the installer puts the keeper program in on a host whose home is home, a
// folder for each build: <Programs>/<build>/spectashell-keeper (install.Ensure).
func Programs(home string) string {
	return filepath.Join(home, ".local", "share", "spectashell")
}

// BuildOf is the build whose program in home's Programs path is; "" when it is not one of them.
func BuildOf(home, path string) string {
	rest, ok := strings.CutPrefix(path, Programs(home)+string(filepath.Separator))
	if !ok {
		return ""
	}
	build, _, ok := strings.Cut(rest, string(filepath.Separator))
	if !ok || !IsPlain(build) {
		return ""
	}
	return build
}

// BuildsAnswer is what `spectashell-keeper builds` prints.
type BuildsAnswer struct {
	// Builds are those in use here, each once, sorted.
	Builds []string `json:"builds"`
	// Error says why what is in use cannot be known.
	Error string `json:"error,omitempty"`
}

// BuildsInUse is the builds whose folders the installer keeps on this host: those of the keepers
// in dir, and those whose program any process of this user runs from Programs.
func BuildsInUse(dir string) BuildsAnswer {
	home, err := os.UserHomeDir()
	if err != nil {
		return BuildsAnswer{Builds: []string{}, Error: err.Error()}
	}
	pids := procs.Mine()
	if pids == nil {
		return BuildsAnswer{Builds: []string{}, Error: "this user's processes cannot be listed"}
	}
	builds := []string{}
	for _, d := range keeper.List(dir) {
		if d.Build != "" {
			builds = append(builds, d.Build)
		}
	}
	for _, pid := range pids {
		if b := BuildOf(home, procs.Executable(pid)); b != "" {
			builds = append(builds, b)
		}
	}
	slices.Sort(builds)
	return BuildsAnswer{Builds: slices.Compact(builds)}
}

// AgentFlag is attach's word for a relay whose session the agent is forwarded to: it points the
// keeper's agent link at it (SSH_AUTH_SOCK) while it holds the keeper.
const AgentFlag = "--agent"

// ExitCannotJoin is `spectashell-keeper join`'s exit status for a keeper that cannot be joined.
const ExitCannotJoin = 3

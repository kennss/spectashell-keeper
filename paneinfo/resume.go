// resume.go
// Author: Kennt Kim
// Created: 2026-10-22 · Last updated: 2026-10-30
//
// Purpose: the shell command that resumes the conversation of the coding agent running in front of
// a terminal — Claude Code or Codex — so that after a restart of the machine, which no shell
// survives, the app can type it at the reopened shell's prompt (Docs/design/02-CONNECTIONS.md §3,
// SiliconDesk's Docs/apps/sdterm.md §2.3).
//
// Notes:
// - Taken from SiliconDesk's host/internal/apps/terminal/restart.go on 2026-10-23: its resume logic
//   (resumeCommand, claudeSession, codexSession, withoutInterpreter, claudeOptions, codexOptions,
//   resumeArgs, shellJoin, shellQuote); the rest of that file is SiliconDesk's sdterm app. There
//   the variables of a program another app asked to run (sdLLM's RunCommand environment) went
//   before the command; sdTerm runs no such programs, so the command is the program's own.
// - The program in front's process group is looked at — its leader and what it started in the
//   group: `ollama launch claude` runs claude as its child. *Bug fixed (2026-10-23):* SiliconDesk
//   looked at the group whose id is the program's process id, which is empty when the group's
//   leader has exited and a member is in front; here it is the program's own group.
// - Claude Code keeps a record of each running interactive session in <config dir>/sessions/
//   <pid>.json (sessionId, cwd, kind; the config dir is CLAUDE_CONFIG_DIR in its environment, else
//   ~/.claude). Seen in Claude Code 2.1.304 on the Mac, 2026-10-22, and 2.1.306 on Linux. Here the
//   home is the process's own HOME when its environment has one, else the reader's.
// - The command is the program as it was started — `claude` as typed, or `ollama launch claude …
//   --` — with the options that set up the session kept (claudeOptions: the permission mode, the
//   model, the folders, settings and tools) and `--resume <id>`. Every other argument is left out: a
//   prompt given on the command line (`claude "fix the build"`) would be sent again, and an option
//   not known could not be told from a prompt.
// - Codex keeps no record by pid, but a running interactive session holds its thread's lock open,
//   <CODEX_HOME>/thread-writer-locks/<id>.lock, and its rollout file (Codex 0.163, 2026-10-23), so
//   the id is read from the process's open files. Its command is the program with the options of
//   codexOptions and `resume <id>` (a subcommand, after the options, which apply to it). `codex
//   exec` (non-interactive) is not resumed.
// - A program started through Node or Bun (`node …/bin/codex`, an npm install) is named by its
//   script.
// - *Bug fixed (2026-10-23):* an argument starting with '=' was left unquoted, and zsh — the Mac's
//   shell — takes `=word` for the path of the command word (`echo =ls` prints /bin/ls); it is
//   quoted now.

package paneinfo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The coding agents whose conversations are resumed.
const (
	agentClaude = "claude"
	agentCodex  = "codex"
)

// claudeSessionID matches Claude Code's session ids (UUIDs).
var claudeSessionID = regexp.MustCompile(`^[0-9A-Fa-f]{8}(-[0-9A-Fa-f]{4}){3}-[0-9A-Fa-f]{12}$`)

// codexLock matches a Codex thread's lock file: <CODEX_HOME>/thread-writer-locks/<id>.lock.
var codexLock = regexp.MustCompile(`/thread-writer-locks/([0-9A-Fa-f]{8}(?:-[0-9A-Fa-f]{4}){3}-[0-9A-Fa-f]{12})\.lock$`)

// resumeCommand is the command line that resumes the coding agent running in front (the process
// front in s, and the rest of its process group), or "" when none is known.
func resumeCommand(sys system, s *snapshot, front int) string {
	for _, pid := range s.group(s.procs[front].pgrp) {
		agent, id := agentClaude, ""
		if isClaude(sys.commandLine(pid)) {
			id = claudeSession(sys, pid)
		}
		if id == "" {
			agent, id = agentCodex, codexSession(sys, pid)
		}
		if id == "" {
			continue
		}
		args := withoutInterpreter(sys.commandLine(front))
		if len(args) == 0 || (agent == agentCodex && slices.Contains(args, "exec")) {
			return ""
		}
		args = resumeArgs(agent, args)
		if agent == agentCodex {
			args = append(args, "resume", id)
		} else {
			args = append(args, "--resume", id)
		}
		return shellJoin(args)
	}
	return ""
}

// isClaude is whether a command line runs Claude Code: its program (its script, run through Node or
// Bun) is named claude, or lies in Claude Code's npm package. Only such a process's record is read:
// records outlive a restart, and a new process given an old record's pid — `python`, `vim` — would
// otherwise be offered that conversation's resume command.
func isClaude(args []string) bool {
	args = withoutInterpreter(args)
	if len(args) == 0 {
		return false
	}
	base := strings.TrimPrefix(filepath.Base(args[0]), "-")
	return base == "claude" || strings.HasPrefix(base, "claude.") || strings.Contains(args[0], "/@anthropic-ai/claude-code/")
}

// codexSession is the id of the Codex thread process pid holds open, or "".
func codexSession(sys system, pid int) string {
	for _, path := range sys.openFiles(pid) {
		if m := codexLock.FindStringSubmatch(path); m != nil {
			return m[1]
		}
	}
	return ""
}

// withoutInterpreter names a program run by Node or Bun by its script: `node …/bin/codex a` is
// `…/bin/codex a`.
func withoutInterpreter(args []string) []string {
	if len(args) > 1 {
		if base := filepath.Base(args[0]); base == "node" || base == "bun" {
			return args[1:]
		}
	}
	return args
}

// claudeSession is the id of the Claude Code session process pid runs, from its record in its
// config dir, or "".
func claudeSession(sys system, pid int) string {
	env := sys.environ(pid)
	dir := env["CLAUDE_CONFIG_DIR"]
	if dir == "" {
		home := env["HOME"]
		if home == "" {
			var err error
			if home, err = os.UserHomeDir(); err != nil {
				return ""
			}
		}
		dir = filepath.Join(home, ".claude")
	}
	b, err := os.ReadFile(filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return ""
	}
	var record struct {
		PID       int    `json:"pid"`
		SessionID string `json:"sessionId"`
		Kind      string `json:"kind"` // "interactive"; a run with --print has nothing to resume
	}
	if json.Unmarshal(b, &record) != nil || (record.PID != 0 && record.PID != pid) ||
		(record.Kind != "" && record.Kind != "interactive") || !claudeSessionID.MatchString(record.SessionID) {
		return ""
	}
	return record.SessionID
}

// claudeOptions are Claude Code's options that set up a session, kept in the command that resumes
// it, with how many values each takes (-1: every value up to the next option, as Claude Code's own
// parser takes them). From `claude --help`, 2.1.306, 2026-10-22.
var claudeOptions = map[string]int{
	"--permission-mode": 1, "--model": 1, "--fallback-model": 1, "--effort": 1, "--agent": 1, "--agents": 1,
	"--settings": 1, "--append-system-prompt": 1, "--system-prompt": 1, "--plugin-dir": 1,
	"--add-dir": -1, "--mcp-config": -1, "--allowedTools": -1, "--allowed-tools": -1,
	"--disallowedTools": -1, "--disallowed-tools": -1,
	"--dangerously-skip-permissions": 0, "--allow-dangerously-skip-permissions": 0, "--ide": 0, "--chrome": 0,
	"--no-chrome": 0,
}

// codexOptions are Codex's, kept the same way (`codex --help`, 0.163, 2026-10-23).
var codexOptions = map[string]int{
	"--sandbox": 1, "-s": 1, "--ask-for-approval": 1, "-a": 1, "--model": 1, "-m": 1, "--profile": 1,
	"-p": 1, "--config": 1, "-c": 1, "--add-dir": 1, "--enable": 1, "--disable": 1, "--local-provider": 1,
	"--full-auto": 0, "--dangerously-bypass-approvals-and-sandbox": 0, "--search": 0, "--oss": 0,
}

// resumeArgs is the program as it was started — the agent as typed, or `ollama launch … --` — with
// the options of the agent's table it was given; every other argument (a prompt, an earlier resume
// or continue, a subcommand, an option not known) is left out.
func resumeArgs(agent string, args []string) []string {
	options := claudeOptions
	if agent == agentCodex {
		options = codexOptions
	}
	start := 1
	if filepath.Base(args[0]) == "ollama" {
		i := slices.Index(args, "--")
		if i < 0 {
			return append(slices.Clone(args), "--")
		}
		start = i + 1
	}
	out := slices.Clone(args[:start])
	for i := start; i < len(args); i++ {
		name, _, hasInline := strings.Cut(args[i], "=")
		n, known := options[name]
		if !strings.HasPrefix(args[i], "-") || !known {
			continue
		}
		out = append(out, args[i])
		if hasInline || n == 0 {
			continue
		}
		for taken := 0; i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && (n < 0 || taken < n); taken++ {
			i++
			out = append(out, args[i])
		}
	}
	return out
}

// shellJoin quotes each argument for a POSIX shell.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// plainWord is an argument that needs no quotes. '=' may not open one: zsh, the Mac's shell, takes
// `=word` for the path of the command word (its EQUALS option, on by default).
var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+:,./-][A-Za-z0-9_@%+=:,./-]*$`)

// shellQuote quotes s for a POSIX shell (and zsh) when it holds anything but plain characters.
func shellQuote(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

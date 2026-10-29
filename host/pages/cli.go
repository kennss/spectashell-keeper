// cli.go
// Author: Kennt Kim
// Created: 2026-10-25 · Last updated: 2026-10-29
//
// Purpose: `spectashell-keeper page <command>`, an agent's command to the page beside its shell
// (Docs/DESIGN.md §6.1.2), for any agent that runs commands: its arguments read into a request,
// asked (ask.go), and the answer printed — or written, for a screenshot.
//
// Notes:
// - A target is a ref from a snapshot (e12), else a CSS selector; --text names an element by what
//   it says. PageAgent.js (client/sdTerm/Shared) takes them as {ref}, {selector} or {text}.
// - --port picks the page by its port on the shell's machine; without it, the page last used.
// - A screenshot goes to --out, else to a new file in the temporary folder, and its path is
//   printed: an agent reads the picture from the file.
// - Moved here from core/internal/pages on 2026-10-29, with the keeper program
//   (Docs/DESIGN.md §3.2).

//go:build unix

package pages

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Usage is the page command's help.
const Usage = `usage: spectashell-keeper page <command> [arguments] [--port N]

Drives the page beside this shell in SpectaShell — the one its user sees — for a coding agent.
A target is a ref from a snapshot (e12) or a CSS selector; --text "Sign in" names an element by
what it says. --port N picks the page of port N; without it, the page last used.

  pages                                  the pages beside this shell
  open <url>                             open http://localhost:<port>/<path> beside this shell
  goto <url|path>                        go to another path of the page (back, forward, reload)
  close                                  close the page
  snapshot [-i] [--selector css]         the page as a tree of elements; -i: only those to act on
  click <target> [--double]
  hover <target> | focus <target>
  fill <target> <text>                   replace what a field holds
  type <text> [--into <target>]          type at the caret
  press <key> [--on <target>]            Enter, Tab, Escape, ArrowDown, Meta+a…
  select <target> <value>...
  check <target> | uncheck <target>
  scroll [<target>] [--dx N] [--dy N]
  wait [--text t] [--selector css] [--url part] [--gone] [--timeout seconds]
  get <text|html|value|attr|count|box|title|url> [<target>] [--name attribute]
  eval <javascript>                      run it in the page; - reads it from standard input
  screenshot [<target>] [--out file.png]
  console [--clear]                      what the page logged, and its errors
`

var refPattern = regexp.MustCompile(`^e[0-9]+$`)

// usageError is a command given wrongly: its message is printed with the usage.
type usageError struct{ message string }

func (e usageError) Error() string { return e.message }

// Command runs the page command with args (after "page"), printing to out and errs; the exit code.
func Command(a Asker, args []string, in io.Reader, out, errs io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(out, Usage)
		return 0
	}
	command, port, request, err := parse(args, in)
	if err != nil {
		fmt.Fprintf(errs, "spectashell-keeper page: %v\n\n%s", err, Usage)
		return 2
	}
	// Where a screenshot goes is the command's own business, not the app's.
	saveTo, _ := request["out"].(string)
	delete(request, "out")
	reply, err := a.Ask(command, port, request)
	if err != nil {
		fmt.Fprintln(errs, err)
		return 1
	}
	if command == "screenshot" {
		path, err := saveScreenshot(reply, saveTo)
		if err != nil {
			fmt.Fprintln(errs, err)
			return 1
		}
		fmt.Fprintf(out, "%s\nSaved to %s\n", reply.Text, path)
		return 0
	}
	if reply.Text != "" {
		fmt.Fprintln(out, strings.TrimRight(reply.Text, "\n"))
	}
	return 0
}

// options is a command line's flags and the words between them.
type options struct {
	words  []string
	values map[string]string
	flags  map[string]bool
}

// Flags that take a value, and flags that do not.
var (
	valueFlags = map[string]bool{"port": true, "selector": true, "text": true, "into": true, "on": true,
		"out": true, "dx": true, "dy": true, "url": true, "timeout": true, "name": true, "max": true}
	plainFlags = map[string]bool{"i": true, "interactive": true, "double": true, "gone": true, "clear": true}
)

func readOptions(args []string) (options, error) {
	o := options{values: map[string]string{}, flags: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			o.words = append(o.words, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" || len(arg) < 2 {
			o.words = append(o.words, arg)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		switch {
		case valueFlags[name]:
			if !hasValue {
				if i+1 >= len(args) {
					return o, usageError{"--" + name + " needs a value"}
				}
				i++
				value = args[i]
			}
			o.values[name] = value
		case plainFlags[name]:
			o.flags[name] = true
		default:
			// A word that starts with "-" and is no flag (a negative number, a key like "-").
			o.words = append(o.words, arg)
		}
	}
	return o, nil
}

// target reads a target from word, or from the --text and --selector flags.
func target(word string, o options) (map[string]any, bool) {
	if t, ok := o.values["text"]; ok {
		return map[string]any{"text": t}, true
	}
	if s, ok := o.values["selector"]; ok && word == "" {
		return map[string]any{"selector": s}, true
	}
	if word == "" {
		return nil, false
	}
	if refPattern.MatchString(word) {
		return map[string]any{"ref": word}, true
	}
	return map[string]any{"selector": word}, true
}

// targetOf reads a target from a flag's value (--into, --on).
func targetOf(value string) map[string]any {
	if refPattern.MatchString(value) {
		return map[string]any{"ref": value}
	}
	return map[string]any{"selector": value}
}

// parse reads a command line into the command, the page's port and the request's arguments.
func parse(args []string, in io.Reader) (string, int, map[string]any, error) {
	command := args[0]
	o, err := readOptions(args[1:])
	if err != nil {
		return "", 0, nil, err
	}
	port := 0
	if p, ok := o.values["port"]; ok {
		if port, err = strconv.Atoi(p); err != nil || port <= 0 || port > 65535 {
			return "", 0, nil, usageError{"--port takes a port number"}
		}
	}
	word := func(i int) string {
		if i < len(o.words) {
			return o.words[i]
		}
		return ""
	}
	request := map[string]any{}
	needTarget := func() error {
		t, ok := target(word(0), o)
		if !ok {
			return usageError{command + " needs a target: a ref (e12), a CSS selector or --text"}
		}
		request["target"] = t
		return nil
	}
	switch command {
	case "pages", "back", "forward", "reload", "close":
	case "open", "goto":
		if word(0) == "" {
			return "", 0, nil, usageError{command + " needs an address"}
		}
		request["url"] = word(0)
	case "snapshot":
		if o.flags["i"] || o.flags["interactive"] {
			request["interactive"] = true
		}
		if s, ok := o.values["selector"]; ok {
			request["selector"] = s
		}
		if m, ok := o.values["max"]; ok {
			n, err := strconv.Atoi(m)
			if err != nil || n <= 0 {
				return "", 0, nil, usageError{"--max takes a number of characters"}
			}
			request["maxChars"] = n
		}
	case "click":
		if err := needTarget(); err != nil {
			return "", 0, nil, err
		}
		if o.flags["double"] {
			command = "dblclick"
		}
	case "hover", "focus", "check", "uncheck":
		if err := needTarget(); err != nil {
			return "", 0, nil, err
		}
	case "fill":
		if _, ok := o.values["text"]; ok {
			// fill --text "Email" "sam@example.com": the value is the only word.
			request["target"], _ = target("", o)
			if len(o.words) != 1 {
				return "", 0, nil, usageError{"fill needs a target and the text to put in it"}
			}
			request["text"] = word(0)
			break
		}
		if len(o.words) != 2 {
			return "", 0, nil, usageError{"fill needs a target and the text to put in it"}
		}
		request["target"], _ = target(word(0), o)
		request["text"] = word(1)
	case "type":
		if len(o.words) != 1 {
			return "", 0, nil, usageError{"type needs the text to type, as one argument"}
		}
		request["text"] = word(0)
		if into, ok := o.values["into"]; ok {
			request["target"] = targetOf(into)
		}
	case "press":
		if len(o.words) != 1 {
			return "", 0, nil, usageError{"press needs one key (Enter, Tab, Meta+a…)"}
		}
		request["key"] = word(0)
		if on, ok := o.values["on"]; ok {
			request["target"] = targetOf(on)
		}
	case "select":
		if len(o.words) < 2 {
			return "", 0, nil, usageError{"select needs a target and the values to choose"}
		}
		request["target"], _ = target(word(0), o)
		request["values"] = o.words[1:]
	case "scroll":
		if t, ok := target(word(0), o); ok {
			request["target"] = t
		}
		for _, axis := range []string{"dx", "dy"} {
			if v, ok := o.values[axis]; ok {
				n, err := strconv.Atoi(v)
				if err != nil {
					return "", 0, nil, usageError{"--" + axis + " takes a number of pixels"}
				}
				request[axis] = n
			}
		}
	case "wait":
		for _, key := range []string{"text", "selector", "url"} {
			if v, ok := o.values[key]; ok {
				request[key] = v
			}
		}
		if len(request) == 0 {
			return "", 0, nil, usageError{"wait needs --text, --selector or --url"}
		}
		if o.flags["gone"] {
			request["gone"] = true
		}
		if t, ok := o.values["timeout"]; ok {
			seconds, err := strconv.ParseFloat(t, 64)
			if err != nil || seconds <= 0 || seconds > 30 {
				return "", 0, nil, usageError{"--timeout takes seconds, at most 30"}
			}
			request["timeoutMs"] = int(seconds * 1000)
		}
	case "get":
		what := word(0)
		switch what {
		case "text", "html", "value", "attr", "count", "box", "title", "url":
		default:
			return "", 0, nil, usageError{"get takes text, html, value, attr, count, box, title or url"}
		}
		request["what"] = what
		if what == "count" {
			selector := o.values["selector"]
			if selector == "" {
				selector = word(1)
			}
			if selector == "" {
				return "", 0, nil, usageError{"get count needs a CSS selector"}
			}
			request["selector"] = selector
			break
		}
		if t, ok := target(word(1), o); ok {
			request["target"] = t
		}
		if name, ok := o.values["name"]; ok {
			request["name"] = name
		} else if what == "attr" {
			return "", 0, nil, usageError{"get attr needs --name"}
		}
	case "eval":
		script := strings.Join(o.words, " ")
		if script == "-" {
			b, err := io.ReadAll(in)
			if err != nil {
				return "", 0, nil, err
			}
			script = string(b)
		}
		if strings.TrimSpace(script) == "" {
			return "", 0, nil, usageError{"eval needs JavaScript to run"}
		}
		request["script"] = script
	case "screenshot":
		if t, ok := target(word(0), o); ok {
			request["target"] = t
		}
		if path, ok := o.values["out"]; ok {
			request["out"] = path
		}
	case "console":
		if o.flags["clear"] {
			request["clear"] = true
		}
	default:
		return "", 0, nil, usageError{fmt.Sprintf("%q is not a page command", command)}
	}
	return command, port, request, nil
}

// saveScreenshot writes reply's picture to out, else to a new file in the temporary folder, and
// returns the path.
func saveScreenshot(reply Reply, path string) (string, error) {
	if len(reply.Image) == 0 {
		return "", fmt.Errorf("SpectaShell sent no picture")
	}
	if path == "" {
		port := 0
		if reply.Page != nil {
			port = reply.Page.Port
		}
		path = filepath.Join(os.TempDir(), fmt.Sprintf("spectashell-page-%d-%s.png", port,
			time.Now().Format("20060102-150405.000")))
	}
	if err := os.WriteFile(path, reply.Image, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

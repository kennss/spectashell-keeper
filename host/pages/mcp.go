// mcp.go
// Author: Kennt Kim
// Created: 2026-10-25 · Last updated: 2026-10-29
//
// Purpose: `sdterm-keeper mcp`, the page commands as an MCP server (Model Context Protocol) on
// standard input and output, for agents that take tools — Claude Code
// (`claude mcp add spectashell -- <keeper program> mcp`), Codex — rather than run commands
// (Docs/DESIGN.md §6.1.2). Each tool is a page command (cli.go's), asked of the pages beside the
// shell the agent runs in (ask.go).
//
// Notes:
// - JSON-RPC 2.0, one message per line: initialize, ping, tools/list and tools/call are answered;
//   notifications are not; any other method is "not found". A batch (an array, 2025-03-26) is
//   answered as an array.
// - The protocol version is the client's when it is one of protocolVersions, else the newest.
// - A target is ref, selector or name (an element by what it says: CLI's --text, PageAgent.js's
//   {text}); "text" is what fill and type put in, so it cannot also name the element.
// - A tool's failure is its result with isError, as the protocol asks, so the agent reads why; a
//   malformed call is a JSON-RPC error.
// - The shell is found again at every call: another device may have taken it meanwhile.
// - Moved here from core/internal/pages on 2026-10-29, with the keeper program
//   (Docs/DESIGN.md §3.2).

//go:build unix

package pages

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"slices"
)

// protocolVersions are the MCP versions answered, oldest first.
var protocolVersions = []string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"}

// mcpInstructions tells the agent what the server is for.
const mcpInstructions = `SpectaShell shows web pages beside the shell this agent runs in, and its user watches them.
Open the server you started with page_open (http://localhost:PORT/path) — a port of this machine — then
page_snapshot to read the page: each element you can act on has a ref (e12) for page_click, page_fill,
page_press and the rest. Refs last until the page navigates: take a new snapshot after it changes.
page_console shows what the page logged and what threw; page_screenshot shows how it looks.`

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP serves the page tools on in and out until in ends. build is the keeper program's.
func MCP(a Asker, build string, in io.Reader, out io.Writer) error {
	dec := json.NewDecoder(in)
	enc := json.NewEncoder(out)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) > 0 && raw[0] == '[' {
			var batch []rpcMessage
			if err := json.Unmarshal(raw, &batch); err != nil {
				_ = enc.Encode(rpcReply{JSONRPC: "2.0", ID: json.RawMessage("null"),
					Error: &rpcError{Code: -32700, Message: err.Error()}})
				continue
			}
			var replies []rpcReply
			for _, m := range batch {
				if r, ok := answer(a, build, m); ok {
					replies = append(replies, r)
				}
			}
			if len(replies) > 0 {
				_ = enc.Encode(replies)
			}
			continue
		}
		var m rpcMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			_ = enc.Encode(rpcReply{JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &rpcError{Code: -32700, Message: err.Error()}})
			continue
		}
		if r, ok := answer(a, build, m); ok {
			_ = enc.Encode(r)
		}
	}
}

// answer is m's reply; false for a notification, which has none.
func answer(a Asker, build string, m rpcMessage) (rpcReply, bool) {
	if len(m.ID) == 0 {
		return rpcReply{}, false
	}
	reply := rpcReply{JSONRPC: "2.0", ID: m.ID}
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(m.Params, &p)
		version := protocolVersions[len(protocolVersions)-1]
		if slices.Contains(protocolVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		reply.Result = map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "spectashell", "title": "SpectaShell pages", "version": build},
			"instructions":    mcpInstructions,
		}
	case "ping":
		reply.Result = map[string]any{}
	case "tools/list":
		reply.Result = map[string]any{"tools": tools()}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			reply.Error = &rpcError{Code: -32602, Message: err.Error()}
			break
		}
		command, port, args, err := toolRequest(p.Name, p.Arguments)
		if err != nil {
			if _, unknown := err.(unknownTool); unknown {
				reply.Error = &rpcError{Code: -32602, Message: err.Error()}
				break
			}
			reply.Result = toolError(err.Error())
			break
		}
		r, err := a.Ask(command, port, args)
		if err != nil {
			reply.Result = toolError(err.Error())
			break
		}
		content := []map[string]any{}
		if r.Text != "" {
			content = append(content, map[string]any{"type": "text", "text": r.Text})
		}
		if len(r.Image) > 0 {
			content = append(content, map[string]any{"type": "image", "mimeType": "image/png",
				"data": base64.StdEncoding.EncodeToString(r.Image)})
		}
		if len(content) == 0 {
			content = append(content, map[string]any{"type": "text", "text": "Done."})
		}
		reply.Result = map[string]any{"content": content}
	default:
		reply.Error = &rpcError{Code: -32601, Message: fmt.Sprintf("%q is not a method of this server", m.Method)}
	}
	return reply, true
}

func toolError(message string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": message}}, "isError": true}
}

type unknownTool struct{ name string }

func (u unknownTool) Error() string { return fmt.Sprintf("%q is not a tool of this server", u.name) }

// Schemas shared by the tools.
var (
	portProperty = map[string]any{"type": "integer",
		"description": "The page's port on this machine; leave it out for the page last used"}
	targetProperties = map[string]any{
		"ref":      map[string]any{"type": "string", "description": "A ref from page_snapshot (e12)"},
		"selector": map[string]any{"type": "string", "description": "A CSS selector matching one element"},
		"name":     map[string]any{"type": "string", "description": "The visible name or text of the element"},
	}
)

// schema is an object schema of properties, with the target's and the port's when asked.
func schema(properties map[string]any, required []string, withTarget bool) map[string]any {
	all := map[string]any{"port": portProperty}
	if withTarget {
		for k, v := range targetProperties {
			all[k] = v
		}
	}
	for k, v := range properties {
		all[k] = v
	}
	s := map[string]any{"type": "object", "properties": all}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func tool(name, title, description string, input map[string]any, readOnly bool) map[string]any {
	return map[string]any{"name": name, "title": title, "description": description, "inputSchema": input,
		"annotations": map[string]any{"readOnlyHint": readOnly, "openWorldHint": false}}
}

func str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// tools is the tools/list answer.
func tools() []map[string]any {
	return []map[string]any{
		tool("page_open", "Open a page", "Opens a page of this machine (http://localhost:PORT/path) beside the shell, "+
			"in SpectaShell, where its user sees it; a page of that port already open goes to the path.",
			schema(map[string]any{"url": str("http://localhost:PORT/path")}, []string{"url"}, false), false),
		tool("page_list", "List pages", "The pages beside the shell: port, path, title.", schema(nil, nil, false), true),
		tool("page_navigate", "Navigate", "Goes to another path of the page, or back, forward or reload.",
			schema(map[string]any{"to": str("A path or http://localhost URL of the page's port, or back, forward, reload")},
				[]string{"to"}, false), false),
		tool("page_close", "Close the page", "Closes the page beside the shell.", schema(nil, nil, false), false),
		tool("page_snapshot", "Read the page", "The page as an indented tree of roles and names; each element "+
			"that can be acted on has a ref (e12). Take a new one after the page changes.",
			schema(map[string]any{
				"interactive": map[string]any{"type": "boolean", "description": "Only the elements that can be acted on"},
				"selector":    str("Only the part under this CSS selector"),
				"max_chars":   map[string]any{"type": "integer", "description": "Cut the snapshot at this length (40000)"},
			}, nil, false), true),
		tool("page_click", "Click", "Clicks an element: a ref, a selector or its visible name.",
			schema(map[string]any{"double": map[string]any{"type": "boolean", "description": "A double click"}},
				nil, true), false),
		tool("page_hover", "Hover", "Moves the pointer over an element.", schema(nil, nil, true), false),
		tool("page_fill", "Fill a field", "Replaces what a text field holds with text.",
			schema(map[string]any{"text": str("What the field is to hold")}, []string{"text"}, true), false),
		tool("page_type", "Type", "Types text at the caret, into the element given or the one focused.",
			schema(map[string]any{"text": str("What to type")}, []string{"text"}, true), false),
		tool("page_press", "Press a key", "Presses a key on the element given or the one focused: Enter, Tab, "+
			"Escape, ArrowDown, Backspace, Meta+a, Shift+Tab…",
			schema(map[string]any{"key": str("The key, with any modifiers: Enter, Control+Enter")}, []string{"key"}, true), false),
		tool("page_select", "Choose options", "Chooses options of a select by value or label.",
			schema(map[string]any{"values": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
				[]string{"values"}, true), false),
		tool("page_check", "Check or uncheck", "Checks or unchecks a checkbox, radio button or switch.",
			schema(map[string]any{"checked": map[string]any{"type": "boolean"}}, []string{"checked"}, true), false),
		tool("page_scroll", "Scroll", "Scrolls an element into view, or the page by dx and dy pixels.",
			schema(map[string]any{"dx": map[string]any{"type": "integer"}, "dy": map[string]any{"type": "integer"}},
				nil, true), false),
		tool("page_wait", "Wait", "Waits until text or an element appears (or, with gone, disappears), or the "+
			"address holds url.",
			schema(map[string]any{
				"text": str("Text to wait for"), "selector": str("A CSS selector to wait for"),
				"url":        str("A part of the address to wait for"),
				"gone":       map[string]any{"type": "boolean", "description": "Wait for it to disappear"},
				"timeout_ms": map[string]any{"type": "integer", "description": "At most this long (5000; at most 30000)"},
			}, nil, false), true),
		tool("page_get", "Get", "Reads text, html, value, an attribute, a count, a box, the title or the url.",
			schema(map[string]any{
				"what": map[string]any{"type": "string",
					"enum": []string{"text", "html", "value", "attr", "count", "box", "title", "url"}},
				"attribute": str("The attribute's name, for attr"),
			}, []string{"what"}, true), true),
		tool("page_eval", "Run JavaScript", "Runs JavaScript in the page and returns its value as JSON: an "+
			"expression, or statements ending in return.",
			schema(map[string]any{"script": str("The JavaScript")}, []string{"script"}, false), false),
		tool("page_screenshot", "Screenshot", "A picture of the page as it shows now, or of one element.",
			schema(nil, nil, true), true),
		tool("page_console", "Console", "What the page logged and what threw, the last 500 lines.",
			schema(map[string]any{"clear": map[string]any{"type": "boolean", "description": "Clear it after reading"}},
				nil, false), true),
	}
}

// toolRequest is the page command for a call of tool name with args.
func toolRequest(name string, args map[string]any) (string, int, map[string]any, error) {
	port := 0
	if p, ok := args["port"].(float64); ok {
		port = int(p)
	}
	request := map[string]any{}
	copyKey := func(from, to string) {
		if v, ok := args[from]; ok {
			request[to] = v
		}
	}
	var target map[string]any
	for _, key := range []string{"ref", "selector", "name"} {
		if v, ok := args[key].(string); ok && v != "" {
			to := key
			if key == "name" {
				to = "text"
			}
			target = map[string]any{to: v}
			break
		}
	}
	needTarget := func() error {
		if target == nil {
			return fmt.Errorf("%s needs a target: ref, selector or name", name)
		}
		request["target"] = target
		return nil
	}
	optionalTarget := func() {
		if target != nil {
			request["target"] = target
		}
	}
	var command string
	switch name {
	case "page_open":
		command = "open"
		copyKey("url", "url")
	case "page_list":
		command = "pages"
	case "page_navigate":
		to, _ := args["to"].(string)
		switch to {
		case "back", "forward", "reload":
			command = to
		case "":
			return "", 0, nil, fmt.Errorf("page_navigate needs to")
		default:
			command = "goto"
			request["url"] = to
		}
	case "page_close":
		command = "close"
	case "page_snapshot":
		command = "snapshot"
		copyKey("interactive", "interactive")
		copyKey("selector", "selector")
		copyKey("max_chars", "maxChars")
		return command, port, request, nil
	case "page_click":
		command = "click"
		if d, _ := args["double"].(bool); d {
			command = "dblclick"
		}
		if err := needTarget(); err != nil {
			return "", 0, nil, err
		}
	case "page_hover":
		command = "hover"
		if err := needTarget(); err != nil {
			return "", 0, nil, err
		}
	case "page_fill":
		command = "fill"
		if err := needTarget(); err != nil {
			return "", 0, nil, err
		}
		copyKey("text", "text")
	case "page_type":
		command = "type"
		optionalTarget()
		copyKey("text", "text")
	case "page_press":
		command = "press"
		optionalTarget()
		copyKey("key", "key")
	case "page_select":
		command = "select"
		if err := needTarget(); err != nil {
			return "", 0, nil, err
		}
		copyKey("values", "values")
	case "page_check":
		command = "uncheck"
		if c, _ := args["checked"].(bool); c {
			command = "check"
		}
		if err := needTarget(); err != nil {
			return "", 0, nil, err
		}
	case "page_scroll":
		command = "scroll"
		optionalTarget()
		copyKey("dx", "dx")
		copyKey("dy", "dy")
	case "page_wait":
		command = "wait"
		copyKey("text", "text")
		copyKey("selector", "selector")
		copyKey("url", "url")
		copyKey("gone", "gone")
		if ms, ok := args["timeout_ms"].(float64); ok {
			request["timeoutMs"] = min(int(ms), 30000)
		}
		return command, port, request, nil
	case "page_get":
		command = "get"
		copyKey("what", "what")
		copyKey("attribute", "name")
		if request["what"] == "count" {
			copyKey("selector", "selector")
			return command, port, request, nil
		}
		optionalTarget()
	case "page_eval":
		command = "eval"
		copyKey("script", "script")
	case "page_screenshot":
		command = "screenshot"
		optionalTarget()
	case "page_console":
		command = "console"
		copyKey("clear", "clear")
	default:
		return "", 0, nil, unknownTool{name}
	}
	return command, port, request, nil
}

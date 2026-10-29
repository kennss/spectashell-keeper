// telnet.go
// Author: Kennt Kim
// Created: 2026-10-27 · Last updated: 2026-10-29
//
// Purpose: the telnet protocol (RFC 854 and its options) between a terminal and a telnet server:
// what the server sends split into the program's output and the protocol's own commands, the
// commands answered, and what the terminal types made safe to send. A Codec knows no connection;
// Link (link.go) puts one on a TCP connection, and spectashell-keeper's telnet bridge on its own.
//
// Notes:
//   - Options agreed: ECHO and SUPPRESS-GO-AHEAD the server's (character at a time, the server
//     echoing, as every console server and router expects); TERMINAL-TYPE ours, answered with
//     XTERM-256COLOR (RFC 1091); NAWS ours, the window's size, sent again at each resize (RFC 1073);
//     TRANSMIT-BINARY either way, when asked (RFC 856). Every other option is refused, and a
//     request is answered only when it changes the option's state, so two sides never loop
//     (RFC 854's rule, RFC 1143's reason).
//   - Opened, the codec asks first for what a terminal needs — DO SUPPRESS-GO-AHEAD, WILL
//     TERMINAL-TYPE, WILL NAWS — as PuTTY and the BSD client do: some routers wait for the client.
//   - Input: IAC is doubled; a carriage return is sent as CR LF, the network's end of line (the
//     Return key sends CR), unless the server agreed to take binary, when bytes go as typed.
//   - Output: a doubled IAC is one 0xFF byte; CR NUL is a bare CR; the server's other commands
//     (GA, NOP, AYT…) are dropped. A command split across reads is kept until the rest comes.

package telnet

import (
	"bytes"
	"encoding/binary"
)

// The protocol's bytes (RFC 854).
const (
	cmdSE   = 240
	cmdSB   = 250
	cmdWILL = 251
	cmdWONT = 252
	cmdDO   = 253
	cmdDONT = 254
	cmdIAC  = 255
)

// The options a terminal agrees to.
const (
	optBinary   = 0
	optEcho     = 1
	optSGA      = 3
	optTermType = 24
	optNAWS     = 31
)

// TerminalType is what the codec says the terminal is (TERMINAL-TYPE IS).
const TerminalType = "XTERM-256COLOR"

const (
	ttIS   = 0
	ttSEND = 1
)

// Codec is one side of a telnet conversation: the terminal's. It is not safe for concurrent
// use; Link guards it.
type Codec struct {
	// The options in force: ours (we WILL) and the server's (it WILL, we said DO).
	us, them map[byte]bool
	// What was asked and not yet answered, so the answer is not answered again.
	askedUs, askedThem map[byte]bool
	// A command not finished at the end of the last read.
	pending    []byte
	cols, rows int
}

// NewCodec returns a codec for a window of cols × rows, and the requests a terminal opens with.
func NewCodec(cols, rows int) (*Codec, []byte) {
	c := &Codec{us: map[byte]bool{}, them: map[byte]bool{}, askedUs: map[byte]bool{},
		askedThem: map[byte]bool{}, cols: cols, rows: rows}
	var out bytes.Buffer
	c.ask(&out, cmdDO, optSGA)
	c.ask(&out, cmdWILL, optTermType)
	c.ask(&out, cmdWILL, optNAWS)
	return c, out.Bytes()
}

func (c *Codec) ask(out *bytes.Buffer, verb, option byte) {
	switch verb {
	case cmdDO:
		c.askedThem[option] = true
	case cmdWILL:
		c.askedUs[option] = true
	}
	out.Write([]byte{cmdIAC, verb, option})
}

// Receive takes bytes from the server: it returns the program's output in them and what to send
// back.
func (c *Codec) Receive(in []byte) (output, reply []byte) {
	buf := append(c.pending, in...)
	c.pending = nil
	var out, rep bytes.Buffer
	for i := 0; i < len(buf); {
		b := buf[i]
		if b == '\r' && !c.them[optBinary] {
			// CR NUL is a bare CR; CR LF and anything else pass as they are.
			if i+1 == len(buf) {
				c.pending = append([]byte(nil), buf[i:]...)
				break
			}
			out.WriteByte('\r')
			if buf[i+1] == 0 {
				i += 2
			} else {
				i++
			}
			continue
		}
		if b != cmdIAC {
			out.WriteByte(b)
			i++
			continue
		}
		n, done := c.command(buf[i:], &out, &rep)
		if !done {
			c.pending = append([]byte(nil), buf[i:]...)
			break
		}
		i += n
	}
	return out.Bytes(), rep.Bytes()
}

// command reads the command at the start of b (IAC …): how many bytes it took, or done false
// when b ends before it does.
func (c *Codec) command(b []byte, out, rep *bytes.Buffer) (int, bool) {
	if len(b) < 2 {
		return 0, false
	}
	switch b[1] {
	case cmdIAC:
		out.WriteByte(cmdIAC)
		return 2, true
	case cmdWILL, cmdWONT, cmdDO, cmdDONT:
		if len(b) < 3 {
			return 0, false
		}
		c.negotiate(b[1], b[2], rep)
		return 3, true
	case cmdSB:
		// IAC SB option … IAC SE, with IAC IAC for a 0xFF inside.
		for i := 2; i+1 < len(b); i++ {
			if b[i] != cmdIAC {
				continue
			}
			if b[i+1] == cmdSE {
				c.subnegotiate(unescape(b[2:i]), rep)
				return i + 2, true
			}
			i++ // IAC IAC, or another command inside: skipped
		}
		return 0, false
	default:
		return 2, true // GA, NOP, AYT, data mark…: nothing for a terminal
	}
}

// negotiate answers the server's WILL, WONT, DO or DONT for option.
func (c *Codec) negotiate(verb, option byte, rep *bytes.Buffer) {
	switch verb {
	case cmdWILL: // the server offers to do it
		asked := c.askedThem[option]
		delete(c.askedThem, option)
		if c.them[option] {
			return
		}
		if acceptsTheirs(option) {
			c.them[option] = true
			if !asked {
				rep.Write([]byte{cmdIAC, cmdDO, option})
			}
		} else {
			rep.Write([]byte{cmdIAC, cmdDONT, option})
		}
	case cmdWONT: // the server will not, or stops: our DO refused needs no answer
		delete(c.askedThem, option)
		if c.them[option] {
			c.them[option] = false
			rep.Write([]byte{cmdIAC, cmdDONT, option})
		}
	case cmdDO: // the server asks us to do it
		asked := c.askedUs[option]
		delete(c.askedUs, option)
		if c.us[option] {
			return
		}
		if acceptsOurs(option) {
			c.us[option] = true
			if !asked {
				rep.Write([]byte{cmdIAC, cmdWILL, option})
			}
			if option == optNAWS {
				rep.Write(c.naws())
			}
		} else {
			rep.Write([]byte{cmdIAC, cmdWONT, option})
		}
	case cmdDONT:
		delete(c.askedUs, option)
		if c.us[option] {
			c.us[option] = false
			rep.Write([]byte{cmdIAC, cmdWONT, option})
		}
	}
}

// acceptsTheirs says whether the terminal lets the server do option.
func acceptsTheirs(option byte) bool {
	return option == optEcho || option == optSGA || option == optBinary
}

// acceptsOurs says whether the terminal does option when asked.
func acceptsOurs(option byte) bool {
	return option == optTermType || option == optNAWS || option == optSGA || option == optBinary
}

// subnegotiate answers a subnegotiation: TERMINAL-TYPE SEND.
func (c *Codec) subnegotiate(body []byte, rep *bytes.Buffer) {
	if len(body) == 2 && body[0] == optTermType && body[1] == ttSEND && c.us[optTermType] {
		rep.Write([]byte{cmdIAC, cmdSB, optTermType, ttIS})
		rep.WriteString(TerminalType)
		rep.Write([]byte{cmdIAC, cmdSE})
	}
}

// unescape turns IAC IAC back into one IAC.
func unescape(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte{cmdIAC, cmdIAC}, []byte{cmdIAC})
}

// Send makes what the terminal types safe to send: IAC doubled, and a carriage return the
// network's end of line unless the server takes binary.
func (c *Codec) Send(in []byte) []byte {
	var out bytes.Buffer
	for i, b := range in {
		switch {
		case b == cmdIAC:
			out.Write([]byte{cmdIAC, cmdIAC})
		case b == '\r' && !c.us[optBinary]:
			out.WriteByte('\r')
			if i+1 >= len(in) || in[i+1] != '\n' {
				out.WriteByte('\n')
			}
		default:
			out.WriteByte(b)
		}
	}
	return out.Bytes()
}

// Resize records the window's size, and returns what tells the server when it agreed to NAWS
// (nil otherwise).
func (c *Codec) Resize(cols, rows int) []byte {
	c.cols, c.rows = cols, rows
	if !c.us[optNAWS] {
		return nil
	}
	return c.naws()
}

// naws is the window's size as NAWS sends it: width and height, 16 bits each, IAC doubled.
func (c *Codec) naws() []byte {
	var size [4]byte
	binary.BigEndian.PutUint16(size[0:], uint16(min(max(c.cols, 0), 0xffff)))
	binary.BigEndian.PutUint16(size[2:], uint16(min(max(c.rows, 0), 0xffff)))
	out := []byte{cmdIAC, cmdSB, optNAWS}
	for _, b := range size {
		out = append(out, b)
		if b == cmdIAC {
			out = append(out, cmdIAC)
		}
	}
	return append(out, cmdIAC, cmdSE)
}

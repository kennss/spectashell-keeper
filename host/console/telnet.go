// telnet.go
// Author: Kennt Kim
// Created: 2026-10-29 · Last updated: 2026-10-29
//
// Purpose: a telnet session as a console a keeper runs (Docs/DESIGN.md §5.8): the engine's telnet
// codec as the bridge's Translator, so `sdterm-keeper telnet` is the bridge with a TCP connection
// where a serial command has its port.
//
// Notes:
//   - The codec's opening requests are sent as the connection is made (OpenTelnet), before the
//     bridge reads anything, as a telnet client sends them.
// - Moved here from core/internal/console on 2026-10-29, with the keeper program
//   (Docs/DESIGN.md §3.2).

package console

import (
	"context"
	"net"
	"time"

	"github.com/kennss/spectashell-keeper/telnet"
)

// Telnet is the telnet codec as a translator.
type Telnet struct{ Codec *telnet.Codec }

func (t Telnet) FromRemote(b []byte) ([]byte, []byte) { return t.Codec.Receive(b) }
func (t Telnet) FromTerm(b []byte) []byte             { return t.Codec.Send(b) }
func (t Telnet) Resize(cols, rows int) []byte         { return t.Codec.Resize(cols, rows) }

// connectTimeout bounds reaching a telnet server: a console server that does not answer is said
// so, not waited on for TCP's minutes.
const connectTimeout = 15 * time.Second

// OpenTelnet connects to address ("host:port") for a window of cols × rows, and sends the requests
// a terminal opens with.
func OpenTelnet(address string, cols, rows int) (Remote, Translator, error) {
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, nil, err
	}
	codec, hello := telnet.NewCodec(cols, rows)
	if _, err := conn.Write(hello); err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, Telnet{codec}, nil
}

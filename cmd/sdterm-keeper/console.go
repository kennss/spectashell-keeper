// console.go
// Author: Kennt Kim
// Created: 2026-10-29 · Last updated: 2026-10-29
//
// Purpose: sdterm-keeper's consoles (Docs/DESIGN.md §5.8): a serial port, or a telnet session, run
// as the program of a keeper where a shell would be — so it stays open with the app closed, and
// is listed, replayed, opened and watched as a kept shell is — and the serial ports this machine
// has, for the app to choose from.
//
// Usage:
//   sdterm-keeper serial [--speed 115200] [--data 8] [--parity none|even|odd] [--stop 1|2]
//                        [--flow none|rts/cts|xon/xoff] <device>
//   sdterm-keeper telnet <host> [port]
//   sdterm-keeper serial-ports      prints {"ports": [{"path": …, "name": …}]}
//
// Notes:
//   - Standard input and output are the keeper's terminal, put in raw mode first (console.MakeRaw).
//   - A serial console holds the Mac awake while it runs (console.KeepAwake) and says so.
// - Moved here from core/cmd/sdterm-keeper on 2026-10-29, with the keeper program
//   (Docs/DESIGN.md §3.2).

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/kennss/spectashell-keeper/host/console"
)

// SerialPortsAnswer is what serial-ports prints.
type SerialPortsAnswer struct {
	Ports []console.Found `json:"ports"`
}

// serial runs a serial console on args; it returns the program's exit code.
func serial(args []string, in, out *os.File, errs io.Writer) int {
	flags := flag.NewFlagSet("serial", flag.ContinueOnError)
	flags.SetOutput(errs)
	port := console.Port{}
	flags.IntVar(&port.Speed, "speed", 115200, "bits per second")
	flags.IntVar(&port.DataBits, "data", 8, "data bits, 5 to 8")
	parity := flags.String("parity", string(console.ParityNone), "none, even or odd")
	flags.IntVar(&port.StopBits, "stop", 1, "stop bits, 1 or 2")
	flow := flags.String("flow", string(console.FlowNone), "none, rts/cts or xon/xoff")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 {
		fmt.Fprintln(errs, "usage: sdterm-keeper serial [--speed N] [--data N] [--parity P] [--stop N] [--flow F] <device>")
		return 2
	}
	port.Device, port.Parity, port.Flow = flags.Arg(0), console.Parity(*parity), console.Flow(*flow)
	if err := port.Check(); err != nil {
		fmt.Fprintln(errs, "sdterm-keeper serial:", err)
		return 2
	}
	_ = console.MakeRaw(in)
	b := &console.Bridge{In: in, Out: out, Name: fmt.Sprintf("%s · %d %s", port.Device, port.Speed, port.Framing()),
		Title: filepath.Base(port.Device), Releasable: true,
		Open: func() (console.Remote, console.Translator, error) {
			f, err := console.Open(port)
			if err != nil {
				return nil, nil, err
			}
			return f, console.Plain{}, nil
		},
		SendBreak: func(r console.Remote) error {
			if f, ok := r.(*os.File); ok {
				return console.SendBreak(f)
			}
			return nil
		}}
	if console.KeepAwake() {
		b.Note("This Mac stays awake while the console is open")
	}
	return b.Run(console.Resizes(in))
}

// telnetCommand runs a telnet session on args ("host [port]"); it returns the program's exit code.
func telnetCommand(args []string, in, out *os.File, errs io.Writer) int {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(errs, "usage: sdterm-keeper telnet <host> [port]")
		return 2
	}
	port := "23"
	if len(args) == 2 {
		if n, err := strconv.Atoi(args[1]); err != nil || n <= 0 || n > 65535 {
			fmt.Fprintln(errs, "sdterm-keeper telnet: port", args[1], "is not a port")
			return 2
		}
		port = args[1]
	}
	address := net.JoinHostPort(args[0], port)
	_ = console.MakeRaw(in)
	b := &console.Bridge{In: in, Out: out, Name: "telnet " + address, Title: args[0], EOFIsExit: true,
		Open: func() (console.Remote, console.Translator, error) {
			size := console.Size(in)
			return console.OpenTelnet(address, max(size[0], 1), max(size[1], 1))
		}}
	return b.Run(console.Resizes(in))
}

// serialPorts prints the serial ports of this machine.
func serialPorts(out io.Writer) {
	ports := console.Ports()
	if ports == nil {
		ports = []console.Found{}
	}
	_ = json.NewEncoder(out).Encode(SerialPortsAnswer{Ports: ports})
}

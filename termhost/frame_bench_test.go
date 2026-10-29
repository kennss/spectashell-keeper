// frame_bench_test.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-29
//
// Purpose: what a frame costs the core (Docs/DESIGN.md §3.1, M0): a terminal of 200 × 60 cells
// whose every row changed, in a different colour every few characters — the worst a frame carries
// short of history — built (Frame) and encoded (proto.Marshal), as the core does before handing it
// to the app. Run with `go test -bench Frame -run ^$ ./termhost`.
//
// Notes:
// - The terminal sits on a link of its own (memLink) that replays nothing and takes no input, so
//   only the emulator and the frame are measured.

//go:build unix

package termhost

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/kennss/spectashell-keeper/keeper"
)

// memLink is a link that sends an empty replay, then whatever is put on output, and takes nothing.
type memLink struct {
	output chan []byte
}

func (l *memLink) ReadEvents(ev keeper.Events) error {
	ev.ReplayEnd()
	for b := range l.output {
		ev.Output(b)
	}
	return nil
}
func (l *memLink) Input([]byte) error        { return nil }
func (l *memLink) Resize(int, int) error     { return nil }
func (l *memLink) SetMeta(keeper.Meta) error { return nil }
func (l *memLink) Hangup(keeper.Who)         { close(l.output) }
func (l *memLink) Close()                    { close(l.output) }

// colouredScreen is a screen's worth of text, its colour changing every 8 characters.
func colouredScreen(cols, rows int) string {
	var b strings.Builder
	b.WriteString("\x1b[H")
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x += 8 {
			fmt.Fprintf(&b, "\x1b[38;5;%dm%-8s", 16+(x+y)%216, fmt.Sprintf("w%d.%d", y, x))
		}
		if y < rows-1 {
			b.WriteString("\r\n")
		}
	}
	return b.String()
}

func BenchmarkFullScreenFrame(b *testing.B) {
	const cols, rows = 200, 60
	link := &memLink{output: make(chan []byte, 1)}
	changed := make(chan struct{}, 1)
	t := Attach(link, LinkInfo{Columns: cols, Rows: rows}, Config{OnChange: func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}})
	defer t.Close()
	screen := []byte(colouredScreen(cols, rows))
	var sync Sync
	<-changed // the empty replay
	t.Frame(&sync)
	size := 0
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		link.output <- screen
		<-changed
		sync = Sync{} // every row is sent again, as to views that hold nothing
		b.StartTimer()
		f, _ := t.Frame(&sync)
		bytes, err := proto.Marshal(f)
		if err != nil {
			b.Fatal(err)
		}
		size = len(bytes)
	}
	b.ReportMetric(float64(size), "bytes/frame")
}

// generate.go
// Author: Kennt Kim
// Created: 2026-10-23 · Last updated: 2026-10-23
//
// Purpose: `go generate` entry point that regenerates the engine's message code from ../proto.
//
// Notes:
// - Requires `buf` on PATH. The generated files are committed, so a plain `go build` works
//   without buf (SiliconDesk's host/internal/gen/generate.go does the same).

package gen

//go:generate sh -c "cd .. && buf generate"

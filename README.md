# spectashell-keeper — the terminal engine and the keeper SpectaShell puts on a host

> Author: Kennt Kim · Created: 2026-10-29 · Last updated: 2026-10-29
>
> Licensed under the Apache License 2.0 ([LICENSE](LICENSE)). Copyright 2026 Calida Lab.

SpectaShell is a terminal for the Mac, the iPad and the iPhone. On a machine where the user lets it,
it puts a small program there, `sdterm-keeper`, which holds the shell and its last output, so the
shell outlives a dropped connection, a sleeping device and the app being quit or updated.

This repository is all of that program's source: what runs on your machine can be read here, and
rebuilt from here to the same bytes. It is also SpectaShell's terminal engine — the emulator, the
frames the app draws, shell integration — because the keeper links them: it reads what runs in its
shell and what the shell's prompt reports with no app attached.

## What runs on a host

- **One program, installed by the app over your SSH login,** at
  `~/.local/share/sdterm/<build>/sdterm-keeper`. Nothing is installed system-wide and nothing needs
  root.
- **It listens on no network port.** The app reaches it only through exec channels of your own SSH
  connection; a keeper's socket is in a folder that must be yours with no group or other access
  (`/tmp/sdterm-<uid>/keepers` on Linux, `~/Library/Application Support/sdTerm/keepers` on a Mac),
  or nothing starts.
- **Its protocol is [Docs/KEEPER.md](Docs/KEEPER.md):** a frame format on a Unix socket, version 1
  for good, since keepers outlive the builds that started them.
- **A keeper ends when you close its shell from the app,** or 24 hours after its shell ended with
  no device connected, so a device that comes back can still read how it ended.

## Layout

| Path | What |
|---|---|
| `term/` | The emulator: escape sequences, the screen and its history, modes, prompts (OSC 133), notifications |
| `termhost/` | A terminal on a link: the emulator, the frames of changed rows, their pacing, starting a login shell |
| `keeper/` | The keeper: holding a shell, its protocol, its client, the agent link, ends |
| `paneinfo/` | What runs in a shell: the program in front, its folder, its ports, a coding agent's state (Linux `/proc`, Mac `libproc`) |
| `shellinteg/` | Shell integration for bash, zsh and fish, embedded in the program |
| `telnet/` | Telnet, as a codec and a terminal's link |
| `proto/`, `gen/` | The engine's messages (`terminal.proto`) and their generated Go |
| `cmd/sdterm-keeper/` | The program itself: `keep`, `start`, `attach`, `join`, `list`, `page`, `mcp`, `serial`, `telnet` |
| `host/` | The program's packages: its commands' messages (`hostkeeper`), the host's boot (`hostboot`), this user's processes (`procs`), a coding agent's page commands (`pages`), serial and telnet consoles (`console`) |
| `scripts/build-keeper.sh` | The build of the program for Linux and the Mac, with the checksums |

The engine's packages import nothing of `host/` or `cmd/`; those two are SpectaShell's program and
may name it.

## Building and testing

Go 1.26 or later; the program's builds use the toolchain `go.mod` names (`toolchain`).

```sh
go vet ./... && go test -race ./...
go generate ./gen        # the messages, after a change to proto/ (needs buf)
```

`scripts/build-keeper.sh <out-dir> <version>` builds what the app carries — the Mac's universal
binary and, for hosts, Linux and macOS on x86_64 and arm64 — on a Mac with Xcode, and writes
`<out-dir>/hosts/SHA256SUMS`. The build id is the version, a dot, and a hash of the sources.

## Checking a host's keeper

The Linux binaries are reproducible: the same commit and build id give the same bytes on any
machine.

```sh
# On the host: the build id, and the checksum of the binary that runs there.
~/.local/share/sdterm/*/sdterm-keeper version
sha256sum ~/.local/share/sdterm/<build id>/sdterm-keeper

# Here, at the commit of that release: build it the way the app's build does (GOARCH=arm64 for an
# arm64 host), and compare.
GOTOOLCHAIN=go1.27.2 CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -buildvcs=false -ldflags "-s -w -X main.build=<build id>" -o sdterm-keeper \
  ./cmd/sdterm-keeper
sha256sum sdterm-keeper
```

The macOS binaries are built with cgo, so they also depend on Xcode's compiler and SDK; the one
inside the Mac app is signed with the app.

## Reading the source

The code is written inside SpectaShell's own repository, which mounts this one at `engine/`. So a
comment's path that starts `engine/` is a path here, and the comments and documents name parts of
SpectaShell that are not published: its design (`Docs/DESIGN.md §…`), the app's Go
(`core/internal/…`) and Swift (`client/…`). Parts of the emulator and the keeper came from
SiliconDesk, by the same author; their headers say which file.

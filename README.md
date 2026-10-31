# spectashell-keeper — the terminal engine and the keeper SpectaShell puts on a host

> Author: Kennt Kim · Created: 2026-10-29 · Last updated: 2026-10-31
>
> Licensed under the Apache License 2.0 ([LICENSE](LICENSE)). Copyright 2026 Calida Lab.

SpectaShell is a terminal for the Mac, the iPad and the iPhone. On a machine where the user lets it,
it puts a small program there, `spectashell-keeper`, which holds the shell and its last output, so
the shell outlives a dropped connection, a sleeping device and the app being quit or updated.

This repository is all of that program's source: what runs on your machine can be read here, and
rebuilt from here to the same bytes. It is also SpectaShell's terminal engine — the emulator, the
frames the app draws, shell integration — because the keeper links them: it reads what runs in its
shell and what the shell's prompt reports with no app attached.

## What runs on a host

- **One program, installed by the app over your SSH login,** at
  `~/.local/share/spectashell/<build>/spectashell-keeper`. Nothing is installed system-wide and
  nothing needs root.
- **It listens on no network port.** The app reaches it only through exec channels of your own SSH
  connection; a keeper's socket is in a folder that must be yours with no group or other access
  (`/tmp/spectashell-<uid>/keepers` on Linux, `~/Library/Application Support/SpectaShell/keepers` on
  a Mac), or nothing starts. The one connection it makes is out, over HTTPS, to the push relay a
  device named, and only once a device turned notifications on for the machine
  ([Notifications](#notifications)).
- **Its protocol is [Docs/KEEPER.md](Docs/KEEPER.md):** a frame format on a Unix socket, version 1
  for good, since keepers outlive the builds that started them.
- **A keeper ends when you close its shell from the app,** or 24 hours after its shell ended with
  no device connected, so a device that comes back can still read how it ended.

## Layout

| Path | What |
|---|---|
| `term/` | The emulator: escape sequences, the screen and its history, modes, prompts (OSC 133), notifications |
| `termhost/` | A terminal on a link: the emulator, the frames of changed rows, their pacing, starting a login shell |
| `keeper/` | The keeper: holding a shell, its protocol, its client, the agent link, ends, what it notices for a user who is away and who is looking |
| `paneinfo/` | What runs in a shell: the program in front, its folder, its ports, a coding agent's state (Linux `/proc`, Mac `libproc`) |
| `shellinteg/` | Shell integration for bash, zsh and fish, embedded in the program |
| `telnet/` | Telnet, as a codec and a terminal's link |
| `proto/`, `gen/` | The engine's messages (`terminal.proto`) and their generated Go |
| `cmd/spectashell-keeper/` | The program itself: `keep`, `start`, `attach`, `join`, `list`, `version`, `builds`, `page`, `mcp`, `pages-relay`, `serial`, `serial-ports`, `telnet`, `push` |
| `host/` | The program's packages: its commands' messages (`hostkeeper`), the host's boot (`hostboot`), this user's processes (`procs`), a coding agent's page commands (`pages`), serial and telnet consoles (`console`), notifications to devices (`push`) and what a host and the push relay say to each other (`pushwire`) |
| `scripts/build-keeper.sh` | The build of the program for Linux and the Mac, with the checksums |

The engine's packages import nothing of `host/` or `cmd/`; those two are SpectaShell's program and
may name it.

## Notifications

A device can ask a host to tell it when a shell there needs the user while the device is away — a
coding agent asking permission, a task done, a long command ended. The app sets this up over your
SSH login; the keeper program does the rest on the host, and only for devices that asked.

- **One file per device, on the host only,**
  `~/.local/share/spectashell-push/<device tag>.json`: the folder 0700 and the files 0600, refused
  when they are a link, another user's or open to others. `spectashell-keeper push add` writes it
  from JSON on its standard input — `device_tag`, `relay` (an `https://` address; plain `http` only
  to `127.0.0.1` or `localhost`), `seal_key` (the device's X25519 public key for this host),
  `host_ref` (the device's own id for the host), `owners` (the device ids whose shells count),
  `events` and, optionally, `new_key`. `push remove <device tag>` deletes it; `push list` prints
  the files without their keys. Each answer is JSON on standard output.
- **The host signs with a key made on the host.** `push add` makes an Ed25519 key and keeps it in
  the file; only its public half and its id are printed, for the device to give the relay. Adding
  for a device that has a file already keeps its key, unless `new_key` asks for another (the old
  key's id is printed as `replaced`), and clears what the keepers learned sending with it.
- **What is sent, the device chose** (`events`): `blocked` (a program says it waits for the user:
  a permission, a question, a sign-in), `done`, `notify` (a program's own notification, OSC 9, 777
  or 99) and `command_seconds` (a command that ran at least that long; 0 is never). The keeper's own
  emulator notices them as the output comes, with no app connected. A file that names `owners` is
  not told of a shell another device started (the keeper keeps who started it, `started_by`).
- **Nothing is sent while you are looking.** A connected app says its device is in front with a
  lease that runs out by itself ([Docs/KEEPER.md](Docs/KEEPER.md) §2.2). While an owner looks, a
  `blocked` or `done` is held, for up to an hour, and sent once no owner looks if the program still
  says the same; a notification or a command's end, seen where it happened, is dropped.
  `when_looking`: `"send"` sends anyway; `"hold"` (or none) is the default. A `blocked` or `done`
  the program no longer says by the time it would go is not sent late.
- **The relay cannot read it.** What the device shows — the host's id, the keeper, the shell's
  name, the program, the state and, cleaned and cut to 180 bytes, the program's own words — is JSON
  padded to 512 bytes and sealed with HPKE (X25519, HKDF-SHA256, ChaCha20-Poly1305) to the device's
  key for this host. The request (`POST /v1/push`) is signed with the host's key over its method,
  path, time, a nonce and its body (`host/pushwire`). The relay's address comes from the file; this
  repository names no server.
- **How the relay answers decides what happens next.** 202: sent. 404 (it does not know the key):
  tried again after as long as the key has been unknown, from a minute up to an hour, and given up
  after 7 days until the device writes its file again; that state is kept beside the file, in
  `<device tag>.state`. 410 (the key was removed): the file and its state are deleted. 429: tried
  after `Retry-After`; no answer or a 5xx: tried again with a growing pause. A keeper sends at most
  one notification every 10 seconds (a `blocked` goes at once), and gives up on one it still could
  not send 10 minutes after it came. The time it sends is the relay's (`GET /v1/time`, and every
  answer's), so a host whose clock is off is still believed.

A keeper's description says `push` when its program tells devices this way (Docs/KEEPER.md §4).

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
~/.local/share/spectashell/*/spectashell-keeper version
sha256sum ~/.local/share/spectashell/<build id>/spectashell-keeper

# Here, at the commit of that release: build it the way the app's build does (GOARCH=arm64 for an
# arm64 host), and compare.
GOTOOLCHAIN=go1.27.2 CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -buildvcs=false -ldflags "-s -w -X main.build=<build id>" -o spectashell-keeper \
  ./cmd/spectashell-keeper
sha256sum spectashell-keeper
```

The macOS binaries are built with cgo, so they also depend on Xcode's compiler and SDK; the one
inside the Mac app is signed with the app.

## Reading the source

The code is written inside SpectaShell's own repository, which mounts this one at `engine/`. So a
comment's path that starts `engine/` is a path here, and the comments and documents name parts of
SpectaShell that are not published: its design (`Docs/design/01-DESIGN.md §…` and its parts, such
as `Docs/design/02-CONNECTIONS.md §3`), the app's Go (`core/internal/…`) and Swift (`client/…`).
Parts of the emulator and the keeper came from SiliconDesk, by the same author; their headers say
which file.

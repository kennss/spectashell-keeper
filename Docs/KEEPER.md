# The keeper protocol

> Author: Kennt Kim · Created: 2026-10-23 · Last updated: 2026-11-02
>
> Status: **version 1, forever.** It only grows: any later build must talk to a keeper an earlier
> build started. Code: `engine/keeper/protocol.go` (the format), `keeper.go` (the keeper),
> `client.go` (a client).

A keeper is a small process that holds one program on a pseudo-terminal, keeps its recent output,
and serves its clients over Unix sockets — one at a time holding the keyboard, the others watching
(§2.1) — so the program outlives whoever started it: an app that quits or updates, a session that is
replaced, a connection that drops (Docs/design/01-DESIGN.md §5). This is the terminal engine's
protocol, not any application's; SpectaShell and SiliconDesk speak it (SiliconDesk's host defined
it, `host/internal/keeper`, 2026-10-14).

## 1. Starting a keeper

The client runs the keeper program (`spectashell-keeper keep`) in a session of its own (`setsid`),
so it is in no client's process group, writes a configuration to its standard input as JSON and
closes it:

| Key | Meaning |
|---|---|
| `socket` | Where it listens: `<folder>/<keeper id>.sock` |
| `keeper_id` | Its id: 16 hexadecimal characters in SpectaShell (a socket's path is at most 104 bytes on macOS) |
| `path`, `argv`, `dir`, `env` | The program: what is executed, its arguments (`argv[0]` may be `-zsh`), its folder, its whole environment |
| `columns`, `rows` | The terminal's size; 0 is 80 × 24 |
| `meta` | What it remembers for the next client (§3); *(SpectaShell, 2026-10-30)* the only place `started_by` is set |
| `build` | *(SpectaShell, 2026-10-23)* The keeper program's build, for its description |

It answers one line on standard output — `ready`, or `error: <reason>` — then points its standard
streams at `/dev/null`. It listens on two sockets, each mode 0600, in a folder that must be the
user's own (§5): `<keeper id>.sock`, where a connection takes the keeper over, and *(SpectaShell,
2026-10-26)* `<keeper id>.join`, where a connection joins to watch (§2.1).

## 2. Frames

Every frame is a 4-byte big-endian length (of what follows), a 1-byte type and the payload, at most
1 MiB; output and input longer than that are split. A type a side does not know is skipped, a JSON
key it does not know is ignored, and nothing that exists changes meaning.

| Type | Direction | Payload |
|---|---|---|
| 1 Hello | keeper → client | JSON: `version` (1), `keeper_id`, `pid` (the program's), `columns`, `rows`, `started` (Unix seconds), `meta`, `exited`, `push` (as in descriptions; 2026-10-30) |
| 2 Output | keeper → client | The program's output |
| 3 ReplayEnd | keeper → client | None: the kept output ends here, live output follows |
| 4 Exit | keeper → client | JSON: `code` (-1 for a signal), `signal` (`SIGHUP`) |
| 5 TakenOver | keeper → client | *(SpectaShell, 2026-10-23)* None: another client connected and this connection closes next |
| 6 Info | keeper → client | *(SpectaShell, 2026-10-23)* JSON: what runs in the shell (§6) |
| 7 Size | keeper → client | *(SpectaShell, 2026-10-23)* Columns and rows, 2 bytes each, big-endian: in the replay, the size the kept output after it was written at; *(2026-10-26)* to a watcher, also the terminal's size whenever it changes |
| 8 Present | keeper → client | *(SpectaShell, 2026-10-26)* JSON: `holding` (this connection holds the keyboard), `holder` (`{client, client_id}` of the one that does, absent when none does), `watchers` (the same of each watcher): to every connection that joined or said Who, whenever it changes (§2.1) |
| 10 Input | client → keeper | Bytes for the program's input |
| 11 Resize | client → keeper | Columns and rows, 2 bytes each, big-endian |
| 12 Meta | client → keeper | JSON: what to remember (§3) |
| 13 Hangup | client → keeper | None, or *(SpectaShell, 2026-10-26)* JSON `{client, client_id}`: the client hanging up, as it names itself in Meta, for the keeper's end (§7). The program's process group gets `SIGHUP`, and the keeper ends |
| 14 Stop | client → keeper | None: `SIGTERM` to the program's whole process group, `SIGKILL` 10 seconds later, until it is empty; then the keeper ends |
| 15 Who | client → keeper | *(SpectaShell, 2026-10-26)* JSON `{client, client_id}`: names this connection, and says it can watch (§2.1) |
| 16 Take | client → keeper | *(SpectaShell, 2026-10-26)* None: a watcher takes the keyboard (§2.1) |
| 17 Front | client → keeper | *(SpectaShell, 2026-10-30)* JSON `{"for": seconds}`: this connection's device is in front, looking, for that long (at most 300); `{"for": 0}`: it went away (§2.2) |

Each connection gets, in order: Hello; the kept output — first the sequences that put back the modes
the program set in output no longer kept, then the last 1 MiB of output from a clean place (the
first escape or newline, so no tail of a sequence is shown as text) — then ReplayEnd; Exit, if the
program has ended; then live output.

**The kept output is sent at the sizes it was written at.** *(SpectaShell, 2026-10-23.)* A program
writes for the size it was told, so output drawn at another width wraps where the program did not
mean it to: zsh's end-of-line mark, written at 80 columns and drawn at 69, leaves a `%` above every
prompt (Docs/design/02-CONNECTIONS.md §1). The keeper remembers where in its kept output each Resize
took effect, and the replay sends Size before each stretch of one size: the first before any output,
the last with the size now, followed by no output when nothing was written since. A client draws
each stretch at its size, then goes on at its own. A client from before Size skips it and draws the
whole replay at one size, as before.

**A new connection takes the keeper over.** The keeper sends the connection it had TakenOver, closes
it, and serves the new one. A client from before TakenOver skips the frame and sees its connection
end. Connecting to look at a keeper therefore takes it from whoever held it: clients list keepers
from their descriptions (§4), never by connecting. *(2026-10-26.)* Only the keyboard's holder is
taken over; the connections watching stay (§2.1).

### 2.1 Joining

*(SpectaShell, 2026-10-26; Docs/design/07-TEAMS.md §1.)* A connection on the second socket,
`<keeper id>.join`, joins the keeper to watch: it gets Hello and the replay as any connection does,
and takes nothing from anyone. Of all the keeper's connections one at most holds the keyboard, and
the others watch.

| | The holder | A watcher |
|---|---|---|
| Output, Exit, Info | Sent | Sent |
| Size after the replay | Not sent: it set the size | Sent whenever the terminal's size changes |
| Input | Reaches the program | Ignored |
| Resize | Sizes the terminal | Remembered, for when it holds the keyboard |
| Meta, Stop | Taken | Ignored |
| Hangup | Ends the keeper | Only closes this connection |

**Who and Take.** Who names a connection and says it can watch. Take moves the keyboard to a
watcher, and the terminal to the size that watcher last asked for; the connection that held it
watches from then on if it said Who, else it is sent TakenOver and closed, as every client was
before joining. A client says Who as it connects; a keeper from before skips it, and Take.

**Present.** Every connection that joined or said Who is sent Present when it connects, and again
whenever the holder or the watchers change: whether it holds the keyboard, who does, and who
watches, each as it named itself.

**A watcher never holds the program back.** Each connection is written from a queue of its own. The
keeper reads no more of the program's output while more than 1 MiB waits for the holder, as its
socket's buffer held the program back before; a watcher with more than the kept output and 4 MiB
waiting is closed, and can join again for a replay.

**A keeper from before has no `<keeper id>.join`.** Joining it fails before connecting anywhere, so
a client meaning to watch never takes such a keeper over; `spectashell-keeper join` exits 3 and says
so.

### 2.2 Who is looking

*(SpectaShell, 2026-10-30; SpectaShell's Docs/design/08-PUSH.md §4.2.)* A keeper whose program asked
for the user — a status becoming `blocked` or `done` (§4), a program's own notification (OSC 9,
777, 99), a command that ran long — may tell a device that is away (the keeper program's observer,
`MainObserved`). It must first know whether anyone is looking. A connection says so with **Front**:
a lease, sent as it connects and again before it runs out while its device is in front, and
`{"for": 0}` as the device goes away. A connection counts as looking only while its lease runs, so
a device suspended before it could say so, or one that lost its network while its socket stays open
on the host, stops counting by itself. A connection that never sent Front — a client from before it
— is not looking. A keeper from before Front skips the frame (§2) and tells no one.

Whoever asks may ask about some devices only, by their `client_id`: the holder and the watchers
count alike, each by the id it named with Who, and a holder that named none by `meta.client_id`.
What the keeper tells — that a status became `blocked` or `done`, a program's notification with its
title and body, a command's end with its command line and how long it ran — and to whom is the
keeper program's, not the protocol's: SpectaShell's is in [the README](../README.md#notifications).

The keeper's own emulator, which reads all the output as it comes (the preview's), keeps the time,
so a command's length is known with no client there.

## 3. Meta

What a keeper remembers for the next client, and describes itself with:

| Key | Meaning |
|---|---|
| `owner` | The application that started it (`sdTerm`) |
| `persist` | It outlives its client by design (a terminal the user did not close) |
| `title`, `directory` | The last title and folder the client saw (the keeper does not parse output) |
| `restoration_id` | The client's name for the terminal's window, to put it back in the same place |
| `server` | SiliconDesk's: the port and token of a web app's server the keeper holds instead of a terminal |
| `client` | *(SpectaShell, 2026-10-23)* The device that attached last, by its name ("Sam's MacBook Pro"), as that client tells it on attaching |
| `client_id` | *(SpectaShell, 2026-10-25)* That device's own id, which tells it apart from another of the same name (every iPad is "iPad" to an app) |
| `origin` | *(SpectaShell, 2026-10-27)* The starting client's own name for where it started the program from — SpectaShell's saved host it reached the keeper through — kept as given: a client attaching later never changes it, so every client names the terminal alike whatever address it reaches the host by |
| `arrangement` | *(SpectaShell, 2026-10-30)* The client's description of where the terminal sits among others — SpectaShell's: the desk it is in, as JSON — kept as given and read only by clients; a client watching (§2.1) never sets it |
| `started_by` | *(SpectaShell, 2026-10-30)* The client that started the program, `{client, client_id}`, given in the keeper's configuration and kept by the keeper itself: a client's Meta never changes it, so one of an earlier build, which knows no such key, cannot drop it. SpectaShell closes a terminal another device started by leaving it to its keeper. Absent for a keeper started before |

## 4. Descriptions

*(SpectaShell, 2026-10-23.)* Each keeper writes `<keeper id>.json` beside its socket, mode 0600,
when it starts, when its meta changes and when its program ends, and removes it when it ends. It is
written to a temporary name and renamed, so a reader never sees half of one.

| Key | Meaning |
|---|---|
| `version`, `keeper_id`, `pid`, `started`, `meta`, `exited` | As in Hello |
| `keeper_pid` | The keeper's own process: a description whose keeper is gone is stale, and a reader removes it with its socket |
| `build` | The keeper program's build |
| `attached` | *(2026-10-23)* A client is connected now — `meta.client`, once it told its name. Written again on every attach and detach |
| `exit` | *(2026-10-23)* How the program ended, as the Exit frame says it (`code`, `signal`); absent while it runs |
| `preview` | *(2026-10-25)* The last lines of the terminal's screen, at most 4, as the keeper's own emulator draws the output: blank lines at the bottom left out, and each line's trailing spaces. Written at most every 2 seconds, when they changed; absent from earlier keepers |
| `full_screen` | *(2026-10-25)* The program draws its whole screen (the alternate screen: an editor, `top`, an agent's interface), so `preview` is a piece of that interface rather than output; absent while it does not |
| `screen_title` | *(2026-10-25)* The title the program set (OSC 0 or 2), as the keeper's emulator holds it: Claude Code names its task in it; absent while it set none |
| `status`, `status_kind` | *(2026-10-29)* What the program says it is doing, as the keeper's emulator reads its OSC 7501 records: the most pressing state (`blocked`, `error`, `working`, `done`, `idle`) and, with `blocked`, why (`permission`, `question`, `auth`). Written with the preview, when it changed. A `working` or `blocked` record ends when the shell is back in front (the keeper reads what runs while one is there, with no client too) or at the next prompt mark; absent while the program says nothing, and from earlier keepers |
| `watchers` | *(2026-10-26)* The connections watching (§2.1), each `{client, client_id}` as it named itself; absent while none does. `attached` and `meta.client` stay the holder's |
| `push` | *(2026-10-30)* The keeper program tells a device that is away what the program needs (§2.2): its connections' Front matters. Absent from earlier keepers, whose shells never tell |

A keeper touches its sockets, its description and the two folders above them every hour, so that
age-based cleaning of `/tmp` (systemd-tmpfiles) never removes them while it runs.

*(2026-10-25.)* A keeper whose socket or description was removed while it runs makes them again —
the sockets first, then the description, in their folder made again (0700) — every 10 seconds, and
at once on SIGUSR1, as tmux does with its socket. The listener it replaces is closed without
unlinking the path, which is the new socket's by then. An app cleaner that removes the app's
folder no longer leaves shells running out of every client's reach (02-CONNECTIONS.md §1).

## 5. Where keepers live

| System | Folder | Why |
|---|---|---|
| Linux | `/tmp/<name>-<uid>/keepers` | tmux's place. Not `$XDG_RUNTIME_DIR`, which logind deletes when the user's last login ends unless lingering is on (off by default on Ubuntu) |
| macOS | `~/Library/Application Support/<app>/keepers` | macOS's `tmp_cleaner` deletes regular files in `/tmp` untouched for 3 days — a description is one — and `$TMPDIR` is a per-user folder for an app yet unset in an SSH login |

The folder and the one above it are created 0700 and checked with `lstat` before use: a real
directory, not a symbolic link, owned by the user, with no group or other permission. On a shared
server another user could create the folder first, or a link in its place; then no keeper starts and
the client says why. An environment variable the application names (`SPECTASHELL_RUNTIME_DIR` for
SpectaShell) can name another folder.

*(SpectaShell, 2026-10-29.)* Beside a keeper's socket may be its agent link, `<id>.agent`: a
symbolic link to the SSH agent forwarded to the client holding the keeper, which a terminal started
with the agent forwarded names as SSH_AUTH_SOCK. A client points it as it attaches (replacing it in
one rename, under the lock of `<id>.agentlock`) and takes it back as it leaves, unless another
client pointed it meanwhile; a client watching never points it. The keeper removes both as it ends,
and a listing removes them with a dead keeper's socket.

## 6. What runs in the shell

*(SpectaShell, 2026-10-23.)* While a client is connected, the keeper reads what runs in its shell
every 2 seconds — beside the shell, on whichever machine it is (the engine's `paneinfo`: `/proc` on
Linux, `sysctl` and `libproc` on the Mac) — and sends Info after the replay and whenever it changed.

| Key | Meaning |
|---|---|
| `program`, `program_pid` | The program in front of the terminal — its foreground group's leader — as its command line, and its process id; the shell's name and pid at its prompt |
| `directory` | The folder of the program in front (the shell's at its prompt), spelled as the shell last reported it (the client's `meta.directory`) when that names the same folder; the report alone when the program's cannot be read. A report goes stale while a program started on the line that changed folder runs (`cd ~/x && make`), as the shell reports at its next prompt (2026-10-29) |
| `branch` | The git branch of that folder, read without running git |
| `ports` | The TCP ports the shell's processes listen on: `port`, `everywhere` (bound to every address), `process` |
| `remote` | The host when the program in front is `ssh` |
| `resume` | The command that resumes the coding agent in front (Claude Code, Codex), for after a restart |

## 7. How a keeper ends

After Hangup; after Stop; 24 hours after its program ended with no client connected; or on SIGTERM,
SIGINT, SIGQUIT or SIGHUP, as it would on Hangup (SpectaShell, 2026-10-29: before, any of them ended
it at once, without an end). A client that only disconnects never ends it, so the next client can
still show the program's exit.

*(SpectaShell, 2026-10-26.)* Ending, a keeper writes `<keeper id>.end` beside its socket, mode 0600,
by a temporary name renamed, before it removes its socket and description: a device whose shell
another closed can then say so, where before it could only say the shell was gone (a Mac closed two
shells, and the iPads' tabs said their programs had ended).

| Key | Meaning |
|---|---|
| `version`, `keeper_id` | As in Hello |
| `ended` | When it ended, Unix seconds; for `lost`, when a reader found it gone |
| `how` | `closed` (Hangup), `stopped` (Stop), `expired` (its program had ended and no client came within 24 hours), `signalled` (a signal, `signal`; 2026-10-29), or `lost`: a reader found its description with no keeper — it was killed, or its machine went down — and wrote this, unless an end was already there |
| `by` | For `closed`, the client the Hangup named (`client`, `client_id`); absent when it named none, as a client from before does |
| `exit` | How its program ended, when it did before the keeper (as in the description): the program ended by itself, and the keeper was closed or left after |
| `signal` | *(2026-10-29)* For `signalled`, the signal that ended the keeper (`SIGTERM`) |
| `events` | *(2026-10-29)* For `lost`, the last lines of the keeper's event log: what it last did before it was killed |

A reader removes ends older than 30 days, and files named `.end` that are not one.

*(SpectaShell, 2026-10-29.)* A keeper keeps an event log, `<keeper id>.log` beside its socket, mode
0600: a line with its time for each thing that happens to it — started, a client attached, joined,
named itself, taken over or gone, its program's end, a signal, its own end — cut to its newest half
past 64 KiB. A keeper that writes its end removes it; a reader that writes `lost` for a keeper puts
the log's last 12 lines in that end, as `events`, and removes the log. A SIGKILL, which no program
can catch, then still leaves what the keeper last did. *Why:* a local shell's keeper ended at the
moment the app quit, leaving no end, no crash report and no line in the system's log, and the same
steps did not end another.

## 8. History

| Date | Change |
|---|---|
| 2026-10-14 | Version 1, in SiliconDesk: Hello, Output, ReplayEnd, Exit, Input, Resize, Meta, Hangup |
| 2026-10-15 | `restoration_id` in Meta |
| 2026-10-19 | Stop, and `server` in Meta (SiliconDesk's web apps) |
| 2026-10-23 | SpectaShell: TakenOver, descriptions, `build`, the folders' times refreshed, Info, Size; `client` in Meta, `attached` and `exit` in descriptions |
| 2026-10-25 | SpectaShell: `client_id` in Meta; `preview`, `full_screen` and `screen_title` in descriptions; a socket or description removed is made again |
| 2026-10-26 | SpectaShell: Hangup names its client; a keeper's end (`<id>.end`); joining — the second socket (`<id>.join`), Who, Take, Present, Size to watchers, `watchers` in descriptions |
| 2026-10-27 | SpectaShell: `origin` in Meta |
| 2026-10-29 | SpectaShell: `status` and `status_kind` in descriptions; a signal ends a keeper as Hangup does, its end `signalled` with `signal`; the event log (`<id>.log`), and `events` in a `lost` end; the agent link (`<id>.agent`, §5) |
| 2026-10-30 | SpectaShell: `arrangement` and `started_by` in Meta, `started_by` set only by the configuration |
| 2026-10-30 | SpectaShell: Front; `push` in descriptions and Hello |

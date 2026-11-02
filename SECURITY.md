# Security

> Author: Kennt Kim · Created: 2026-11-02 · Last updated: 2026-11-02

`spectashell-keeper` runs on your machines, under your login, holding your shells. A flaw in it is
a flaw on every host where SpectaShell keeps shells, so a report of one is welcome and is read
first.

## Reporting a vulnerability

Report it privately, not in a public issue:

- through GitHub's private vulnerability reporting, on this repository's **Security** tab; or
- by mail to [kennt@calidalab.ai](mailto:kennt@calidalab.ai), as
  [security.txt](https://www.calidalab.ai/.well-known/security.txt) says.

Say what you found, the commit or build it is in (`spectashell-keeper version` prints the build),
and how to reproduce it. The report stays private until a fix is out, and is credited in its
release notes unless you ask otherwise.

## What is in scope

- The keeper program (`cmd/spectashell-keeper`, `host/`): its sockets and their folders, the files
  it writes beside them, what it starts and with which environment, its commands, the push files
  and what it sends to the relay ([README](README.md#notifications)).
- The engine it links: the emulator (`term/`), what it reads of a shell's processes (`paneinfo/`),
  shell integration (`shellinteg/`), telnet (`telnet/`).
- A build that does not reproduce: the Linux binaries are meant to rebuild to the same bytes
  ([README](README.md)), and one that does not is worth a report.

SpectaShell's apps, its push relay and calidalab.ai are not in this repository; report a flaw in
them to the same address.

## Supported versions

The keeper a current SpectaShell installs. Each build of the app installs its own keeper beside the
earlier ones (`~/.local/share/spectashell/<build>/`), and a fix reaches a host with the next build
of the app.

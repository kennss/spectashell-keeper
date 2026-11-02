# Contributing

> Author: Kennt Kim · Created: 2026-11-02 · Last updated: 2026-11-02

This repository is the source of what SpectaShell puts on your machines, published so it can be
read and rebuilt. Issues and pull requests are welcome; a security issue goes privately instead
([SECURITY.md](SECURITY.md)).

## How a change lands

The engine is written inside SpectaShell's own repository, which mounts this one at `engine/`
([README](README.md#reading-the-source)). A pull request here is read, and when it is taken it is
applied there and committed here from there, so the commit that lands may differ from yours. A
contribution is under the Apache License 2.0, as section 5 of [LICENSE](LICENSE) says.

## Before you send one

- **Build and test.** Go 1.26 or later (the build uses `go.mod`'s toolchain):

  ```sh
  go vet ./... && go test -race ./...
  ```

  The shell integration tests run bash (4.4 or later), zsh and fish where they are installed, and
  skip a shell that is missing.

- **Keep the protocol.** The keeper's protocol is version 1 for good
  ([Docs/KEEPER.md](Docs/KEEPER.md)): keepers outlive the builds that started them, so a change
  only adds, and an older client or keeper must still work with it.
- **Keep the direction.** The engine's packages import nothing of `host/` or `cmd/`; those are
  SpectaShell's keeper program and may name it.
- **Write like the code around it.** Every file starts with a header (what it is, its author, when
  it was created and last updated, notes on its gotchas); comments say why, in full sentences.
  A bug fixed comes with a test that fails without the fix.
- **Permissive licences only.** A new dependency is MIT, BSD, Apache 2.0, ISC or zlib.
- **No real hosts.** Examples use `example.com`, documentation addresses (`192.0.2.0/24`) and
  made-up users (`/home/sam`).

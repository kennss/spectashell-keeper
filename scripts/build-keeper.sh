#!/bin/sh
# build-keeper.sh
# Author: Kennt Kim
# Created: 2026-10-23 · Last updated: 2026-10-29
#
# Purpose: builds sdterm-keeper (cmd/sdterm-keeper), the program that holds a shell (Docs/KEEPER.md;
# SpectaShell's Docs/DESIGN.md §5.1, §5.3): for the Mac app's local shells, one universal binary
# (arm64 and x86_64); and for hosts, one gzipped binary per system and machine, which the app puts
# there; with the checksums of the hosts' binaries.
#
# Usage: scripts/build-keeper.sh <out-dir> <build>
#   <out-dir>/sdterm-keeper        universal macOS binary; Xcode copies it into
#                                  Contents/Helpers and signs it with the app
#   <out-dir>/hosts/sdterm-keeper-<goos>-<goarch>.gz
#                                  linux-amd64, linux-arm64, darwin-arm64, darwin-amd64; the app
#                                  carries them as resources (keepers/), for the core to install
#   <out-dir>/hosts/build          the keepers' build id, which the app gives the core (Start.build)
#   <out-dir>/hosts/SHA256SUMS     the SHA-256 of each host binary as a host runs it (unzipped),
#                                  and the Go toolchain that built them
#
# Notes:
# - Built with cgo, with Xcode's clang and the macOS SDK: on the Mac the keeper reads what runs in
#   its shell through libproc (paneinfo), which only C reaches. The Linux keepers for hosts are
#   built without cgo, static; paneinfo reads /proc there. The Mac hosts' are the same two cgo
#   binaries as the universal one, apart: a host is sent only its own machine's.
# - Reproducible: the Go toolchain is go.mod's (GOTOOLCHAIN), paths are trimmed and no VCS stamp is
#   recorded, so the same sources and build id give the same Linux binaries on any machine (the
#   README says how to check one). The Mac's also depend on Xcode's clang and SDK.
# - The keepers' build id is <build>, a dot, and the hash below: it names the folder a host's keeper
#   is installed in (~/.local/share/sdterm/<id>), so a keeper whose sources changed is a new build
#   there even when the app's version did not change. With <build> alone (2026-10-23: "0.1.0+1"),
#   a host kept the keeper first installed, whatever was built after. The id is recorded in the
#   binary (main.build) and in each keeper's description.
# - The hash is of this script, go.mod and go.sum, and every file of this module that goes into the
#   program for a Mac or Linux — Go, cgo, C and embedded files, as `go list` names them. Until
#   2026-10-29 it was every non-test .go file, which left out the shell integration scripts
#   (shellinteg's go:embed): a changed script made no new build, and hosts kept the old one.
# - Skipped when <out-dir> already holds a binary of the same build and sources.
# - A binary built here for a Mac host is signed ad hoc by the linker, which an arm64 Mac requires
#   to run it.

set -eu

here=$(cd "$(dirname "$0")/.." && pwd)
out=${1:?usage: build-keeper.sh <out-dir> <build>}
build=${2:?usage: build-keeper.sh <out-dir> <build>}
mkdir -p "$out"
out=$(cd "$out" && pwd)
cd "$here"

PATH="$PATH:/opt/homebrew/bin:/usr/local/go/bin:/usr/local/bin"
command -v go >/dev/null || { echo "error: build-keeper.sh: Go is required (https://go.dev/dl/)" >&2; exit 1; }
GOTOOLCHAIN=$(sed -n 's/^toolchain //p' go.mod)
export GOTOOLCHAIN

# inputs lists the files of this module that go into the program, for a Mac (cgo) and for Linux.
inputs() {
	for target in darwin/1 linux/0; do
		GOOS=${target%/*} CGO_ENABLED=${target#*/} go list -deps -f '{{if .Module}}{{if .Module.Main}}{{$d := .Dir}}
{{- range .GoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}{{range .CgoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}
{{- range .CFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}{{range .HFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}
{{- range .EmbedFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}{{end}}{{end}}' ./cmd/sdterm-keeper
	done | LC_ALL=C sort -u
}
files=$(inputs)
[ -n "$files" ] || { echo "error: build-keeper.sh: go list named no sources" >&2; exit 1; }
hash=$( (echo "$build"; cat scripts/build-keeper.sh go.mod go.sum; echo "$files" | tr '\n' '\0' | xargs -0 cat) |
	shasum -a 256 | cut -c1-16)
id="$build.$hash"
if [ -f "$out/sdterm-keeper.hash" ] && [ "$(cat "$out/sdterm-keeper.hash")" = "$hash" ] && [ -x "$out/sdterm-keeper" ] \
	&& [ -f "$out/hosts/sdterm-keeper-linux-amd64.gz" ] && [ -f "$out/hosts/build" ] && [ -f "$out/hosts/SHA256SUMS" ]; then
	exit 0
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
sysroot=$(xcrun --sdk macosx --show-sdk-path)
clang=$(xcrun --sdk macosx -f clang)
for arch in arm64 amd64; do
	flags="-arch $([ $arch = amd64 ] && echo x86_64 || echo arm64) -mmacosx-version-min=15.0"
	env CGO_ENABLED=1 GOOS=darwin GOARCH=$arch CC="$clang -isysroot $sysroot $flags" \
		go build -trimpath -buildvcs=false -ldflags "-X main.build=$id" -o "$work/sdterm-keeper-darwin-$arch" ./cmd/sdterm-keeper
done
for arch in amd64 arm64; do
	env CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
		go build -trimpath -buildvcs=false -ldflags "-s -w -X main.build=$id" -o "$work/sdterm-keeper-linux-$arch" ./cmd/sdterm-keeper
done
mkdir -p "$out/hosts"
lipo -create "$work/sdterm-keeper-darwin-arm64" "$work/sdterm-keeper-darwin-amd64" -output "$out/sdterm-keeper"
for target in linux-amd64 linux-arm64 darwin-arm64 darwin-amd64; do
	gzip -9 -n -c "$work/sdterm-keeper-$target" > "$out/hosts/sdterm-keeper-$target.gz"
done
(cd "$work" && echo "# sdterm-keeper $id, $(go env GOVERSION)" &&
	shasum -a 256 sdterm-keeper-linux-amd64 sdterm-keeper-linux-arm64 sdterm-keeper-darwin-arm64 sdterm-keeper-darwin-amd64) \
	> "$out/hosts/SHA256SUMS"
echo "$id" > "$out/hosts/build"
echo "$hash" > "$out/sdterm-keeper.hash"
echo "build-keeper.sh: built $out/sdterm-keeper ($id)"

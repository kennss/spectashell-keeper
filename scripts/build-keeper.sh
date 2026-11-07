#!/bin/sh
# build-keeper.sh
# Author: Kennt Kim
# Created: 2026-10-23 · Last updated: 2026-11-07
#
# Purpose: builds spectashell-keeper (cmd/spectashell-keeper), the program that holds a shell
# (Docs/KEEPER.md; SpectaShell's Docs/design/02-CONNECTIONS.md §1, 02-CONNECTIONS.md §3): for the
# Mac app's local shells, one universal app bundle (arm64 and x86_64); and for hosts, one gzipped
# binary per Linux machine and one gzipped tar of an app bundle per Mac machine, which the app puts
# there; with the checksums of what the hosts run.
#
# Usage: [KEEPER_SIGN_IDENTITY=<identity>] [KEEPER_SIGN_TIMESTAMP=1] scripts/build-keeper.sh <out-dir> <build>
#   <out-dir>/spectashell-keeper.app
#                                  universal macOS bundle; Xcode copies it into Contents/Helpers
#   <out-dir>/hosts/spectashell-keeper-linux-<goarch>.gz
#                                  linux-amd64, linux-arm64
#   <out-dir>/hosts/spectashell-keeper-darwin-<goarch>.app.tar.gz
#                                  darwin-arm64, darwin-amd64: the bundle, spectashell-keeper.app
#                                  The app carries hosts/ as resources (keepers/), for the core to
#                                  install
#   <out-dir>/hosts/build          the keepers' build id, which the app gives the core (Start.build)
#   <out-dir>/hosts/SHA256SUMS     the SHA-256 of each program as a host runs it (unpacked: the
#                                  Linux binaries, the Mac bundles' executables), and the Go
#                                  toolchain that built them
#   A build replaces <out-dir>/hosts whole.
#
# Notes:
# - Built with cgo, with Xcode's clang and the macOS SDK: on the Mac the keeper reads what runs in
#   its shell through libproc (paneinfo), and starts its keepers responsible for themselves
#   (keeper/spawn_darwin.go); only C reaches either. The Linux keepers for hosts are built without
#   cgo, static; paneinfo reads /proc there. The Mac hosts' are the same two cgo binaries as the
#   universal one, apart: a host is sent only its own machine's.
# - A Mac's keeper is an app bundle, id ai.calidalab.sdterm.keeper, named SpectaShell Keeper
#   (2026-11-07): macOS keeps a privacy grant (~/Desktop, ~/Documents…) for a bundle by its id and
#   signer, and for a lone binary by its path, so a keeper that answers for itself was asked again
#   at every build's new folder. KEEPER_SIGN_IDENTITY signs the bundles, with the hardened runtime;
#   SpectaShell's builds give its Developer ID, so one grant serves every build. Without it they are
#   signed ad hoc: they run, and macOS asks again for each build. KEEPER_SIGN_TIMESTAMP=1 adds a
#   secure timestamp (a release; it asks Apple's server), else none.
# - Reproducible: the Go toolchain is go.mod's (GOTOOLCHAIN), paths are trimmed and no VCS stamp is
#   recorded, so the same sources and build id give the same Linux binaries on any machine (the
#   README says how to check one). The Mac's also depend on Xcode's clang and SDK, and carry their
#   signature.
# - The keepers' build id is <build>, a dot, and the hash below: it names the folder a host's keeper
#   is installed in (~/.local/share/spectashell/<id>), so a keeper whose sources changed is a new build
#   there even when the app's version did not change. With <build> alone (2026-10-23: "0.1.0+1"),
#   a host kept the keeper first installed, whatever was built after. The id is recorded in the
#   binary (main.build) and in each keeper's description.
# - The hash is of this script, go.mod and go.sum, and every file of this module that goes into the
#   program for a Mac or Linux — Go, cgo, C and embedded files, as `go list` names them. Until
#   2026-10-29 it was every non-test .go file, which left out the shell integration scripts
#   (shellinteg's go:embed): a changed script made no new build, and hosts kept the old one. The
#   signing identity is not in it — the same sources are the same build however signed — but in
#   the stamp that skips a build.
# - Skipped when <out-dir> already holds a build of the same sources, signed the same way.

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
{{- range .EmbedFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}{{end}}{{end}}' ./cmd/spectashell-keeper
	done | LC_ALL=C sort -u
}
files=$(inputs)
[ -n "$files" ] || { echo "error: build-keeper.sh: go list named no sources" >&2; exit 1; }
hash=$( (echo "$build"; cat scripts/build-keeper.sh go.mod go.sum; echo "$files" | tr '\n' '\0' | xargs -0 cat) |
	shasum -a 256 | cut -c1-16)
id="$build.$hash"
sign=${KEEPER_SIGN_IDENTITY:--}
timestamp=--timestamp=none
[ "${KEEPER_SIGN_TIMESTAMP:-}" = 1 ] && timestamp=--timestamp
stamp="$hash $sign $timestamp"
if [ -f "$out/spectashell-keeper.hash" ] && [ "$(cat "$out/spectashell-keeper.hash")" = "$stamp" ] \
	&& [ -x "$out/spectashell-keeper.app/Contents/MacOS/spectashell-keeper" ] \
	&& [ -f "$out/hosts/spectashell-keeper-linux-amd64.gz" ] && [ -f "$out/hosts/build" ] && [ -f "$out/hosts/SHA256SUMS" ]; then
	exit 0
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
sysroot=$(xcrun --sdk macosx --show-sdk-path)
clang=$(xcrun --sdk macosx -f clang)
for arch in arm64 amd64; do
	flags="-arch $([ $arch = amd64 ] && echo x86_64 || echo arm64) -mmacosx-version-min=15.0"
	env CGO_ENABLED=1 GOOS=darwin GOARCH=$arch CC="$clang -isysroot $sysroot $flags" \
		go build -trimpath -buildvcs=false -ldflags "-X main.build=$id" -o "$work/spectashell-keeper-darwin-$arch" ./cmd/spectashell-keeper
done
for arch in amd64 arm64; do
	env CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
		go build -trimpath -buildvcs=false -ldflags "-s -w -X main.build=$id" -o "$work/spectashell-keeper-linux-$arch" ./cmd/spectashell-keeper
done

# bundle makes <dir>/spectashell-keeper.app around the binary <program>, and signs it.
version=${build%%+*}
number=${build#*+}
bundle() {
	app="$1/spectashell-keeper.app"
	mkdir -p "$app/Contents/MacOS"
	cp "$2" "$app/Contents/MacOS/spectashell-keeper"
	cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key><string>ai.calidalab.sdterm.keeper</string>
	<key>CFBundleName</key><string>SpectaShell Keeper</string>
	<key>CFBundleDisplayName</key><string>SpectaShell Keeper</string>
	<key>CFBundleExecutable</key><string>spectashell-keeper</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
	<key>CFBundleShortVersionString</key><string>$version</string>
	<key>CFBundleVersion</key><string>$number</string>
	<key>LSMinimumSystemVersion</key><string>15.0</string>
	<key>LSBackgroundOnly</key><true/>
	<key>NSDesktopFolderUsageDescription</key><string>Your shells kept by SpectaShell open the files you ask for on your Desktop.</string>
	<key>NSDocumentsFolderUsageDescription</key><string>Your shells kept by SpectaShell open the files you ask for in Documents.</string>
	<key>NSDownloadsFolderUsageDescription</key><string>Your shells kept by SpectaShell open the files you ask for in Downloads.</string>
	<key>NSRemovableVolumesUsageDescription</key><string>Your shells kept by SpectaShell open the files you ask for on removable volumes.</string>
	<key>NSNetworkVolumesUsageDescription</key><string>Your shells kept by SpectaShell open the files you ask for on network volumes.</string>
</dict>
</plist>
PLIST
	codesign --force --options runtime "$timestamp" --sign "$sign" "$app"
}

# hosts/, which the app copies whole, is made in a folder of its own and then replaces the old one:
# a file an earlier build left there under another name is not carried into the app (2026-10-29:
# the keepers renamed from sdterm-keeper, whose binaries stayed beside the new ones and went into
# the bundle).
new="$work/out"
mkdir -p "$new/hosts"
lipo -create "$work/spectashell-keeper-darwin-arm64" "$work/spectashell-keeper-darwin-amd64" -output "$work/spectashell-keeper-darwin"
bundle "$new" "$work/spectashell-keeper-darwin"
for target in linux-amd64 linux-arm64; do
	gzip -9 -n -c "$work/spectashell-keeper-$target" > "$new/hosts/spectashell-keeper-$target.gz"
done
for target in darwin-arm64 darwin-amd64; do
	mkdir "$work/$target"
	bundle "$work/$target" "$work/spectashell-keeper-$target"
	cp "$work/$target/spectashell-keeper.app/Contents/MacOS/spectashell-keeper" "$work/spectashell-keeper-$target"
	# Owner and extended attributes left out: the host unpacks it as its user, and an AppleDouble
	# file beside the signed ones would break the signature.
	COPYFILE_DISABLE=1 tar --no-mac-metadata --uid 0 --gid 0 --uname root --gname wheel -cf - -C "$work/$target" spectashell-keeper.app |
		gzip -9 -n > "$new/hosts/spectashell-keeper-$target.app.tar.gz"
done
(cd "$work" && echo "# spectashell-keeper $id, $(go env GOVERSION)" &&
	shasum -a 256 spectashell-keeper-linux-amd64 spectashell-keeper-linux-arm64 spectashell-keeper-darwin-arm64 spectashell-keeper-darwin-amd64) \
	> "$new/hosts/SHA256SUMS"
echo "$id" > "$new/hosts/build"
rm -rf "$out/hosts" "$out/spectashell-keeper" "$out/spectashell-keeper.app"
mv "$new/hosts" "$out/hosts"
mv "$new/spectashell-keeper.app" "$out/spectashell-keeper.app"
echo "$stamp" > "$out/spectashell-keeper.hash"
echo "build-keeper.sh: built $out/spectashell-keeper.app and hosts/ ($id, signed by $sign)"

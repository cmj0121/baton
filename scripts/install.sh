#!/bin/sh
# baton installer — fetch the prebuilt binary for this host and drop it on PATH.
#
#   curl -fsSL https://raw.githubusercontent.com/cmj0121/baton/main/scripts/install.sh | sh
#
# Environment:
#   BATON_VERSION  install a specific tag (default: the latest release)
#   BATON_PREFIX   install prefix (default: /usr/local, or ~/.local when that is not writable)
#
# On macOS `brew install cmj0121/tap/baton` is the better path — it upgrades with
# the rest of your casks. This script is for Linux, and for anyone without brew.
set -eu

REPO="cmj0121/baton"
TMP=""

die() {
	printf 'install: %s\n' "$1" >&2
	exit 1
}

cleanup() {
	[ -n "$TMP" ] && rm -rf "$TMP"
}
trap cleanup EXIT INT TERM

need() {
	command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"
}

need tar

# curl and wget are interchangeable here; take whichever the host has.
if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL "$1" -o "$2"; }
	fetch_stdout() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -qO "$2" "$1"; }
	fetch_stdout() { wget -qO- "$1"; }
else
	die "either curl or wget is required"
fi

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) die "unsupported OS: $(uname -s) — build from source with 'go install github.com/$REPO/cmd/baton@latest'" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) die "unsupported architecture: $(uname -m)" ;;
esac

version="${BATON_VERSION:-}"
if [ -z "$version" ]; then
	# resolve the latest tag without needing jq on the host.
	version=$(fetch_stdout "https://api.github.com/repos/$REPO/releases/latest" |
		sed -n 's/.*"tag_name" *: *"\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$version" ] || die "could not resolve the latest release — set BATON_VERSION"
fi
# the archives are named without the leading v, the tags carry it.
bare="${version#v}"

prefix="${BATON_PREFIX:-}"
if [ -z "$prefix" ]; then
	if [ -w /usr/local/bin ] 2>/dev/null; then
		prefix=/usr/local
	else
		prefix="$HOME/.local"
	fi
fi
bindir="$prefix/bin"

archive="baton_${bare}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"

TMP=$(mktemp -d)
printf 'install: baton %s (%s/%s) -> %s\n' "$version" "$os" "$arch" "$bindir"

fetch "$base/$archive" "$TMP/$archive" || die "download failed: $base/$archive"

# verify against the release's own checksums file; skip only if the host has
# neither sha256 tool, and say so rather than pretending it was checked.
if fetch "$base/checksums.txt" "$TMP/checksums.txt" 2>/dev/null; then
	want=$(sed -n "s/^\([0-9a-f]*\)  *$archive\$/\1/p" "$TMP/checksums.txt" | head -n 1)
	if [ -n "$want" ]; then
		if command -v sha256sum >/dev/null 2>&1; then
			got=$(sha256sum "$TMP/$archive" | cut -d' ' -f1)
		elif command -v shasum >/dev/null 2>&1; then
			got=$(shasum -a 256 "$TMP/$archive" | cut -d' ' -f1)
		else
			got=""
			printf 'install: no sha256 tool found, skipping checksum verification\n' >&2
		fi
		if [ -n "$got" ] && [ "$got" != "$want" ]; then
			die "checksum mismatch for $archive"
		fi
	fi
fi

tar -xzf "$TMP/$archive" -C "$TMP" baton || die "could not extract baton from $archive"

mkdir -p "$bindir"
install -m 0755 "$TMP/baton" "$bindir/baton" 2>/dev/null ||
	{ cp "$TMP/baton" "$bindir/baton" && chmod 0755 "$bindir/baton"; } ||
	die "could not install to $bindir — set BATON_PREFIX to a writable location"

printf 'install: baton %s installed to %s/baton\n' "$version" "$bindir"
case ":$PATH:" in
*":$bindir:"*) printf 'install: run "baton" to start.\n' ;;
*) printf 'install: %s is not on your PATH — add it, then run "baton".\n' "$bindir" ;;
esac

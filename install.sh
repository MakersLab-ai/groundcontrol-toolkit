#!/bin/sh
# GROUNDCONTROL CLI installer.
#
#   curl -fsSL https://groundcontrol.makerslab.ai/install.sh | sh
#   curl -fsSL https://groundcontrol.makerslab.ai/install.sh | sh -s -- --version 0.1.0
#   curl -fsSL https://groundcontrol.makerslab.ai/install.sh | sh -s -- --install-dir /usr/local/bin
#
# Downloads gc for this OS/arch, verifies its SHA-256 against checksums.txt,
# installs it to ~/.local/bin (or --install-dir / $GC_INSTALL_DIR) and links
# `groundcontrol` -> `gc` (oh-my-zsh aliases `gc` to `git commit`).
# Updating = running this again.
set -eu

BASE="${GC_INSTALL_BASE:-https://groundcontrol.makerslab.ai}"
INSTALL_DIR="${GC_INSTALL_DIR:-$HOME/.local/bin}"
VERSION=""

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --version) [ $# -ge 2 ] || die "--version needs a value"; VERSION="$2"; shift 2 ;;
    --version=*) VERSION="${1#*=}"; shift ;;
    --install-dir) [ $# -ge 2 ] || die "--install-dir needs a value"; INSTALL_DIR="$2"; shift 2 ;;
    --install-dir=*) INSTALL_DIR="${1#*=}"; shift ;;
    -h|--help)
      say "Usage: install.sh [--version X.Y.Z] [--install-dir DIR]"
      say "Env: GC_INSTALL_DIR (default ~/.local/bin), GC_INSTALL_BASE (default https://groundcontrol.makerslab.ai)"
      exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

case "$(uname -s)" in
  Darwin) OS=darwin ;;
  Linux) OS=linux ;;
  *) die "unsupported OS: $(uname -s) (gc ships for macOS and Linux)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) die "unsupported architecture: $(uname -m) (gc ships for amd64 and arm64)" ;;
esac

ASSET="gc_${OS}_${ARCH}.tar.gz"
QUERY=""
[ -n "$VERSION" ] && QUERY="?version=$VERSION"

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --retry 2 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
else
  die "need curl or wget"
fi

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  die "need sha256sum or shasum to verify the download"
fi

TMP="$(mktemp -d 2>/dev/null || mktemp -d -t gc-install)"
trap 'rm -rf "$TMP"' EXIT
trap 'exit 130' INT TERM

say "Downloading $ASSET${VERSION:+ (version $VERSION)}…"
fetch "$BASE/api/cli/download/$ASSET$QUERY" "$TMP/$ASSET" || die "download failed: $BASE/api/cli/download/$ASSET$QUERY"
fetch "$BASE/api/cli/download/checksums.txt$QUERY" "$TMP/checksums.txt" || die "download failed: checksums.txt"

EXPECTED="$(awk -v f="$ASSET" '$2 == f || $2 == "*"f {print $1}' "$TMP/checksums.txt")"
[ -n "$EXPECTED" ] || die "checksums.txt has no entry for $ASSET"
ACTUAL="$(sha256 "$TMP/$ASSET")"
[ "$EXPECTED" = "$ACTUAL" ] || die "checksum mismatch for $ASSET (expected $EXPECTED, got $ACTUAL) — not installing"

tar -xzf "$TMP/$ASSET" -C "$TMP" gc || die "could not unpack $ASSET"
mkdir -p "$INSTALL_DIR"
# Replace via rename so a running gc is never overwritten in place.
cp "$TMP/gc" "$INSTALL_DIR/.gc.new"
chmod 755 "$INSTALL_DIR/.gc.new"
mv -f "$INSTALL_DIR/.gc.new" "$INSTALL_DIR/gc"
ln -sf gc "$INSTALL_DIR/groundcontrol"

if [ "$OS" = darwin ] && command -v xattr >/dev/null 2>&1; then
  xattr -d com.apple.quarantine "$INSTALL_DIR/gc" 2>/dev/null || true
fi

say "Installed $("$INSTALL_DIR/gc" version) to $INSTALL_DIR/gc (also: groundcontrol)"

case ":$PATH:" in
  *":$INSTALL_DIR:"*) GC="gc" ;;
  *)
    GC="$INSTALL_DIR/gc"
    say ""
    say "$INSTALL_DIR is not on your PATH. Add it (e.g. to ~/.zshrc or ~/.bashrc):"
    say "  export PATH=\"$INSTALL_DIR:\$PATH\""
    ;;
esac

say ""
say "Next: connect it to your workspace with the agent API key from GROUNDCONTROL:"
say "  $GC onboarding --token gc_live_…"
say "  $GC context"

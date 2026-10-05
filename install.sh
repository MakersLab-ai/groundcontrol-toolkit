#!/bin/sh
# GROUNDCONTROL CLI installer.
#
#   curl -fsSL https://github.com/MakersLab-ai/groundcontrol-toolkit/releases/latest/download/install.sh | sh
#   … | sh -s -- --version 0.2.0
#   … | sh -s -- --install-dir /usr/local/bin
#
# Downloads gc for this OS/arch from the GitHub release, verifies its SHA-256
# against the release's checksums.txt, installs it to ~/.local/bin (or
# --install-dir / $GC_INSTALL_DIR) and links `groundcontrol` -> `gc`
# (oh-my-zsh aliases `gc` to `git commit`). Updating = running this again.
#
# GC_INSTALL_BASE is a testing hook: a URL of a directory that holds
# gc_<os>_<arch>.tar.gz and checksums.txt. It moves the checksum source too, so
# the check only proves the download matches that directory — point it only at
# assets you built or trust.
set -eu

REPO="MakersLab-ai/groundcontrol-toolkit"
TMP=""
NEW=""

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }
cleanup() {
  [ -n "$NEW" ] && rm -f "$NEW"
  [ -n "$TMP" ] && rm -rf "$TMP"
  return 0
}

# Everything runs inside main, called on the very last line: a download cut
# off mid-way by `curl | sh` defines an incomplete function and runs nothing.
main() {
  INSTALL_DIR="${GC_INSTALL_DIR:-}"
  VERSION=""

  while [ $# -gt 0 ]; do
    case "$1" in
      --version) [ $# -ge 2 ] || die "--version needs a value"; VERSION="$2"; shift 2 ;;
      --version=*) VERSION="${1#*=}"; shift ;;
      --install-dir) [ $# -ge 2 ] || die "--install-dir needs a value"; INSTALL_DIR="$2"; shift 2 ;;
      --install-dir=*) INSTALL_DIR="${1#*=}"; shift ;;
      -h|--help)
        say "Usage: install.sh [--version X.Y.Z] [--install-dir DIR]"
        say "Env: GC_INSTALL_DIR (default ~/.local/bin); GC_INSTALL_BASE (testing hook: a directory URL holding the"
        say "     release assets — the checksums come from there too)"
        exit 0 ;;
      *) die "unknown argument: $1 (see --help)" ;;
    esac
  done
  VERSION="${VERSION#v}"

  if [ -z "$INSTALL_DIR" ]; then
    [ -n "${HOME:-}" ] || die "HOME is not set — pass --install-dir <dir> or set GC_INSTALL_DIR"
    INSTALL_DIR="$HOME/.local/bin"
  fi

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
  if [ -n "${GC_INSTALL_BASE:-}" ]; then
    BASE="${GC_INSTALL_BASE%/}"
  elif [ -n "$VERSION" ]; then
    BASE="https://github.com/$REPO/releases/download/v$VERSION"
  else
    BASE="https://github.com/$REPO/releases/latest/download"
  fi

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
  trap cleanup EXIT
  trap 'exit 130' INT TERM

  say "Downloading $ASSET${VERSION:+ (version $VERSION)}…"
  fetch "$BASE/$ASSET" "$TMP/$ASSET" || die "download failed: $BASE/$ASSET"
  fetch "$BASE/checksums.txt" "$TMP/checksums.txt" || die "download failed: $BASE/checksums.txt"

  EXPECTED="$(awk -v f="$ASSET" '$2 == f || $2 == "*"f {print $1}' "$TMP/checksums.txt")"
  [ -n "$EXPECTED" ] || die "checksums.txt has no entry for $ASSET"
  ACTUAL="$(sha256 "$TMP/$ASSET")"
  [ "$EXPECTED" = "$ACTUAL" ] || die "checksum mismatch for $ASSET (expected $EXPECTED, got $ACTUAL) — not installing"

  tar -xzf "$TMP/$ASSET" -C "$TMP" gc || die "could not unpack $ASSET"
  mkdir -p "$INSTALL_DIR"
  # Replace via rename so a running gc is never overwritten in place.
  NEW="$INSTALL_DIR/.gc.new"   # removed by cleanup() if anything below fails
  cp "$TMP/gc" "$NEW" || die "could not write to $INSTALL_DIR"
  chmod 755 "$NEW"
  mv -f "$NEW" "$INSTALL_DIR/gc"
  NEW=""
  ln -sf gc "$INSTALL_DIR/groundcontrol"

  if [ "$OS" = darwin ] && command -v xattr >/dev/null 2>&1; then
    xattr -d com.apple.quarantine "$INSTALL_DIR/gc" 2>/dev/null || true
  fi

  say "Installed $("$INSTALL_DIR/gc" version) to $INSTALL_DIR/gc (also: groundcontrol)"

  case ":${PATH:-}:" in
    *":$INSTALL_DIR:"*) GC="gc" ;;
    *)
      GC="$INSTALL_DIR/gc"
      say ""
      say "$INSTALL_DIR is not on your PATH. Add it (e.g. to ~/.zshrc or ~/.bashrc):"
      say "  export PATH=\"$INSTALL_DIR:\$PATH\""
      ;;
  esac

  say ""
  say "Next: connect it to your workspace with the agent API key from GROUNDCONTROL."
  say "Via stdin, which keeps the key out of ps and your shell history:"
  say "  printf %s \"\$KEY\" | $GC onboarding --token -"
  say "or directly:"
  say "  $GC onboarding --token \"gc_live_…\""
  say "Then check with: $GC context"
}

main "$@"

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
# If the wanted version is already installed (in the install dir or on PATH),
# nothing is downloaded and only the next steps are printed — so a setup prompt
# that always starts with this installer is harmless on a machine that already
# has gc (e.g. baked into a container image). --force reinstalls anyway.
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

# installed_version <path> → "X.Y.Z" if <path> is our gc, else nothing.
# </dev/null: another tool named gc (Graphviz has one) must not wait on stdin.
installed_version() {
  [ -x "$1" ] || return 0
  "$1" version </dev/null 2>/dev/null | sed -n 's/^gc v\{0,1\}\([0-9][0-9A-Za-z.+-]*\)$/\1/p' | head -n 1
}

next_steps() {
  say ""
  say "Next: connect it to your workspace with the agent API key from GROUNDCONTROL."
  say "Via stdin, which keeps the key out of ps and your shell history:"
  say "  printf %s \"\$KEY\" | $1 onboarding --token -"
  say "or directly:"
  say "  $1 onboarding --token \"gc_live_…\""
  say "Then check with: $1 context"
}
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
  FORCE=""

  while [ $# -gt 0 ]; do
    case "$1" in
      --version) [ $# -ge 2 ] || die "--version needs a value"; VERSION="$2"; shift 2 ;;
      --version=*) VERSION="${1#*=}"; shift ;;
      --install-dir) [ $# -ge 2 ] || die "--install-dir needs a value"; INSTALL_DIR="$2"; shift 2 ;;
      --install-dir=*) INSTALL_DIR="${1#*=}"; shift ;;
      --force) FORCE=1; shift ;;
      -h|--help)
        say "Usage: install.sh [--version X.Y.Z] [--install-dir DIR] [--force]"
        say "Skips the download when that version (default: the latest release) is already installed in DIR or"
        say "on PATH; --force reinstalls."
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

  # Already installed? The wanted version is --version, else the latest release
  # (read off GitHub's /releases/latest redirect; if that fails we just install).
  WANT="$VERSION"
  if [ -z "$WANT" ] && [ -z "${GC_INSTALL_BASE:-}" ] && command -v curl >/dev/null 2>&1; then
    WANT="$(curl -fsSLI --retry 2 -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" 2>/dev/null \
      | sed -n 's#.*/releases/tag/v\{0,1\}##p')" || WANT=""
  fi
  if [ -n "$WANT" ] && [ -z "$FORCE" ]; then
    ON_PATH="$(command -v gc 2>/dev/null || true)"
    for c in "$INSTALL_DIR/gc" "$ON_PATH"; do
      [ -n "$c" ] || continue
      if [ "$(installed_version "$c")" = "$WANT" ]; then
        say "gc $WANT is already installed at $c — nothing to download (--force reinstalls)."
        if [ "$c" = "$ON_PATH" ]; then next_steps gc; else next_steps "$c"; fi
        return 0
      fi
    done
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

  # Another gc earlier on PATH would shadow the one we just installed.
  ON_PATH="$(command -v gc 2>/dev/null || true)"
  if [ "$GC" = gc ] && [ -n "$ON_PATH" ] && [ "$ON_PATH" != "$INSTALL_DIR/gc" ]; then
    say ""
    say "Note: \`gc\` on your PATH is $ON_PATH ($(installed_version "$ON_PATH" || true)), not $INSTALL_DIR/gc."
    GC="$INSTALL_DIR/gc"
  fi

  next_steps "$GC"
}

main "$@"

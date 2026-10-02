#!/bin/sh
# src/client.ts and src/redact.ts are VERBATIM copies of the GROUNDCONTROL
# repo's claude-code-plugin/server/src/{client,redact}.ts — the same API client
# the Claude Code plugin and the hosted MCP server (/api/mcp) use. Change it
# there, then run this (with the groundcontrol repo checked out next to this
# one, or GC_REPO=<path>). `--check` exits 1 when the copies differ.
set -eu
GC_REPO="${GC_REPO:-$(dirname "$0")/../../groundcontrol}"
SRC="$GC_REPO/claude-code-plugin/server/src"
[ -f "$SRC/client.ts" ] || { echo "no $SRC/client.ts — set GC_REPO" >&2; exit 2; }
if [ "${1:-}" = "--check" ]; then
  diff -q "$SRC/client.ts" src/client.ts && diff -q "$SRC/redact.ts" src/redact.ts && echo "client in sync"
  exit $?
fi
cp "$SRC/client.ts" "$SRC/redact.ts" src/
echo "copied client.ts + redact.ts from $SRC"

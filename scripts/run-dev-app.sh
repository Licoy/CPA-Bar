#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EXE="$ROOT/build/cpa-bar-dev"

# Build and run in the foreground with the normal settings and credentials.
# Quit an already running CPA-Bar first; Ctrl+C stops this development run.
cd "$ROOT"
go build -o "$EXE" .

printf 'CPA-Bar Dev: %s\n' "$EXE"
exec "$EXE" "$@"

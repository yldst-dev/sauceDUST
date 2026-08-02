#!/usr/bin/env sh
set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
BIN="${SAUCEDUST_BIN:-$SCRIPT_DIR/saucedust}"

if [ "${1:-}" = "--bin" ]; then
  shift
  BIN="$1"
  shift
fi

if [ ! -f "$BIN" ]; then
  echo "saucedust binary not found: $BIN" >&2
  exit 1
fi

chmod 755 "$BIN"

if command -v xattr >/dev/null 2>&1; then
  xattr -cr "$BIN" || true
fi

if command -v codesign >/dev/null 2>&1; then
  codesign --force --sign - "$BIN" >/dev/null 2>&1 || true
fi

exec "$BIN" "$@"

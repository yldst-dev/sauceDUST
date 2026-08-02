#!/usr/bin/env sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
OUT_DIR="${1:-$ROOT_DIR/dist/saucedust-release}"

cd "$ROOT_DIR"
cargo build --release -p sauce-supervisor

rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"

cp target/release/saucedust "$OUT_DIR/"
chmod +x "$OUT_DIR/saucedust"
cp scripts/run_saucedust.sh "$OUT_DIR/"
chmod +x "$OUT_DIR/run_saucedust.sh"
if command -v xattr >/dev/null 2>&1; then
  xattr -cr "$OUT_DIR/saucedust" || true
  xattr -cr "$OUT_DIR/run_saucedust.sh" || true
fi
if command -v codesign >/dev/null 2>&1; then
  codesign --force --sign - "$OUT_DIR/saucedust"
fi

echo "$OUT_DIR"

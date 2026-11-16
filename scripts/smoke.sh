#!/bin/sh
set -eu

binary=${1:?usage: sh scripts/smoke.sh PATH_TO_BRAINDEX}
repo=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
temp=$(mktemp -d)
trap 'rm -rf "$temp"' EXIT
library=$temp/library
binary_dir=$(CDPATH='' cd -- "$(dirname -- "$binary")" && pwd)
binary="$binary_dir/$(basename -- "$binary")"

# A release user can try the binary from an empty directory without a checkout,
# configured library, network-dependent model, or commands on PATH.
mkdir "$temp/demo"
(
  cd "$temp/demo"
  env -i HOME="$temp/demo" TMPDIR="$temp/demo" PATH=/nonexistent "$binary" demo > "$temp/demo.txt"
)
grep -Fq '[buying-climate] Buying Climate Brief, line 1' "$temp/demo.txt"
grep -Fq '[field-notes] Discovery Field Notes, line 1' "$temp/demo.txt"
grep -Fq 'Reopened source: Buying Climate Brief, line 1' "$temp/demo.txt"
test -z "$(ls -A "$temp/demo")"

"$binary" --library "$library" init >/dev/null
"$binary" --library "$library" brain create founder-sales >/dev/null
"$binary" --library "$library" source add "$repo/examples/founder-led-sales/discovery.md" --brain founder-sales >/dev/null
"$binary" --library "$library" rule set founder-led-sales --match 'founder-led sales' --brain founder-sales
"$binary" --library "$library" index rebuild
"$binary" --library "$library" context 'How should I run founder-led sales discovery?' --mode keyword > "$temp/context.json"
grep -q '"status": "hits"' "$temp/context.json"
grep -q '"locator": "line ' "$temp/context.json"
grep -q '"revision": "' "$temp/context.json"
printf '%s\n' 'Braindex smoke test passed.'

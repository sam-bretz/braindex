#!/bin/sh
set -eu

version=${1:?usage: sh scripts/release.sh vMAJOR.MINOR.PATCH}
case "$version" in
  *[!A-Za-z0-9.-]*) printf '%s\n' 'Invalid version characters.' >&2; exit 2 ;;
esac
if ! printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  printf '%s\n' 'Use a version such as v0.1.0.' >&2
  exit 2
fi

repo=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo"
mkdir -p dist
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export CGO_ENABLED=0
module=$(go list -m)
goroot=$(go env GOROOT)

for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  os=${target%/*}
  arch=${target#*/}
  name="braindex_${version}_${os}_${arch}"
  stage="$work/$name"
  mkdir -p "$stage/third-party/go"
  cp LICENSE README.md "$stage/"
  cp "$goroot/LICENSE" "$goroot/PATENTS" "$stage/third-party/go/"

  # Bundle notices for the modules actually linked into this binary.
  GOOS=$os GOARCH=$arch go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}{{"\t"}}{{.Dir}}{{end}}{{end}}' ./cmd/braindex > "$work/deps.raw"
  sort -u "$work/deps.raw" > "$work/deps"
  while IFS="$(printf '\t')" read -r dep directory; do
    [ -n "$dep" ] || continue
    dest="$stage/third-party/$dep"
    mkdir -p "$dest"
    found=false
    for notice in "$directory"/LICENSE* "$directory"/LICENCE* "$directory"/NOTICE* "$directory"/COPYING* "$directory"/COPYRIGHT* "$directory"/PATENTS*; do
      [ -f "$notice" ] || continue
      cp "$notice" "$dest/"
      found=true
    done
    if [ "$found" != true ]; then
      printf 'No license notice found for %s\n' "$dep" >&2
      exit 1
    fi
  done < "$work/deps"

  GOOS=$os GOARCH=$arch go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X $module/internal/cli.buildVersion=$version" \
    -o "$stage/braindex" ./cmd/braindex
  COPYFILE_DISABLE=1 tar -czf "$work/$name.tar.gz" -C "$stage" braindex LICENSE README.md third-party
done

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$work" && sha256sum braindex_*.tar.gz) > "$work/checksums.txt"
else
  (cd "$work" && shasum -a 256 braindex_*.tar.gz) > "$work/checksums.txt"
fi
mv "$work"/braindex_*.tar.gz dist/
mv "$work/checksums.txt" "dist/braindex_${version}_checksums.txt"
printf 'Built Braindex %s archives and checksums in dist/\n' "$version"

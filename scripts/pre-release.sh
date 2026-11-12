#!/bin/sh
set -eu

version=${1:-}
eval_mode=offline
if [ "$#" -eq 2 ] && [ "$2" = "--local-evals" ]; then
  eval_mode=local
elif [ "$#" -ne 1 ]; then
  printf '%s\n' 'Usage: sh scripts/pre-release.sh VERSION [--local-evals]' >&2
  exit 2
fi
if ! printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  printf '%s\n' 'Use a version such as v0.1.0-rc.4.' >&2
  exit 2
fi

repo=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
output="$repo/dist/prerelease/$version"
if [ -e "$output" ]; then
  printf 'Candidate already exists: %s\n' "$output" >&2
  exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$repo"
git diff --check
git diff --cached --check
printf '\n==> Snapshot working tree based on %s\n' "$(git rev-parse --short HEAD)"
git ls-files --cached --others --exclude-standard -z > "$work/files"
mkdir "$work/source" "$work/installed"
tar --no-xattrs --no-acls -cf "$work/source.tar" --null -T "$work/files"
tar -xf "$work/source.tar" -C "$work/source"
cd "$work/source"
if [ -n "$(find . -type l -print)" ]; then
  printf '%s\n' 'Release snapshots must contain no symbolic links.' >&2
  exit 1
fi

unset GOOS GOARCH
export GOWORK=off
host="$(go env GOHOSTOS)_$(go env GOHOSTARCH)"

printf '\n==> Go checks\n'
go version
unformatted=$(find . -name '*.go' -type f -exec gofmt -l {} +)
if [ -n "$unformatted" ]; then
  printf 'Run gofmt on:\n%s\n' "$unformatted" >&2
  exit 1
fi
go mod verify
go vet ./...
(
  unset BRAINDEX_EVAL_MODEL BRAINDEX_EVAL_GENERATOR
  go test -race -count=1 ./...
)

printf '\n==> Gmail and email fixtures\n'
go test -count=1 -v -run 'Gmail|Email' ./internal/cli ./internal/connectors ./internal/ingest

printf '\n==> Evals (%s)\n' "$eval_mode"
sh scripts/eval.sh "$eval_mode"

printf '\n==> Go vulnerability scan\n'
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

printf '\n==> Build and verify candidate archives\n'
sh scripts/release.sh "$version"
checksums="braindex_${version}_checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  (cd dist && sha256sum -c "$checksums")
else
  (cd dist && shasum -a 256 -c "$checksums")
fi

printf '\n==> Smoke-test extracted %s binary\n' "$host"
tar -xzf "dist/braindex_${version}_${host}.tar.gz" -C "$work/installed" braindex
test "$("$work/installed/braindex" --version)" = "braindex $version"
sh scripts/smoke.sh "$work/installed/braindex"

mkdir -p "$repo/dist/prerelease"
mkdir "$output"
if ! cp dist/braindex_* "$output/"; then
  rm -rf "$output"
  exit 1
fi
printf '\nPre-release checks passed. Candidate files: %s\n' "$output"

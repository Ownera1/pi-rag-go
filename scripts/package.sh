#!/bin/sh
set -eu

version=${1:?usage: package.sh VERSION [OUTPUT_DIRECTORY]}
case "$version" in *[!A-Za-z0-9.+-]*|'') echo "Invalid version" >&2; exit 1;; esac
output=${2:-dist}
mkdir -p "$output"
output=$(cd "$output" && pwd)
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT HUP INT TERM
commit=$(git rev-parse --short HEAD)
platform=$(go env GOOS)
architecture=$(go env GOARCH)
export CGO_ENABLED=1
for binary in rag; do
  go build -trimpath -tags sqlite_fts5 -ldflags "-s -w -X github.com/Ownera1/rag-go/internal/command.Version=$version -X github.com/Ownera1/rag-go/internal/command.Commit=$commit" -o "$stage/$binary" "./cmd/$binary"
done
cp LICENSE README.md "$stage/"
archive="rag-go_${version}_${platform}_${architecture}.tar.gz"
tar -czf "$output/$archive" -C "$stage" rag LICENSE README.md
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$output" && sha256sum "$archive") > "$output/$archive.sha256"
else
  (cd "$output" && shasum -a 256 "$archive") > "$output/$archive.sha256"
fi
printf '%s\n' "$output/$archive"

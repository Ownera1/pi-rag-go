#!/bin/sh
set -eu

version=""
prefix="${HOME}/.local/bin"
base="${RAG_RELEASE_BASE_URL:-https://github.com/Ownera1/rag-go/releases}"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) [ "$#" -ge 2 ] || exit 2; version=$2; shift 2;;
    --prefix) [ "$#" -ge 2 ] || exit 2; prefix=$2; shift 2;;
    --help) echo 'install.sh [--version vX.Y.Z] [--prefix DIRECTORY]'; exit 0;;
    *) echo "Unknown argument: $1" >&2; exit 2;;
  esac
done
case "$(uname -s)" in Darwin) platform=darwin;; Linux) platform=linux;; *) echo 'Only macOS and Linux are supported' >&2; exit 1;; esac
case "$(uname -m)" in arm64|aarch64) architecture=arm64;; x86_64|amd64) architecture=amd64;; *) echo 'Unsupported CPU architecture' >&2; exit 1;; esac
[ "$platform-$architecture" != darwin-amd64 ] || { echo 'Intel Macs are not supported' >&2; exit 1; }
if [ -z "$version" ]; then
  latest=$(curl --fail --silent --show-error --location --proto '=https' --write-out '%{url_effective}' --output /dev/null "$base/latest")
  version=${latest##*/}
fi
case "$version" in v[0-9]*) ;; *) echo 'Version must start with v and a number' >&2; exit 1;; esac
case "$version" in *[!A-Za-z0-9.+-]*) echo 'Invalid version' >&2; exit 1;; esac
archive="rag-go_${version}_${platform}_${architecture}.tar.gz"
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM
curl --fail --silent --show-error --location --proto '=https' "$base/download/$version/$archive" --output "$scratch/$archive"
curl --fail --silent --show-error --location --proto '=https' "$base/download/$version/checksums.txt" --output "$scratch/checksums.txt"
expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$scratch/checksums.txt")
[ "${#expected}" -eq 64 ] || { echo 'Missing or invalid checksum' >&2; exit 1; }
case "$expected" in *[!0-9a-fA-F]*) echo 'Invalid checksum' >&2; exit 1;; esac
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$scratch/$archive" | awk '{ print $1 }')
else
  actual=$(shasum -a 256 "$scratch/$archive" | awk '{ print $1 }')
fi
[ "$expected" = "$actual" ] || { echo 'Checksum mismatch; installation aborted' >&2; exit 1; }
tar -tzf "$scratch/$archive" > "$scratch/contents"
while IFS= read -r entry; do
  case "$entry" in rag|LICENSE|README.md) ;; *) echo 'Unexpected archive entry' >&2; exit 1;; esac
done < "$scratch/contents"
mkdir "$scratch/stage"
tar -xzf "$scratch/$archive" -C "$scratch/stage"
for binary in rag; do
  [ -f "$scratch/stage/$binary" ] && [ ! -L "$scratch/stage/$binary" ] || { echo "Missing binary: $binary" >&2; exit 1; }
done
mkdir -p "$prefix"
for binary in rag; do
  destination=$(mktemp "$prefix/.${binary}.XXXXXX")
  cp "$scratch/stage/$binary" "$destination"
  chmod 755 "$destination"
  mv -f "$destination" "$prefix/$binary"
done
printf 'Installed %s to %s. Add this directory to PATH and run rag init.\n' "$version" "$prefix"
printf 'Run rag init inside each workspace, then rag connect claude or rag connect codex.\n'

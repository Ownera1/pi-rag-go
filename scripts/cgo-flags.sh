#!/bin/sh
# Print CGO_CFLAGS that compile sqlite-vec against go-sqlite3's bundled
# SQLite headers. A system sqlite3.h older than 3.38 (Ubuntu 22.04 ships
# 3.37) disables vec0 "rowid IN" prefilters, breaking filtered vector search.
# Setting CGO_CFLAGS replaces Go's default "-O2 -g", so keep it when the
# caller has none; otherwise SQLite and sqlite-vec compile unoptimized.
#   export CGO_CFLAGS="$(scripts/cgo-flags.sh)"
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
go mod download github.com/mattn/go-sqlite3
dir=$(cd "$root" && go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3)
test -f "$dir/sqlite3-binding.h" || { echo "go-sqlite3 headers not found" >&2; exit 1; }
printf '%s\n' "-I$root/scripts/sqlite3 -I$dir ${CGO_CFLAGS:--O2 -g}"

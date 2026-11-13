#!/bin/sh
# Static build for the host, or all six targets with -all. Flags: see `go run ./tools/dev build -h`.
set -eu
cd "$(dirname "$0")/.."
exec go run ./tools/dev build "$@"

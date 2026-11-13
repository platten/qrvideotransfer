#!/bin/sh
# Tests. The first argument may name a task: test (default), cover, lint, fuzz or check-tools.
#   scripts/test.sh                unit tests, CGO_ENABLED=0
#   scripts/test.sh -race          race detector (enables cgo)
#   scripts/test.sh -long          include long tests
#   scripts/test.sh cover          tests with the 95% per-package coverage gate
#   scripts/test.sh lint           golangci-lint
#   scripts/test.sh fuzz -time 30s every fuzz target for 30 s
set -eu
cd "$(dirname "$0")/.."
task="test"
case "${1:-}" in
test | cover | lint | fuzz | check-tools)
	task=$1
	shift
	;;
esac
exec go run ./tools/dev "$task" "$@"

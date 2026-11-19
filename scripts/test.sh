#!/bin/sh
# Tests. The first argument may name a task: test (default), cover, lint, fuzz, check-tools,
# licenses or check-workflows.
#   scripts/test.sh                unit tests, CGO_ENABLED=0
#   scripts/test.sh -race          race detector (enables cgo)
#   scripts/test.sh -race -part isolated   the root package, internal/pipeline and internal/video,
#                                          each alone (-part others: every other package)
#   scripts/test.sh -pkg ./internal/vision,.   only the listed packages, split as ./... is
#   scripts/test.sh -long          include long tests
#   scripts/test.sh -untagged      without the release build tags (RELEASE_TAGS in .goreleaser.yaml)
#   scripts/test.sh cover          tests with the 95% per-package coverage gate (-part rest, then
#                                  -part video: internal/video in a run of its own, then the gate)
#   scripts/test.sh lint           golangci-lint
#   scripts/test.sh fuzz -time 30s every fuzz target for 30 s
set -eu
cd "$(dirname "$0")/.."
task="test"
case "${1:-}" in
test | cover | lint | fuzz | check-tools | licenses | check-workflows)
	task=$1
	shift
	;;
esac
exec go run ./tools/dev "$task" "$@"

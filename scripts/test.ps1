# Tests. The first argument may name a task: test (default), cover, lint, fuzz, check-tools,
# licenses or check-workflows.
#   scripts/test.ps1                unit tests, CGO_ENABLED=0
#   scripts/test.ps1 -race          race detector (enables cgo)
#   scripts/test.ps1 -race -part isolated   the root package, internal/pipeline and internal/video,
#                                           each alone (-part others: every other package)
#   scripts/test.ps1 cover          tests with the 95% per-package coverage gate (-part rest, then
#                                   -part video: internal/video in a run of its own, then the gate)
#   scripts/test.ps1 fuzz -time 30s every fuzz target for 30 s
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')
$task = 'test'
$rest = @($args)
if ($rest.Count -gt 0 -and @('test', 'cover', 'lint', 'fuzz', 'check-tools', 'licenses', 'check-workflows') -contains $rest[0]) {
    $task = $rest[0]
    $rest = @($rest | Select-Object -Skip 1)
}
& go run ./tools/dev $task @rest
exit $LASTEXITCODE

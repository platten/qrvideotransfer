# Tests. The first argument may name a task: test (default), cover, lint, fuzz or check-tools.
#   scripts/test.ps1                unit tests, CGO_ENABLED=0
#   scripts/test.ps1 -race          race detector (enables cgo)
#   scripts/test.ps1 cover          tests with the 95% per-package coverage gate
#   scripts/test.ps1 fuzz -time 30s every fuzz target for 30 s
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')
$task = 'test'
$rest = @($args)
if ($rest.Count -gt 0 -and @('test', 'cover', 'lint', 'fuzz', 'check-tools') -contains $rest[0]) {
    $task = $rest[0]
    $rest = @($rest | Select-Object -Skip 1)
}
& go run ./tools/dev $task @rest
exit $LASTEXITCODE

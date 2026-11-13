# Static build for the host, or all six targets with -all. Flags: see `go run ./tools/dev build -h`.
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')
& go run ./tools/dev build @args
exit $LASTEXITCODE

<#
.SYNOPSIS
Installs or verifies the pinned development tools on Windows. Stands alone: Go may be absent.
Prints everything it will install, and asks, before installing anything.

.DESCRIPTION
  scripts/setup.ps1          Go 1.27.4 and git, then `go run ./tools/dev install-tools`
                             (golangci-lint, GoReleaser, syft, actionlint, go-licenses, govulncheck)
  scripts/setup.ps1 -Spike   also a native ffmpeg with an HEVC encoder (libx265) and the wasmtime
                             command-line runner, which only spike S1 needs
  -Yes                       do not ask
  -DryRun                    print the plan and stop

Go and wasmtime come from their vendors' release archives, checked against the SHA-256 sums pinned
below, and go under %LOCALAPPDATA%\Programs. git and ffmpeg come from winget (Git.Git and
Gyan.FFmpeg, whose full build includes libx265).
#>
param(
    [switch]$Spike,
    [switch]$Yes,
    [switch]$DryRun
)
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')
# Never let an older go on PATH download a toolchain before the plan is printed.
$env:GOTOOLCHAIN = 'local'

$GoVersion = '1.27.4'
$WasmtimeVersion = '50.0.1'
$Prefix = Join-Path $env:LOCALAPPDATA 'Programs'

# SHA-256 of the release archives, from https://go.dev/dl/?mode=json and the GitHub release assets of
# bytecodealliance/wasmtime.
$GoSha256 = @{
    'amd64' = '178ab98950ac69ab25069307dc6ade79f3816e414cce95b4da166a115cde1e7e'
    'arm64' = '9e7a72565792b8ab370b37ac9ac109799237abacb39ff2f541c438b963c0cc64'
}
$WasmtimeSha256 = @{
    'x86_64'  = 'c3646ee2de8cfcefe532e75852e1fd2728d78c3fcc43251794803a698d1c30ca'
    'aarch64' = '2fe15ad13e1ed2f975ff530a335aa6cc8cd63548517321fc9e96ab1dec9db046'
}

function Have($name) { [bool](Get-Command $name -ErrorAction SilentlyContinue) }

$osArch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
switch ($osArch) {
    'X64' { $arch = 'amd64'; $warch = 'x86_64' }
    'Arm64' { $arch = 'arm64'; $warch = 'aarch64' }
    default { throw "setup: unsupported architecture $osArch" }
}

$plan = @()
$winget = @()
$goRoot = Join-Path $Prefix "go$GoVersion"
$goBin = Join-Path $goRoot 'go\bin'
$goInstall = $false
$wasmtimeInstall = $false
$wasmtimeName = "wasmtime-v$WasmtimeVersion-$warch-windows"
$wasmtimeDir = Join-Path $Prefix $wasmtimeName

if ((Have go) -and ((& go env GOVERSION) -eq "go$GoVersion")) {
    Write-Host "ok  Go $GoVersion ($((Get-Command go).Source))"
    $goBin = $null
} elseif ((Test-Path (Join-Path $goBin 'go.exe')) -and ((& (Join-Path $goBin 'go.exe') env GOVERSION) -eq "go$GoVersion")) {
    Write-Host "ok  Go $GoVersion ($goBin, not on PATH)"
} else {
    $goInstall = $true
    $plan += "Go $GoVersion from https://go.dev/dl/go$GoVersion.windows-$arch.zip (SHA-256 checked) into $goRoot"
}

if (Have git) { Write-Host 'ok  git' } else { $winget += 'Git.Git'; $plan += 'git from winget (Git.Git)' }

if ($Spike) {
    if ((Have ffmpeg) -and ((& ffmpeg -hide_banner -encoders 2>$null) -match 'libx265')) {
        Write-Host "ok  ffmpeg with libx265 ($((Get-Command ffmpeg).Source))"
    } else {
        $winget += 'Gyan.FFmpeg'
        $plan += 'ffmpeg (full build, with libx265) from winget (Gyan.FFmpeg)'
    }
    $wasmtimeExe = Join-Path $wasmtimeDir 'wasmtime.exe'
    if ((Have wasmtime) -and ((& wasmtime --version) -match " $WasmtimeVersion")) {
        Write-Host "ok  wasmtime $WasmtimeVersion"
    } elseif ((Test-Path $wasmtimeExe) -and ((& $wasmtimeExe --version) -match " $WasmtimeVersion")) {
        Write-Host "ok  wasmtime $WasmtimeVersion ($wasmtimeDir, not on PATH)"
    } else {
        $wasmtimeInstall = $true
        $plan += "wasmtime $WasmtimeVersion from the GitHub release $wasmtimeName.zip (SHA-256 checked) into $wasmtimeDir"
    }
}
if ($winget.Count -gt 0 -and -not (Have winget)) { throw 'setup: winget is required to install ' + ($winget -join ', ') }

if ($goInstall) {
    $plan += 'then, if missing, the pinned tools of tools/dev: golangci-lint, GoReleaser, syft and actionlint (release archives, SHA-256 checked) and go-licenses and govulncheck (go install)'
} else {
    if ($goBin) { $env:PATH = "$goBin;$env:PATH" }
    $tools = & go run ./tools/dev install-tools -dry-run
    if (($tools -join "`n") -match 'Will install') {
        $plan += ($tools | Select-Object -Skip 1 | ForEach-Object { $_.Trim() })
    } else {
        Write-Host 'ok  pinned tools (golangci-lint, GoReleaser, syft, actionlint, go-licenses, govulncheck)'
    }
}

if ($plan.Count -eq 0) { Write-Host 'Everything is installed.'; exit 0 }
Write-Host 'setup will install:'
$plan | ForEach-Object { Write-Host "  - $_" }
if ($DryRun) { exit 0 }
if (-not $Yes) {
    $answer = Read-Host 'Proceed? [y/N]'
    if ($answer -notmatch '^(y|yes)$') { throw 'setup: nothing installed' }
}

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $tmp | Out-Null
function Fetch($url, $sha, $file) {
    Write-Host "downloading $url"
    Invoke-WebRequest -Uri $url -OutFile $file -UseBasicParsing
    $got = (Get-FileHash -Algorithm SHA256 $file).Hash.ToLowerInvariant()
    if ($got -ne $sha) { throw "setup: checksum mismatch for ${url}: got $got, want $sha" }
}

try {
    foreach ($id in $winget) {
        & winget install --id $id --exact --silent --accept-package-agreements --accept-source-agreements
        if ($LASTEXITCODE -ne 0) { throw "setup: winget install $id failed" }
    }
    if ($goInstall) {
        $zip = Join-Path $tmp 'go.zip'
        Fetch "https://go.dev/dl/go$GoVersion.windows-$arch.zip" $GoSha256[$arch] $zip
        Expand-Archive -Path $zip -DestinationPath $goRoot
        Write-Host "installed Go $GoVersion into $goRoot"
    }
    if ($wasmtimeInstall) {
        $zip = Join-Path $tmp 'wasmtime.zip'
        Fetch "https://github.com/bytecodealliance/wasmtime/releases/download/v$WasmtimeVersion/$wasmtimeName.zip" $WasmtimeSha256[$warch] $zip
        Expand-Archive -Path $zip -DestinationPath $Prefix
        Write-Host "installed wasmtime $WasmtimeVersion into $wasmtimeDir"
    }
} finally {
    Remove-Item -Recurse -Force $tmp
}

# winget changes the machine PATH; pick it up in this session.
$env:PATH = [Environment]::GetEnvironmentVariable('PATH', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('PATH', 'User') + ';' + $env:PATH
if ($goBin) { $env:PATH = "$goBin;$env:PATH" }
if ($Spike -and -not ((& ffmpeg -hide_banner -encoders 2>$null) -match 'libx265')) { throw 'setup: the installed ffmpeg has no libx265 encoder' }

& go run ./tools/dev install-tools
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
& go run ./tools/dev check-tools
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

if ($goBin -or $wasmtimeInstall) {
    Write-Host ''
    Write-Host 'Add to your user PATH (System Properties > Environment Variables):'
    if ($goBin) { Write-Host "  $goBin" }
    if ($wasmtimeInstall) { Write-Host "  $wasmtimeDir" }
    Write-Host "  $(& go env GOPATH)\bin"
}
Write-Host 'setup complete'

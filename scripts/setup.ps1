<#
.SYNOPSIS
Installs or verifies the pinned development tools on Windows. Stands alone: Go may be absent.
Prints everything it will install, and asks, before installing anything.

.DESCRIPTION
  scripts/setup.ps1          Go 1.27.4 and git, then `go run ./tools/dev install-tools`
                             (golangci-lint, GoReleaser, syft, actionlint, go-licenses, govulncheck)
  scripts/setup.ps1 -Spike   also a native ffmpeg with an HEVC encoder (libx265) and the wasmtime
                             command-line runner, which only spike S1 needs
  scripts/setup.ps1 -Cross   also zig, the C cross-compiler of `tools/dev platforms`' race builds
  -Yes                       do not ask
  -DryRun                    print the plan and stop

Go, wasmtime and zig come from their vendors' release archives, checked against the SHA-256 sums pinned
below, and go under %LOCALAPPDATA%\Programs. git and ffmpeg come from winget (Git.Git and
Gyan.FFmpeg, whose full build includes libx265).
#>
param(
    [switch]$Spike,
    [switch]$Cross,
    [switch]$Yes,
    [switch]$DryRun
)
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')
# Never let an older go on PATH download a toolchain before the plan is printed.
$env:GOTOOLCHAIN = 'local'

$GoVersion = '1.27.4'
$WasmtimeVersion = '50.0.1'
$ZigVersion = '0.17.1'
$Prefix = Join-Path $env:LOCALAPPDATA 'Programs'

# SHA-256 of the release archives, from https://go.dev/dl/?mode=json and the GitHub release assets of
# bytecodealliance/wasmtime.
$GoSha256 = @{
    'amd64' = '178ab98950ac69ab25069307dc6ade79f3816e414cce95b4da166a115cde1e7e'
    'arm64' = '9e7a72565792b8ab370b37ac9ac109799237abacb39ff2f541c438b963c0cc64'
}
# From https://ziglang.org/download/index.json.
$ZigSha256 = @{
    'x86_64'  = 'b03f477a1943673e02aca0f570e8030dffd536ce1f45cbc4d3326ec31159562e'
    'aarch64' = '4789895bdb1e312a2ea73b2a7c0a0cc8d2ab62ba6976e834a7c54cf443377c15'
}
$WasmtimeSha256 = @{
    'x86_64'  = 'c3646ee2de8cfcefe532e75852e1fd2728d78c3fcc43251794803a698d1c30ca'
    'aarch64' = '2fe15ad13e1ed2f975ff530a335aa6cc8cd63548517321fc9e96ab1dec9db046'
}

function Have($name) { [bool](Get-Command $name -ErrorAction SilentlyContinue) }

# The limit of each winget install, in seconds: a stalled download otherwise hangs the setup without
# a word. 30 minutes, since a slow mirror that still progresses cannot be told from a stalled one
# by time alone. QRVT_PM_TIMEOUT sets it (0: none).
$PkgTimeout = 1800
if ($env:QRVT_PM_TIMEOUT) {
    if ($env:QRVT_PM_TIMEOUT -notmatch '^[0-9]+$') { throw "setup: QRVT_PM_TIMEOUT must be a number of seconds, not '$env:QRVT_PM_TIMEOUT'" }
    $PkgTimeout = [int]$env:QRVT_PM_TIMEOUT
}

# Runs a program and returns its exit code. Past $Seconds (0: no limit) it stops the program and its
# child processes and throws, saying so.
function Invoke-Bounded([int]$Seconds, [string]$What, [string]$FilePath, [string[]]$Arguments) {
    $p = Start-Process -FilePath $FilePath -ArgumentList $Arguments -NoNewWindow -PassThru
    $null = $p.Handle # keeps the handle, without which ExitCode stays empty
    if ($Seconds -gt 0 -and -not $p.WaitForExit($Seconds * 1000)) {
        try { $p.Kill($true) } catch { $p.Kill() } # the process tree where .NET can (PowerShell 7)
        throw "setup: $What did not finish within $Seconds s (a download may be slow or stalled); run setup again, or set QRVT_PM_TIMEOUT to a longer limit in seconds (0: none)"
    }
    $p.WaitForExit()
    return $p.ExitCode
}

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
$zigInstall = $false
$zigName = "zig-$warch-windows-$ZigVersion"
$zigDir = Join-Path $Prefix $zigName

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
if ($Cross) {
    $zigExe = Join-Path $zigDir 'zig.exe'
    if ((Have zig) -and ((& zig version) -eq $ZigVersion)) {
        Write-Host "ok  zig $ZigVersion"
    } elseif ((Test-Path $zigExe) -and ((& $zigExe version) -eq $ZigVersion)) {
        Write-Host "ok  zig $ZigVersion ($zigDir, not on PATH)"
    } else {
        $zigInstall = $true
        $plan += "zig $ZigVersion from https://ziglang.org/download/$ZigVersion/$zigName.zip (SHA-256 checked) into $zigDir"
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
    Invoke-WebRequest -Uri $url -OutFile $file -UseBasicParsing -TimeoutSec 300
    $got = (Get-FileHash -Algorithm SHA256 $file).Hash.ToLowerInvariant()
    if ($got -ne $sha) { throw "setup: checksum mismatch for ${url}: got $got, want $sha" }
}

try {
    foreach ($id in $winget) {
        $code = Invoke-Bounded $PkgTimeout "winget install $id" (Get-Command winget).Source @('install', '--id', $id, '--exact', '--silent', '--accept-package-agreements', '--accept-source-agreements')
        if ($code -ne 0) { throw "setup: winget install $id failed (exit code $code)" }
    }
    if ($goInstall) {
        $zip = Join-Path $tmp 'go.zip'
        Fetch "https://go.dev/dl/go$GoVersion.windows-$arch.zip" $GoSha256[$arch] $zip
        Expand-Archive -Path $zip -DestinationPath $goRoot
        Write-Host "installed Go $GoVersion into $goRoot"
    }
    if ($zigInstall) {
        $zip = Join-Path $tmp 'zig.zip'
        Fetch "https://ziglang.org/download/$ZigVersion/$zigName.zip" $ZigSha256[$warch] $zip
        Expand-Archive -Path $zip -DestinationPath $Prefix
        Write-Host "installed zig $ZigVersion into $zigDir"
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

if ($goBin -or $wasmtimeInstall -or $zigInstall) {
    Write-Host ''
    Write-Host 'Add to your user PATH (System Properties > Environment Variables):'
    if ($goBin) { Write-Host "  $goBin" }
    if ($wasmtimeInstall) { Write-Host "  $wasmtimeDir" }
    if ($zigInstall) { Write-Host "  $zigDir" }
    Write-Host "  $(& go env GOPATH)\bin"
}
Write-Host 'setup complete'

# deslop-hook installer (Windows).
#
#   irm https://github.com/productmind-code/deslop-hook/releases/latest/download/install.ps1 | iex
#
# Environment:
#   DESLOP_HOOK_VERSION      install this version (e.g. 0.1.0) instead of the latest
#   DESLOP_HOOK_INSTALL_DIR  install here instead of %LOCALAPPDATA%\Programs\deslop-hook
#
# The git hook itself runs under Git for Windows' bundled sh, which finds
# deslop-hook.exe on PATH.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$Repo = 'productmind-code/deslop-hook'
$BaseUrl = "https://github.com/$Repo/releases"

function Fail($msg) {
    Write-Error "deslop-hook install: $msg"
    exit 1
}

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { Fail "unsupported architecture: $($env:PROCESSOR_ARCHITECTURE)" }
}

$version = $env:DESLOP_HOOK_VERSION
if (-not $version) {
    Write-Host 'Resolving the latest release...'
    # Follow the releases/latest redirect instead of the rate-limited API.
    $resp = Invoke-WebRequest -Uri "$BaseUrl/latest" -Method Head -UseBasicParsing
    $final = $resp.BaseResponse.ResponseUri
    if (-not $final) { $final = $resp.BaseResponse.RequestMessage.RequestUri }
    $version = ($final.AbsoluteUri -split '/tag/v')[-1]
}
$version = $version.TrimStart('v')
if (-not $version -or $version.Contains('/')) { Fail "could not work out the latest version; set DESLOP_HOOK_VERSION" }

$archive = "deslop-hook_${version}_windows_${arch}.zip"
$dirUrl = "$BaseUrl/download/v$version"
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("deslop-hook-" + [System.Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Write-Host "Downloading deslop-hook $version (windows/$arch)..."
    Invoke-WebRequest -Uri "$dirUrl/$archive" -OutFile (Join-Path $tmp $archive) -UseBasicParsing
    Invoke-WebRequest -Uri "$dirUrl/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt') -UseBasicParsing

    $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match " $([regex]::Escape($archive))$" } | Select-Object -First 1
    if (-not $line) { Fail "$archive is not listed in checksums.txt" }
    $expected = ($line -split '\s+')[0].ToLower()
    $actual = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $archive)).Hash.ToLower()
    if ($expected -ne $actual) { Fail "checksum mismatch for $archive (expected $expected, got $actual)" }
    Write-Host 'Checksum verified.'

    Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath $tmp -Force

    $dir = $env:DESLOP_HOOK_INSTALL_DIR
    if (-not $dir) { $dir = Join-Path $env:LOCALAPPDATA 'Programs\deslop-hook' }
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    Copy-Item -Force (Join-Path $tmp 'deslop-hook.exe') (Join-Path $dir 'deslop-hook.exe')

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not (($userPath -split ';') -contains $dir)) {
        [Environment]::SetEnvironmentVariable('Path', "$userPath;$dir", 'User')
        Write-Host "Added $dir to your user PATH (open a new terminal to pick it up)."
    }
    Write-Host ""
    Write-Host "deslop-hook $version installed to $dir"
    Write-Host 'Next, in each repository: deslop-hook install'
}
finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

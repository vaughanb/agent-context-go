# agent-context-go installer (Windows).
#
# Downloads the latest prebuilt binary and registers it into every detected AI
# coding agent.
#
#   irm https://raw.githubusercontent.com/vaughanb/agent-context-go/main/install.ps1 | iex
#
# Environment overrides:
#   $env:ACG_VERSION      release tag to install (default: latest)
#   $env:ACG_INSTALL_DIR  install location (default: %LOCALAPPDATA%\Programs\agent-context-go)
#   $env:ACG_NO_CONFIGURE set to skip auto-configuring agents
$ErrorActionPreference = 'Stop'

# Windows PowerShell 5.1 may default to TLS 1.0/1.1, which GitHub rejects.
try {
    [Net.ServicePointManager]::SecurityProtocol =
        [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {}

$repo = 'vaughanb/agent-context-go'
$bin = 'agent-context-go'

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$asset = "${bin}_windows_${arch}.exe"

$version = if ($env:ACG_VERSION) { $env:ACG_VERSION } else { 'latest' }
$base = if ($version -eq 'latest') {
    "https://github.com/$repo/releases/latest/download"
} else {
    "https://github.com/$repo/releases/download/$version"
}

$installDir = if ($env:ACG_INSTALL_DIR) { $env:ACG_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\agent-context-go" }
New-Item -ItemType Directory -Force -Path $installDir | Out-Null
$dest = Join-Path $installDir "$bin.exe"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
try {
    Write-Host "Downloading $asset ($version)..."
    $binTmp = Join-Path $tmp $asset
    try {
        Invoke-WebRequest -Uri "$base/$asset" -OutFile $binTmp -UseBasicParsing
    } catch {
        throw "Could not download $asset. The release may still be building, or no " +
              "prebuilt binary exists for this platform/version yet. Check " +
              "https://github.com/$repo/releases and try again in a few minutes. " +
              "($($_.Exception.Message))"
    }

    # Verify checksum when the release publishes one. Downloading the sums file
    # is best-effort; a mismatch, once we have the file, is fatal.
    $sumsTmp = Join-Path $tmp 'SHA256SUMS'
    $haveSums = $true
    try {
        Invoke-WebRequest -Uri "$base/SHA256SUMS" -OutFile $sumsTmp -UseBasicParsing
    } catch {
        $haveSums = $false
    }
    if ($haveSums) {
        $line = (Get-Content $sumsTmp | Where-Object { $_ -match [regex]::Escape($asset) + '$' } | Select-Object -First 1)
        if ($line) {
            $want = ($line -split '\s+')[0]
            $got = (Get-FileHash -Algorithm SHA256 $binTmp).Hash.ToLower()
            if ($want.ToLower() -ne $got) { throw "checksum mismatch for $asset" }
            Write-Host "Checksum verified."
        }
    }

    Move-Item -Force $binTmp $dest
    Write-Host "Installed to $dest"
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

# Add the install dir to the user PATH if it is not already there.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not ($userPath -split ';' | Where-Object { $_ -eq $installDir })) {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$installDir", 'User')
    Write-Host "Added $installDir to your PATH (restart the terminal to pick it up)."
}

if (-not $env:ACG_NO_CONFIGURE) {
    Write-Host ""
    & $dest install
} else {
    Write-Host "Skipping agent configuration (ACG_NO_CONFIGURE set). Run: $bin install"
}

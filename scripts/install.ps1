# auxly-cli installer (Windows / PowerShell)
# Usage:  irm https://get.auxly.io/cli.ps1 | iex
#
# Installs the latest auxly.exe to %LOCALAPPDATA%\Programs\auxly and adds it to
# the per-user PATH. Mirrors scripts/install.sh (the macOS/Linux installer).

$ErrorActionPreference = 'Stop'

$Repo    = 'Tzamun-Arabia-IT-Co/auxly-cli'
$Binary  = 'auxly.exe'

# --- Detect architecture -----------------------------------------------------
# PROCESSOR_ARCHITECTURE reports the *process* arch; on ARM64 Windows a 32-bit
# host sets PROCESSOR_ARCHITEW6432, so prefer it when present.
$rawArch = $env:PROCESSOR_ARCHITEW6432
if (-not $rawArch) { $rawArch = $env:PROCESSOR_ARCHITECTURE }

switch ($rawArch) {
    'AMD64' { $goarch = 'amd64' }
    'ARM64' { $goarch = 'arm64' }
    default { Write-Error "Unsupported architecture: $rawArch"; exit 1 }
}

Write-Host "Detecting system: windows/$goarch"

$url = "https://github.com/$Repo/releases/latest/download/auxly-cli_windows_$goarch.zip"
Write-Host "Downloading from: $url"

# --- Download + extract ------------------------------------------------------
$tmp = Join-Path $env:TEMP ("auxly-" + [System.Guid]::NewGuid().ToString())
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
$zip = Join-Path $tmp 'auxly-cli.zip'

try {
    Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing
    Expand-Archive -Path $zip -DestinationPath $tmp -Force

    $src = Join-Path $tmp $Binary
    if (-not (Test-Path $src)) {
        Write-Error "Archive did not contain $Binary"
        exit 1
    }

    # --- Install -------------------------------------------------------------
    $installDir = Join-Path $env:LOCALAPPDATA 'Programs\auxly'
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    Copy-Item -Force $src (Join-Path $installDir $Binary)

    # --- Add to per-user PATH (idempotent) -----------------------------------
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not $userPath) { $userPath = '' }
    $onPath = $userPath.Split(';') | Where-Object { $_ -eq $installDir }
    if (-not $onPath) {
        $newPath = if ($userPath.TrimEnd(';')) { $userPath.TrimEnd(';') + ';' + $installDir } else { $installDir }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        $env:Path += ";$installDir"
        Write-Host "Added $installDir to your user PATH (restart open terminals to pick it up)."
    }
}
finally {
    if (Test-Path $tmp) { Remove-Item -Recurse -Force $tmp }
}

Write-Host ''
Write-Host 'auxly-cli installed successfully!'
Write-Host "   Location: $installDir\$Binary"
Write-Host ''
& (Join-Path $installDir $Binary) --version

Write-Host ''
Write-Host 'Get started:'
Write-Host '   auxly init     # Initialize memory folder'
Write-Host '   auxly ui       # Launch TUI dashboard'
Write-Host ''

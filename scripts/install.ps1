# auxly-cli installer (Windows / PowerShell)
# Usage:  irm https://auxly.io/cli.ps1 | iex
#
# Downloads the matching static auxly.exe and installs it to
# %LOCALAPPDATA%\Programs\auxly, then adds it to the per-user PATH.
# Mirrors scripts/install.sh (the macOS/Linux installer).

$ErrorActionPreference = 'Stop'

$BaseUrl = if ($env:AUXLY_INSTALL_BASE) { $env:AUXLY_INSTALL_BASE } else { 'https://auxly.io' }
# M4: never let an inherited/poisoned AUXLY_INSTALL_BASE downgrade the download to
# http. Accept https, or http on an EXACT loopback host (dev), or explicit opt-in.
# The hostname is parsed (not prefix-matched) so http://localhost.evil.example is
# rejected.
$secureBase = $false
if ($env:AUXLY_INSECURE_INSTALL -eq '1') {
    $secureBase = $true
} else {
    try {
        $u = [Uri]$BaseUrl
        if ($u.Scheme -eq 'https') { $secureBase = $true }
        elseif ($u.Scheme -eq 'http' -and @('localhost','127.0.0.1','::1') -contains $u.Host) { $secureBase = $true }
    } catch { $secureBase = $false }
}
if (-not $secureBase) {
    Write-Warning "Refusing insecure AUXLY_INSTALL_BASE ($BaseUrl); using https://auxly.io"
    $BaseUrl = 'https://auxly.io'
}
$Binary  = 'auxly.exe'

# --- Detect architecture ------------------------------------------------------
$arch = $env:PROCESSOR_ARCHITECTURE
if ($env:PROCESSOR_ARCHITEW6432) { $arch = $env:PROCESSOR_ARCHITEW6432 }
switch ($arch) {
  'AMD64' { $goarch = 'amd64' }
  'ARM64' { $goarch = 'arm64' }
  default { Write-Error "Unsupported architecture: $arch"; exit 1 }
}

$url = "$BaseUrl/dl/auxly-windows-$goarch.exe"
Write-Host "Detected windows/$goarch"
Write-Host "Downloading $url"

# --- Install location ---------------------------------------------------------
$installDir = Join-Path $env:LOCALAPPDATA 'Programs\auxly'
New-Item -ItemType Directory -Force -Path $installDir | Out-Null
$dest = Join-Path $installDir $Binary

# Download to a temp file first, then swap it into place. Writing straight over
# $dest fails when auxly.exe is currently RUNNING (a live MCP session on the box
# locks it) — that's the "update failed / exit status 1" on Windows. Windows lets
# a running .exe be RENAMED out of the way, so we move the old binary aside and
# drop the new one in its place; the still-running process keeps using the renamed
# file and the next launch picks up the new binary.
$tmp = Join-Path $installDir 'auxly.exe.new'
Invoke-WebRequest -Uri $url -OutFile $tmp -UseBasicParsing

# --- Verify against the signed checksum manifest (H3, staged) ------------------
# Pinned minisign public key (matches internal/update/verify.go). Not a secret.
# STAGED ROLLOUT: a release published before signing existed has NO manifest — the
# manifest fetch fails, verification is skipped, and the binary installs unverified
# so the existing distribution keeps working. Once a manifest IS present (signing is
# live), verification is MANDATORY and fails CLOSED.
#
# IMPORTANT: only the *manifest fetch* is wrapped in a try/catch (that's the legit
# "no manifest yet" staged case). Every in-manifest failure — checksum mismatch,
# missing .minisig while minisign is installed, bad signature — runs OUTSIDE that
# catch and aborts hard. (A previous version put these inside the catch; under
# $ErrorActionPreference='Stop', Write-Error threw before `exit 1` and the catch
# silently swallowed the abort, letting a tampered binary install.)
$MinisignPubKey = 'RWQfIGHWpXR4MtPvcbWwN1J7mx9FGsCaHMmdIpGMZAKDvmILC2Of5Q/K'
try {
    $verRaw  = (Invoke-WebRequest -Uri "$BaseUrl/version" -UseBasicParsing -ErrorAction Stop).Content
    $version = ($verRaw -replace '[^0-9A-Za-z.\-]', '')
} catch { $version = '' }
if ($version) {
    $manifestUrl = "$BaseUrl/dl/auxly-$version-checksums.txt"
    $sumsPath = "$tmp.sums"
    $haveManifest = $false
    try {
        Invoke-WebRequest -Uri $manifestUrl -OutFile $sumsPath -UseBasicParsing -ErrorAction Stop
        # A CDN missing the asset may answer 200 with an SPA/HTML page rather than a
        # 404. Only treat it as a real manifest if it has a checksum-shaped line, so
        # we staged-skip junk instead of fail-closing a legitimate install.
        if ((Get-Content -LiteralPath $sumsPath -Raw) -match '(?m)^[0-9a-fA-F]{64}\s') {
            $haveManifest = $true
        }
    } catch {
        # Manifest absent (pre-signing release) — staged: install unverified.
    }
    if ($haveManifest) {
        # Full first-field match (mirrors manifestHasHash in verify.go); a substring
        # regex would pass a manifest that merely CONTAINS the hash anywhere on a line.
        $hash = (Get-FileHash -LiteralPath $tmp -Algorithm SHA256).Hash.ToLower()
        $match = $false
        foreach ($line in (Get-Content -LiteralPath $sumsPath)) {
            $field = (($line -split '\s+') | Where-Object { $_ -ne '' } | Select-Object -First 1)
            if ($field -and $field.ToLower() -eq $hash) { $match = $true; break }
        }
        if (-not $match) {
            Remove-Item -LiteralPath $tmp, $sumsPath -Force -ErrorAction SilentlyContinue
            Write-Error "Checksum mismatch - refusing to install."; exit 1
        }
        if (Get-Command minisign -ErrorAction SilentlyContinue) {
            $sigPath = "$tmp.sig"
            $haveSig = $false
            try {
                Invoke-WebRequest -Uri "$manifestUrl.minisig" -OutFile $sigPath -UseBasicParsing -ErrorAction Stop
                $haveSig = $true
            } catch {
                # A dropped .minisig must NOT downgrade a host that can verify to
                # checksum-only — fail closed.
            }
            if (-not $haveSig) {
                Remove-Item -LiteralPath $tmp, $sumsPath -Force -ErrorAction SilentlyContinue
                Write-Error "Signature missing for a signed release but minisign is installed - refusing to install."; exit 1
            }
            & minisign -Vm $sumsPath -x $sigPath -P $MinisignPubKey | Out-Null
            if ($LASTEXITCODE -ne 0) {
                Remove-Item -LiteralPath $tmp, $sumsPath, $sigPath -Force -ErrorAction SilentlyContinue
                Write-Error "Signature verification failed - refusing to install."; exit 1
            }
            Write-Host "Signature verified"
            Remove-Item -LiteralPath $sigPath -Force -ErrorAction SilentlyContinue
        }
        elseif ($env:AUXLY_REQUIRE_SIGNATURE -eq '1') {
            # Strict mode requested but minisign isn't installed — checksum-only is
            # not enough under AUXLY_REQUIRE_SIGNATURE.
            Remove-Item -LiteralPath $tmp, $sumsPath -Force -ErrorAction SilentlyContinue
            Write-Error "AUXLY_REQUIRE_SIGNATURE=1 but minisign is not installed - cannot verify the signature. Install minisign and retry, or unset the variable."; exit 1
        }
        Remove-Item -LiteralPath $sumsPath -Force -ErrorAction SilentlyContinue
    }
}

try {
  if (Test-Path -LiteralPath $dest) {
    $old = Join-Path $installDir ('auxly.exe.old-' + [Guid]::NewGuid().ToString('N').Substring(0,8))
    Move-Item -LiteralPath $dest -Destination $old -Force
  }
  Move-Item -LiteralPath $tmp -Destination $dest -Force
} catch {
  Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue
  Write-Error "Failed to install auxly.exe (close any agent using it, then retry): $_"
  exit 1
}
# Best-effort sweep of superseded binaries that are no longer locked.
Get-ChildItem -LiteralPath $installDir -Filter 'auxly.exe.old-*' -ErrorAction SilentlyContinue |
  ForEach-Object { Remove-Item -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue }

# --- Add to per-user PATH (persisted, for new terminals) ----------------------
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -notlike "*$installDir*") {
  [Environment]::SetEnvironmentVariable('Path', "$userPath;$installDir", 'User')
  Write-Host "Added $installDir to your PATH."
}

# Also update THIS session's PATH so `auxly` works immediately — the persisted
# PATH above only reaches NEW terminals, but `irm | iex` runs in your current
# session, so we make it usable right now without a restart.
if (($env:Path -split ';') -notcontains $installDir) {
  $env:Path = "$env:Path;$installDir"
}

Write-Host ""
Write-Host "auxly installed: $dest"
& $dest --version
Write-Host ""
Write-Host "Ready now in this terminal — run 'auxly' to get started. New terminals work too."

# --- Optional self-provisioning (env-driven, since `irm | iex` can't pass args) ---
# Set AUXLY_SETUP=1 for local MCP+skills, AUXLY_CONNECT=1 to wire to an advertised host:
#   $env:AUXLY_CONNECT=1; irm https://auxly.io/cli.ps1 | iex
if ($env:AUXLY_SETUP -eq '1') {
  Write-Host "`nRunning local setup (MCP + skills)..."
  & $dest setup
}
if ($env:AUXLY_CONNECT -eq '1') {
  Write-Host "`nWiring this machine to an advertised memory host..."
  & $dest connect auto
}

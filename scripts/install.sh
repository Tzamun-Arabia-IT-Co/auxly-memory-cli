#!/bin/sh
# auxly-cli installer (POSIX sh)
# Usage: curl -sSL https://auxly.io/cli | sh
#
# Downloads the matching static auxly binary and installs it on PATH.
# The binaries are CGO-free single files (no archive, no extraction needed).

set -eu

BASE_URL="${AUXLY_INSTALL_BASE:-https://auxly.io}"
# M4: never let an inherited/poisoned AUXLY_INSTALL_BASE downgrade the download to
# http. Accept https, or http on localhost (dev), or an explicit insecure opt-in.
# Patterns are delimiter-anchored so http://localhost.evil.example does NOT match
# (only exact loopback host, optionally with :port or /path).
case "$BASE_URL" in
  https://*) : ;;
  http://localhost|http://localhost:*|http://localhost/*) : ;;
  http://127.0.0.1|http://127.0.0.1:*|http://127.0.0.1/*) : ;;
  *)
    if [ "${AUXLY_INSECURE_INSTALL:-}" != "1" ]; then
      echo "Refusing insecure AUXLY_INSTALL_BASE ($BASE_URL); using https://auxly.io" >&2
      BASE_URL="https://auxly.io"
    fi
    ;;
esac
BINARY="auxly"

# --- Parse args ---------------------------------------------------------------
# --connect : after install, run `auxly connect auto` to wire this box to a
#             memory host advertised here (the one-command remote bootstrap).
# --setup   : after install, run `auxly setup` (local MCP + skills).
DO_CONNECT=0
DO_SETUP=0
for arg in "$@"; do
  case "$arg" in
    --connect) DO_CONNECT=1 ;;
    --setup)   DO_SETUP=1 ;;
  esac
done

# --- Detect OS / architecture -------------------------------------------------
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

case "$OS" in
  linux)  OS="linux" ;;
  darwin) OS="darwin" ;;
  *) echo "Unsupported OS: $OS (use the PowerShell installer on Windows: irm ${BASE_URL}/cli.ps1 | iex)" >&2; exit 1 ;;
esac

URL="${BASE_URL}/dl/auxly-${OS}-${ARCH}"
echo "🔍 Detected ${OS}/${ARCH}"
echo "📥 Downloading ${URL}"

# --- Download to a temp file --------------------------------------------------
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
if ! curl -fSL "$URL" -o "$TMP"; then
  echo "✗ Download failed: ${URL}" >&2
  exit 1
fi

# --- Verify against the signed checksum manifest (H3, staged) ------------------
# Pinned minisign public key (matches internal/update/verify.go). Not a secret.
# STAGED ROLLOUT: a release published before signing existed has NO manifest —
# the manifest fetch 404s, the whole block is skipped, and the binary installs
# unverified so the existing distribution keeps working. But the moment a manifest
# IS present (signing is live for this release), verification becomes MANDATORY and
# fails CLOSED:
#   • no SHA-256 tool on the box        -> refuse (can't verify a signed release)
#   • computed hash not in the manifest -> refuse (first-field match, not substring)
#   • minisign installed but .minisig   -> refuse (don't let a dropped sig downgrade
#     missing or not verifying              a host that CAN verify to checksum-only)
# The remaining "drop the whole manifest to force the staged skip" downgrade is the
# known, accepted cost of staging; it closes when the self-updater/installers flip
# to default-deny once signing infra is live (see internal/update/verify.go).
MINISIGN_PUBKEY="RWQfIGHWpXR4MtPvcbWwN1J7mx9FGsCaHMmdIpGMZAKDvmILC2Of5Q/K"
VERSION="$(curl -fsSL "${BASE_URL}/version" 2>/dev/null | tr -d 'v \r\n\t')"
if [ -n "$VERSION" ]; then
  MANIFEST_URL="${BASE_URL}/dl/auxly-${VERSION}-checksums.txt"
  SUMS="${TMP}.sums"; SIG="${TMP}.sig"
  if curl -fsSL "$MANIFEST_URL" -o "$SUMS" 2>/dev/null && grep -qiE '^[0-9a-f]{64}[[:space:]]' "$SUMS"; then
    # Manifest present AND looks like a real checksums file => signed release.
    # (A CDN missing the asset may answer 200 with an SPA/HTML page rather than a
    # 404 — the grep guard treats that junk as "absent" so we staged-skip instead
    # of fail-closing.) Integrity is now required, not best-effort.
    SUM=""
    if command -v sha256sum >/dev/null 2>&1; then SUM="$(sha256sum "$TMP" | awk '{print $1}')";
    elif command -v shasum  >/dev/null 2>&1; then SUM="$(shasum -a 256 "$TMP" | awk '{print $1}')"; fi
    if [ -z "$SUM" ]; then
      echo "✗ No SHA-256 tool (sha256sum/shasum) to verify the signed release — refusing to install." >&2
      echo "  Install coreutils (Linux) or ensure shasum is on PATH, then retry." >&2
      rm -f "$SUMS"; exit 1
    fi
    # Full first-field match (mirrors manifestHasHash in verify.go); a substring
    # grep would pass a manifest that merely CONTAINS the hash anywhere on a line.
    if ! awk -v s="$SUM" 'tolower($1)==tolower(s){found=1} END{exit found?0:1}' "$SUMS"; then
      echo "✗ Checksum mismatch — refusing to install." >&2; rm -f "$SUMS"; exit 1
    fi
    if command -v minisign >/dev/null 2>&1; then
      if ! curl -fsSL "${MANIFEST_URL}.minisig" -o "$SIG" 2>/dev/null; then
        echo "✗ Signature missing for a signed release but minisign is installed — refusing to install." >&2
        rm -f "$SUMS" "$SIG"; exit 1
      fi
      if ! minisign -Vm "$SUMS" -x "$SIG" -P "$MINISIGN_PUBKEY" >/dev/null 2>&1; then
        echo "✗ Signature verification failed — refusing to install." >&2; rm -f "$SUMS" "$SIG"; exit 1
      fi
      echo "🔒 Signature verified"
    elif [ "${AUXLY_REQUIRE_SIGNATURE:-}" = "1" ]; then
      # Strict mode requested, but we can't verify the minisign signature without
      # the minisign binary. Checksum-only is not enough under AUXLY_REQUIRE_SIGNATURE.
      echo "✗ AUXLY_REQUIRE_SIGNATURE=1 but minisign is not installed — cannot verify the signature." >&2
      echo "  Install minisign (e.g. 'brew install minisign') and retry, or unset AUXLY_REQUIRE_SIGNATURE." >&2
      rm -f "$SUMS"; exit 1
    fi
    rm -f "$SUMS" "$SIG"
  fi
fi

chmod +x "$TMP"

# --- Pick a writable install dir on PATH --------------------------------------
# Prefer /usr/local/bin; fall back to ~/.local/bin without requiring sudo.
INSTALL_DIR="/usr/local/bin"
if [ ! -w "$INSTALL_DIR" ]; then
  if command -v sudo >/dev/null 2>&1 && [ -t 0 ]; then
    echo "📦 Installing to ${INSTALL_DIR} (sudo)..."
    sudo mv "$TMP" "${INSTALL_DIR}/${BINARY}"
    trap - EXIT
  else
    INSTALL_DIR="${HOME}/.local/bin"
    mkdir -p "$INSTALL_DIR"
    mv "$TMP" "${INSTALL_DIR}/${BINARY}"
    trap - EXIT
    case ":${PATH}:" in
      *":${INSTALL_DIR}:"*) ;;
      *) echo "⚠ Add ${INSTALL_DIR} to your PATH:  export PATH=\"${INSTALL_DIR}:\$PATH\"" ;;
    esac
  fi
else
  mv "$TMP" "${INSTALL_DIR}/${BINARY}"
  trap - EXIT
fi

echo ""
echo "✅ auxly installed: ${INSTALL_DIR}/${BINARY}"
BIN="${INSTALL_DIR}/${BINARY}"
"$BIN" --version 2>/dev/null || true

# --- Optional self-provisioning ----------------------------------------------
if [ "$DO_SETUP" = "1" ]; then
  echo ""
  echo "🔧 Running local setup (MCP + skills)..."
  "$BIN" setup || true
fi
if [ "$DO_CONNECT" = "1" ]; then
  echo ""
  echo "🔗 Wiring this machine to an advertised memory host..."
  "$BIN" connect auto || true
fi

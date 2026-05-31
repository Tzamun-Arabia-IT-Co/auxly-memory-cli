#!/bin/sh
# auxly-cli installer (POSIX sh)
# Usage: curl -sSL https://auxly.io/cli | sh
#
# Downloads the matching static auxly binary and installs it on PATH.
# The binaries are CGO-free single files (no archive, no extraction needed).

set -eu

BASE_URL="${AUXLY_INSTALL_BASE:-https://auxly.io}"
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

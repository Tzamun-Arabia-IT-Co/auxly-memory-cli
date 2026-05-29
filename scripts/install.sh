#!/bin/bash
# auxly-cli installer
# Usage: curl -sSL https://get.auxly.io/cli | bash
#
# Installs the latest auxly-cli binary to /usr/local/bin

set -e

REPO="Tzamun-Arabia-IT-Co/auxly-cli"
INSTALL_DIR="/usr/local/bin"
BINARY="auxly"

# Detect OS and architecture
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)             echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

case "$OS" in
  linux)  OS="linux" ;;
  darwin) OS="darwin" ;;
  *)      echo "Unsupported OS: $OS"; exit 1 ;;
esac

echo "🔍 Detecting system: ${OS}/${ARCH}"

# Get latest release URL
LATEST_URL="https://github.com/${REPO}/releases/latest/download/auxly-cli_${OS}_${ARCH}.tar.gz"
echo "📥 Downloading from: ${LATEST_URL}"

# Download and extract
TMP_DIR=$(mktemp -d)
curl -sSL "$LATEST_URL" -o "${TMP_DIR}/auxly-cli.tar.gz"
tar -xzf "${TMP_DIR}/auxly-cli.tar.gz" -C "${TMP_DIR}"

# Install
if [ -w "$INSTALL_DIR" ]; then
  mv "${TMP_DIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
else
  echo "📦 Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo mv "${TMP_DIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
fi

chmod +x "${INSTALL_DIR}/${BINARY}"
rm -rf "$TMP_DIR"

echo ""
echo "✅ auxly-cli installed successfully!"
echo "   Location: ${INSTALL_DIR}/${BINARY}"
echo ""
echo "🚀 Get started:"
echo "   auxly init     # Initialize memory folder"
echo "   auxly list     # List memory files"
echo "   auxly ui       # Launch TUI dashboard"
echo ""

#!/usr/bin/env sh
# Generate Windows PE version-info resources (.syso) so the released auxly.exe
# carries proper publisher/product metadata (CompanyName, ProductName, version,
# copyright). An UNSIGNED binary that still has version-info scores far lower on
# Windows Defender / SmartScreen heuristics than an anonymous one — this is the
# free first step toward stopping the false-positive "virus" detections, ahead
# of Authenticode code signing.
#
# Go automatically links a `*_windows_<arch>.syso` file in the main package dir
# into the matching GOOS=windows build, so the resources just need to exist at
# repo root before `goreleaser`/`go build` runs.
#
# Called from goreleaser before.hooks with the release version:
#   sh scripts/gen-win-versioninfo.sh {{ .Version }}
# Falls back to the VERSION file when no argument is given (local source builds).
set -eu

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
	VERSION="$(cat VERSION 2>/dev/null || echo 0.0.0)"
fi

# Normalize: drop a leading "v" and any -prerelease/+build suffix → MAJOR.MINOR.PATCH
CLEAN="$(printf '%s' "$VERSION" | sed 's/^v//; s/[-+].*$//')"
MAJOR="$(printf '%s' "$CLEAN" | cut -d. -f1)"; MAJOR="${MAJOR:-0}"
MINOR="$(printf '%s' "$CLEAN" | cut -d. -f2)"; MINOR="${MINOR:-0}"
PATCH="$(printf '%s' "$CLEAN" | cut -d. -f3)"; PATCH="${PATCH:-0}"

TMPL="scripts/windows/versioninfo.json.tmpl"
GEN="versioninfo.generated.json"
if [ ! -f "$TMPL" ]; then
	echo "gen-win-versioninfo: template not found: $TMPL" >&2
	exit 1
fi

sed -e "s/@MAJOR@/${MAJOR}/g" \
    -e "s/@MINOR@/${MINOR}/g" \
    -e "s/@PATCH@/${PATCH}/g" \
    -e "s/@VERSION@/${CLEAN}/g" \
    "$TMPL" > "$GEN"

# Pinned so CI is reproducible. Runs cross-platform (pure Go).
GVI="github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.5.0"

# amd64 and arm64 resources. `-arm -64` => ARM64.
go run "$GVI" -64      -o resource_windows_amd64.syso "$GEN"
go run "$GVI" -arm -64 -o resource_windows_arm64.syso "$GEN"

echo "gen-win-versioninfo: wrote resource_windows_{amd64,arm64}.syso for v${CLEAN}"

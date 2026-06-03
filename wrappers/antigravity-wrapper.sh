#!/bin/bash
# Antigravity Agent Wrapper for auxly-cli
#
# Usage:
#   auxly-antigravity-write <file> "<diff>" "<reason>"
#
# Environment:
#   AUXLY_AGENT_ID   - Unique session/agent ID (default: antigravity-$$)
#   AUXLY_MEMORY_PATH - Override memory path (optional)

AGENT_ID="${AUXLY_AGENT_ID:-antigravity-$$}"
PROVIDER="antigravity"

auxly-antigravity-read() {
  local file="${1:?Usage: auxly-antigravity-read <file>}"
  auxly view "$file"
}

auxly-antigravity-write() {
  local file="${1:?Usage: auxly-antigravity-write <file> <diff> <reason>}"
  local diff="${2:?Missing diff}"
  local reason="${3:?Missing reason}"

  auxly write \
    --agent "$AGENT_ID" \
    --provider "$PROVIDER" \
    --file "$file" \
    --diff "$diff" \
    --reason "$reason"
}

auxly-antigravity-search() {
  local query="${1:?Usage: auxly-antigravity-search <query>}"
  auxly search "$query"
}

export -f auxly-antigravity-read 2>/dev/null
export -f auxly-antigravity-write 2>/dev/null
export -f auxly-antigravity-search 2>/dev/null

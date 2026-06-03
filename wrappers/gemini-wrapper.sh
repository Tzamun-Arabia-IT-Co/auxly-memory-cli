#!/bin/bash
# Gemini Agent Wrapper for auxly-cli
#
# Usage:
#   auxly-gemini-write <file> "<diff>" "<reason>"
#
# Environment:
#   AUXLY_AGENT_ID   - Unique session/agent ID (default: gemini-$$)
#   AUXLY_MEMORY_PATH - Override memory path (optional)

AGENT_ID="${AUXLY_AGENT_ID:-gemini-$$}"
PROVIDER="gemini"

auxly-gemini-read() {
  local file="${1:?Usage: auxly-gemini-read <file>}"
  auxly view "$file"
}

auxly-gemini-write() {
  local file="${1:?Usage: auxly-gemini-write <file> <diff> <reason>}"
  local diff="${2:?Missing diff}"
  local reason="${3:?Missing reason}"

  auxly write \
    --agent "$AGENT_ID" \
    --provider "$PROVIDER" \
    --file "$file" \
    --diff "$diff" \
    --reason "$reason"
}

auxly-gemini-search() {
  local query="${1:?Usage: auxly-gemini-search <query>}"
  auxly search "$query"
}

export -f auxly-gemini-read 2>/dev/null
export -f auxly-gemini-write 2>/dev/null
export -f auxly-gemini-search 2>/dev/null

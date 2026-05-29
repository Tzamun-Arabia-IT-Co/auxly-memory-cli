#!/bin/bash
# Copilot Agent Wrapper for auxly-cli
#
# Usage:
#   auxly-copilot-write <file> "<diff>" "<reason>"
#
# Environment:
#   AUXLY_AGENT_ID   - Unique session/agent ID (default: copilot-$$)
#   AUXLY_MEMORY_PATH - Override memory path (optional)

AGENT_ID="${AUXLY_AGENT_ID:-copilot-$$}"
PROVIDER="copilot"

auxly-copilot-read() {
  local file="${1:?Usage: auxly-copilot-read <file>}"
  auxly view "$file"
}

auxly-copilot-write() {
  local file="${1:?Usage: auxly-copilot-write <file> <diff> <reason>}"
  local diff="${2:?Missing diff}"
  local reason="${3:?Missing reason}"

  auxly write \
    --agent "$AGENT_ID" \
    --provider "$PROVIDER" \
    --file "$file" \
    --diff "$diff" \
    --reason "$reason"
}

auxly-copilot-search() {
  local query="${1:?Usage: auxly-copilot-search <query>}"
  auxly search "$query"
}

export -f auxly-copilot-read 2>/dev/null
export -f auxly-copilot-write 2>/dev/null
export -f auxly-copilot-search 2>/dev/null

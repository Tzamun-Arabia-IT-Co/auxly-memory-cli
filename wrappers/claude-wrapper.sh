#!/bin/bash
# Claude CLI Agent Wrapper for auxly-cli
# Source this file or call it from your Claude agent hook.
#
# Usage:
#   auxly-claude-write <file> "<diff>" "<reason>"
#
# Environment:
#   AUXLY_AGENT_ID   - Unique session/agent ID (default: claude-$$)
#   AUXLY_MEMORY_PATH - Override memory path (optional)

AGENT_ID="${AUXLY_AGENT_ID:-claude-$$}"
PROVIDER="claude"

auxly-claude-read() {
  local file="${1:?Usage: auxly-claude-read <file>}"
  auxly view "$file"
}

auxly-claude-write() {
  local file="${1:?Usage: auxly-claude-write <file> <diff> <reason>}"
  local diff="${2:?Missing diff}"
  local reason="${3:?Missing reason}"

  auxly write \
    --agent "$AGENT_ID" \
    --provider "$PROVIDER" \
    --file "$file" \
    --diff "$diff" \
    --reason "$reason"
}

auxly-claude-search() {
  local query="${1:?Usage: auxly-claude-search <query>}"
  auxly search "$query"
}

# Export functions for use in subshells
export -f auxly-claude-read 2>/dev/null
export -f auxly-claude-write 2>/dev/null
export -f auxly-claude-search 2>/dev/null

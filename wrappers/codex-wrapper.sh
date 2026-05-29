#!/bin/bash
# Codex (ChatGPT/OpenAI) Agent Wrapper for auxly-cli
#
# Usage:
#   auxly-codex-write <file> "<diff>" "<reason>"
#
# Environment:
#   AUXLY_AGENT_ID   - Unique session/agent ID (default: codex-$$)
#   AUXLY_MEMORY_PATH - Override memory path (optional)

AGENT_ID="${AUXLY_AGENT_ID:-codex-$$}"
PROVIDER="codex"

auxly-codex-read() {
  local file="${1:?Usage: auxly-codex-read <file>}"
  auxly view "$file"
}

auxly-codex-write() {
  local file="${1:?Usage: auxly-codex-write <file> <diff> <reason>}"
  local diff="${2:?Missing diff}"
  local reason="${3:?Missing reason}"

  auxly write \
    --agent "$AGENT_ID" \
    --provider "$PROVIDER" \
    --file "$file" \
    --diff "$diff" \
    --reason "$reason"
}

auxly-codex-search() {
  local query="${1:?Usage: auxly-codex-search <query>}"
  auxly search "$query"
}

export -f auxly-codex-read 2>/dev/null
export -f auxly-codex-write 2>/dev/null
export -f auxly-codex-search 2>/dev/null

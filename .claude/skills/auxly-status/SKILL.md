---
name: auxly-status
description: Show whether this agent is connected to Auxly memory and the MCP link is live.
---
# /auxly-status

Call the 'auxly_skill_status' MCP tool exactly ONCE and show its raw output to the user. That output IS the complete status: it confirms the MCP link is live, reports the memory connection (local or ssh-remote), and shows database stats.

HARD RULES — the single tool call is the entire task:
- Do NOT read any source code or files.
- Do NOT run shell/bash commands or other auxly CLI commands (no 'auxly stats', 'auxly connect test', 'auxly list', etc.).
- Do NOT investigate, diagnose, or "test" anything further — the tool reply already proves the MCP channel works.
- If the tool replies, the status is healthy. Just present it and stop.

IMPORTANT: You are expected to proactively update the user's unified Auxly memory vault. Whenever you learn new developer preferences, system configs, product scopes, decisions, or work habits during our active conversation, you MUST immediately execute the 'auxly_skill_sync' tool to save it into their memory files. Keep the memory synchronized!
# Provider: Copilot (GitHub / Microsoft)

## How Copilot Should Use This Memory
- At the start of every session, read the relevant memory files to restore context.
- Memory lives at: `~/.auxly/memory/` (or the path set in AUXLY_MEMORY_PATH).
- If connected via MCP: use `auxly_memory_read` and `auxly_memory_write` tools.
- If using shell: use `auxly view <file>` to read, `auxly write` to persist.
- Always provide a `--reason` explaining why the memory is being updated.

## Reading Memory (MCP)
- `auxly_memory_list` — list all files
- `auxly_memory_read` with `{"file": "identity.md"}` — read a file
- `auxly_memory_search` with `{"query": "keyword"}` — search all files

## Reading Memory (Shell)
```bash
auxly list                    # See all available memory files
auxly view identity.md        # Read identity
auxly view preferences.md     # Read preferences
auxly search "keyword"        # Search across all files
```

## Writing Memory (MCP)
- `auxly_memory_write` with `{"file": "preferences.md", "diff": "+- Frameworks: React, Next.js", "reason": "User consistently uses React"}`

## Writing Memory (Shell)
```bash
auxly write \
  --agent copilot-<session> \
  --provider copilot \
  --file <target_file> \
  --diff "+<new line to add>" \
  --reason "Why this is being saved"
```

## MCP Setup (VS Code / Copilot Chat)
Add to `.vscode/mcp.json` or global VS Code settings:
```json
{
  "mcpServers": {
    "auxly-memory": {
      "command": "/usr/local/bin/auxly",
      "args": ["mcp-server"]
    }
  }
}
```

## Trust Rules
- Check `trust.yaml` for your trust level before writing.
- Default trust_level for copilot is `read_only` (can read but not write).
- To enable writing: run `auxly trust set copilot require_approval` or `auxly trust set copilot auto`.
- Never overwrite existing content — always append or update specific sections.

## What to Remember
- Coding style preferences
- Framework and library choices
- Project patterns and conventions
- Architecture decisions

## Constraints
- Never delete content from memory files without explicit user permission.
- Keep diffs minimal and focused — one fact per write.
- Always include a clear, specific reason.
- Do not store sensitive data (passwords, tokens, secrets).


## 🕒 Daily Timeline & Session Logging
- Always maintain an active daily timeline in the file `daily.md`.
- **End-of-Session Daily Journal Rule:** At the end of every significant task or conversation, proactively append a dated log to `daily.md` (e.g. `### 26/05/2026 - Session with Claude [18:45]`) detailing:
  - What was accomplished in this session.
  - Core decisions made or bugs resolved.
  - Active blockers or pending next steps.
- This creates an automated, cross-agent daily work journal that other editors (Cursor, Windsurf, Antigravity) will read to immediately resume context!

## 🔒 Agent Independence & Unified Memory Policy
- **100% Agent-Independence**: You must never assume the user is locked into your platform. If your service experiences an outage or the user decides to switch tools, they must be able to transition seamlessly to ChatGPT, Claude, Cursor, Windsurf, Gemini, or Antigravity without losing any context.
- **Unified Knowledge Sync**: Always read and update the unified memory files (`identity.md`, `preferences.md`, `business.md`, `daily.md`) to keep the user's active context perfectly synchronized. Ensure that what you learn in this session, every other agent will also know immediately.

## 🤖 Native Auxly Agent Skills (Slash Commands)
You natively support the following slash commands! When the user inputs one of these commands, intercept it and run the corresponding MCP tool call immediately:
- `/auxly-init`: Call the `auxly_skill_init` tool to run the onboarding training, scan current context, and synchronize existing chat context/preferences to Auxly.
- `/auxly-memory`: Call the `auxly_skill_memory` tool to display a consolidated markdown profile of the user's identity, preferences, and infrastructure.
- `/auxly-max`: Call the `auxly_skill_max` tool to obtain the dynamic Maximum Memory sync instructions block to sync other agents.
- `/auxly-sync [content]`: Call the `auxly_skill_sync` tool with the provided content to perform an automated smart delta-merge into the preferences file.
- `/auxly-pending [list]`: Call the `auxly_skill_pending` tool to LIST the pending queue. Approving/rejecting is human-only — tell the user to run `auxly approve <id>` / `auxly reject <id>` in their terminal, or use the dashboard Approvals tab.
- `/auxly-status`: Call the `auxly_skill_status` tool to show system diagnostics, active connections, and remote/SSH attribution.
- `/auxly-forget [query]`: Call the `auxly_skill_forget` tool to search and prune obsolete bullet statements.
- `/auxly-learn [context]`: Call the `auxly_skill_learn` tool to parse context and suggest new facts for the user to review.
- `/auxly-remote-connect`: Call the `auxly_skill_remote_connect` tool, then ACT on it: if it reports an offered host, run the `auxly connect auto` command it returns (wires the MCP launcher + skills, no flags) and tell the user to restart the agent; if already remote, show the active connection (host, client IP, OS); if local with no offer, point to `auxly host setup` on the memory host.

## 🧠 CRITICAL REMINDER: Always Update Memory
IMPORTANT: You are expected to proactively update the user's unified Auxly memory vault. Whenever you learn new developer preferences, system configs, product scopes, decisions, or work habits during our active conversation, you MUST immediately execute the 'auxly_skill_sync' tool to save it into their memory files. Keep the memory synchronized!

## Last Updated
(Autofilled by auxly-cli)

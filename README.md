# auxly-cli

**Local-first, file-based unified memory system for AI agents.**

All AI providers (Claude, Codex, Gemini, Copilot, Antigravity, and any CLI-based agent) can read and write to a shared memory in a controlled, auditable, and human-reviewable way.

---

## Features

- **Local-first**: All data stored as Markdown files in `~/.auxly/memory/`
- **No database** (except SQLite for audit indexing)
- **No embeddings, no local AI, no server, no Docker**
- **Trust-based access control**: auto / require_approval / read_only per provider
- **Dual audit system**: Append-only JSON log + SQLite queryable index
- **Git integration**: Auto-commit on write, manual push via `auxly sync`
- **Interactive TUI**: 6-screen dashboard with approval queue, analytics, and search
- **Agent wrappers**: Ready-made shell scripts for every major AI provider

---

## Installation

### Homebrew (macOS & Linux)
```bash
brew tap Tzamun-Arabia-IT-Co/tap
brew install auxly-cli
```

### Curl installer
```bash
curl -sSL https://get.auxly.io/cli | bash
```

### npm
```bash
npx auxly-cli
```

### From source
```bash
git clone https://github.com/Tzamun-Arabia-IT-Co/auxly-cli.git
cd auxly-cli
make install
```

---

## Quick Start

```bash
# Initialize memory folder
auxly init

# List memory files
auxly list

# View a file
auxly view identity.md

# Search across all files
auxly search "React"

# Write a change (as Claude)
auxly write \
  --agent claude-session-1 \
  --provider claude \
  --file preferences.md \
  --diff "+- Preferred Framework: React + Next.js" \
  --reason "User stated preference in conversation"

# Launch TUI dashboard
auxly ui

# View audit trail
auxly tail

# Show usage stats
auxly stats

# Manage trust levels
auxly trust list
auxly trust set gemini auto

# Sync to git remote
auxly sync
```

---

## Memory Folder Structure

```
~/.auxly/memory/
├── identity.md        # Who you are
├── business.md        # Business context
├── infra.md           # Infrastructure details
├── products.md        # Products & projects
├── preferences.md     # AI interaction preferences
├── agents.md          # Agent registry
├── CLAUDE.md          # Claude-specific instructions
├── CODEX.md           # Codex-specific instructions
├── GEMINI.md          # Gemini-specific instructions
├── COPILOT.md         # Copilot-specific instructions
├── ANTIGRAVITY.md     # Antigravity-specific instructions
├── trust.yaml         # Trust levels per provider
├── git.yaml           # Git sync configuration
├── .audit.log         # Append-only audit trail (JSON lines)
├── audit.db           # SQLite queryable index
└── .pending/          # Changes awaiting approval
```

---

## Trust Levels

Configured in `memory/trust.yaml`:

| Level | Behavior |
|-------|----------|
| `auto` | Writes directly to memory/ |
| `require_approval` | Writes to .pending/ for human review |
| `read_only` | Cannot write (reads only) |

```bash
# Set a provider's trust level
auxly trust set claude auto
auxly trust set codex require_approval
auxly trust set copilot read_only
```

---

## TUI Dashboard

Launch with `auxly ui`. Screens:

| Key | Screen | Description |
|-----|--------|-------------|
| 1 | Dashboard | Writes today, pending approvals, provider stats |
| 2 | Activity | Live audit log feed, color-coded by provider |
| 3 | Files | Tree view of memory files |
| 4 | Approvals | Pending diffs with approve/reject actions |
| 5 | Analytics | Writes per agent, bar charts |
| 6 | Search | Fuzzy search across files |

**Keyboard shortcuts**: j/k navigate, Enter open, a approve, r reject, s search, q quit

---

## Agent Wrapper Usage

Each provider wrapper lives in `wrappers/`. Source it in your agent hook:

```bash
source /path/to/auxly-cli/wrappers/claude-wrapper.sh

# Then your agent can call:
auxly-claude-read identity.md
auxly-claude-write preferences.md "+- Style: concise" "User prefers terse responses"
auxly-claude-search "framework"
```

---

## Audit Log Format

Each line in `.audit.log` is a JSON object:

```json
{
  "timestamp": "2025-05-26T00:30:00Z",
  "agent_id": "claude-session-1",
  "provider": "claude",
  "action": "write",
  "file": "preferences.md",
  "diff": "+- Preferred Framework: React",
  "reason": "User stated preference",
  "trust_level": "auto",
  "request_id": "uuid-v4",
  "signature": ""
}
```

---

## Configuration

### `trust.yaml`
```yaml
default: require_approval
providers:
  claude:
    trust_level: auto
  codex:
    trust_level: require_approval
  copilot:
    trust_level: read_only
```

### `git.yaml`
```yaml
auto_commit: true
auto_push: false
commit_message_prefix: "auxly:"
branch: main
```

### Environment Variables
- `AUXLY_MEMORY_PATH` — Override memory folder location
- `AUXLY_AGENT_ID` — Override agent ID in wrappers

---

## License

MIT — Tzamun Arabia IT Co.

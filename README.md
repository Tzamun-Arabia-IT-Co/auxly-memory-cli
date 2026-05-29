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

## Remote Memory over SSH

Auxly lets a remote/agent machine use a memory **host** as its single source of truth — with **plain SSH as the only transport**. There is no daemon, no open listening port, no auth token, and no gzip layer. A session is just:

```bash
ssh host auxly mcp-server
```

The IDE on the remote machine spawns this over SSH and speaks MCP over stdio. The host streams its memory and writes are audited on the host exactly as if they were local.

### Both machines run `auxly`

| Machine | Role | Command |
|---------|------|---------|
| Memory **host** | Serves memory + audits every access | `auxly mcp-server` (invoked over SSH) |
| **Remote / agent** | Holds the same skills + launches sessions | `auxly connect` (wizard) and a hidden `connect-mcp` launcher the IDE invokes |

Set up a link from the remote machine with the wizard:

```bash
auxly connect          # interactive wizard to link a remote memory host over SSH
auxly connect list     # list configured remote hosts
auxly connect remove   # remove a configured remote host
auxly connect test     # reachability + host-auxly dependency doctor
auxly connect print    # print the MCP JSON block (manual fallback)
```

### VPN-agnostic, bring-your-own network

Auxly **never installs or manages a VPN** and stores **no network credentials**. You bring the reachability; Auxly rides on top of it. **No public IP is required.** Any of these work:

| Method | When to use |
|--------|-------------|
| **LAN** | Host and remote on the same local network |
| **VPN** | Your own overlay network (e.g. Tailscale, WireGuard) — Auxly does not configure it for you |
| **Jump host / bastion** | Reach the host through an intermediate SSH hop |
| **Public / custom** | A reachable hostname/IP or a custom SSH config entry |

Because the transport is just SSH, anything that gives you `ssh host` already gives you Auxly.

### Remote agents in the Audit & Activity views

Sessions opened from a remote machine show up in the **Audit / Activity** views tagged as **SSH-remote**, annotated with the connecting **client IP** and **OS**. You can see at a glance which writes came from a remote agent versus a local one.

### OS-aware dependency doctor

`auxly connect` runs an OS-aware doctor that auto-checks the dependency surface on the host before linking:

- On a **macOS or Linux** host, it **silently installs** the `auxly` binary via the official release channel if it is missing.
- On a **Windows** host, or when **sshd** needs to be enabled, the steps are **guided** (printed instructions you confirm) rather than performed silently. For a Windows host, the doctor prints the PowerShell installer one-liner to run on the host:

  ```powershell
  irm https://get.auxly.io/cli.ps1 | iex
  ```

  (Silent install isn't attempted over SSH on Windows because the remote default shell may be `cmd.exe` or PowerShell.)

Run `auxly connect test` any time to re-run reachability checks and the host-side dependency doctor without re-linking.

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

<div align="center">

# 🧠 Auxly

### One memory. Every AI agent. On your machine.

**Auxly is a local-first, file-based memory layer that every AI agent you use — Claude, Codex, Gemini, Copilot, Cursor, Antigravity, and any CLI agent — reads from and writes to as a single shared source of truth.**

No cloud. No database. No vendor lock-in. Just Markdown files you own, with an audit trail you can read and a review queue you control.

[![Release](https://img.shields.io/github/v/release/Tzamun-Arabia-IT-Co/auxly-cli?label=release)](https://github.com/Tzamun-Arabia-IT-Co/auxly-cli/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8.svg)](go.mod)
![Platforms](https://img.shields.io/badge/platforms-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)

[Install](#install) · [Quick start](#quick-start) · [How it works](#how-it-works) · [Skills](#skills) · [Remote memory](#remote-memory-over-ssh) · [Docs](https://auxly.io/docs)

</div>

---

## The problem

Every AI agent keeps its own memory in its own walled garden. Tell Claude your stack, then open Codex — it knows nothing. Switch to Gemini — start over. Your context is fragmented across a dozen tools, none of them talk to each other, and none of them let you see or correct what they "remember" about you.

## What Auxly does

Auxly gives all of your agents **one** memory — a folder of Markdown files on your own machine — and wires every agent to it through the [Model Context Protocol (MCP)](https://modelcontextprotocol.io). Teach one agent something once; every other agent knows it instantly. And because the memory is plain Markdown under your control, you can read it, edit it, diff it, and version it in Git like any other file.

### Why you'll want it

| | Benefit |
|---|---|
| 🧠 **Shared context** | Say it once to any agent — all your other agents inherit it. No more re-explaining your stack, preferences, or projects per tool. |
| 📂 **You own the data** | Memory is Markdown in `~/.auxly/memory/`. Open it in any editor, grep it, commit it. Nothing is locked inside a vendor's cloud. |
| 🔒 **Local-first & private** | No server, no telemetry, no embeddings, no Docker. Memory never leaves your machine unless *you* push it to a Git remote. |
| 🛂 **You stay in control** | Per-agent trust levels decide whether a write lands instantly, queues for your approval, or is denied outright. |
| 🧾 **Fully auditable** | Every read and write is logged append-only with who, what, when, and why — surfaced in a live TUI. |
| 🌐 **Works across machines** | Share one memory host with NAT'd servers and laptops over plain SSH — no daemon, no open port, no token. |
| 🆓 **Free & open** | MIT-licensed Go binary. Single file, zero runtime dependencies. |

---

## How it works

Auxly is a single static Go binary that plays three roles at once:

```
                ┌─────────────────────────────────────────────┐
   Claude  ─┐   │                  auxly                       │
   Codex   ─┤   │   ┌──────────────┐      ┌────────────────┐   │
   Gemini  ─┼──▶│   │  MCP server  │─────▶│  Trust gate    │   │
   Copilot ─┤   │   │ (stdio JSON- │      │ auto / approve │   │
   Cursor  ─┤   │   │   RPC tools) │      │  / read-only   │   │
   …any CLI─┘   │   └──────────────┘      └───────┬────────┘   │
                │                                  ▼            │
                │   ┌──────────────┐      ┌────────────────┐   │
                │   │  Audit log   │◀─────│  ~/.auxly/      │   │
                │   │ JSONL + SQLite│      │  memory/*.md    │──┼──▶ git push
                │   └──────────────┘      └────────────────┘   │   (optional)
                └─────────────────────────────────────────────┘
```

1. **MCP server** — `auxly mcp-server` exposes a set of tools (read, write, search, sync, …) to any MCP-capable agent over stdio. Agents call them like any other tool.
2. **Trust gate** — every write is checked against the writing provider's trust level: write directly, queue for human approval, or reject.
3. **Memory vault** — accepted writes land as Markdown in `~/.auxly/memory/`, optionally auto-committed to Git.
4. **Audit** — every access is recorded to an append-only JSON Lines log and indexed in SQLite for instant querying in the TUI.

The only "database" anywhere is a local SQLite file used purely to index the audit log. There are **no embeddings, no background server, no network calls** in normal operation.

---

## Install

### macOS & Linux

```bash
curl -fsSL https://auxly.io/cli | sh
```

Bootstrap everything in one go — install **and** wire up your local agents:

```bash
curl -fsSL https://auxly.io/cli | sh -s -- --setup
```

### Windows (PowerShell)

```powershell
irm https://auxly.io/cli.ps1 | iex
```

### Homebrew

```bash
brew install Tzamun-Arabia-IT-Co/homebrew-tap/auxly
```

### Go

```bash
go install github.com/Tzamun-Arabia-IT-Co/auxly-cli@latest
```

### From source

```bash
git clone https://github.com/Tzamun-Arabia-IT-Co/auxly-cli.git
cd auxly-cli
make build         # produces ./auxly
# Apple Silicon dev builds: codesign --force --sign - ./auxly
```

Or grab a prebuilt binary / `.deb` / `.rpm` from the [Releases page](https://github.com/Tzamun-Arabia-IT-Co/auxly-cli/releases). Binaries are CGO-free single files — no archive to extract, no shared libraries to install.

---

## Quick start

```bash
# 1. Create your memory vault and walk through first-time setup
auxly init

# 2. Wire every AI agent on this machine to Auxly (MCP + slash commands)
auxly setup

# 3. Open the dashboard
auxly ui
```

Then, inside any connected agent's chat, type:

```
/auxly-init     # scans the conversation and seeds your memory
/auxly-sync     I prefer pnpm over npm and deploy on Vercel
/auxly-memory   # shows the consolidated profile every agent now shares
```

That's it. From now on, anything any agent learns about you can be saved with `/auxly-sync`, and every other agent picks it up.

---

## The memory vault

Your memory lives in `~/.auxly/memory/` as human-readable Markdown, organized by topic. Auxly's smart sync auto-routes new facts to the right file based on content:

```
~/.auxly/memory/
├── identity.md        # who you are, role, expertise
├── preferences.md     # how you like your agents to work
├── infra.md           # machines, networks, environments
├── products.md        # products & services you build
├── projects.md        # active work, goals, constraints
├── business.md        # business / organizational context
├── daily.md           # recent, time-bound notes
├── agents.md          # registry of connected agents
├── CLAUDE.md · CODEX.md · GEMINI.md · …   # per-agent instruction files
├── trust.yaml         # per-provider access control
├── git.yaml           # Git sync configuration
├── .audit.log         # append-only audit trail (JSON Lines)
├── audit.db           # SQLite index of the audit log
└── .pending/          # writes awaiting your approval
```

You can edit any of these by hand at any time — Auxly treats the files as the source of truth.

---

## Trust & access control

You decide what each provider is allowed to do. Trust levels live in `trust.yaml`:

| Level | Behavior |
|-------|----------|
| `auto` | Writes land in memory immediately |
| `require_approval` | Writes queue in `.pending/` for you to review and approve/reject |
| `read_only` | Provider can read but never write |

```bash
auxly trust list                      # show current levels
auxly trust set claude auto           # trust Claude to write directly
auxly trust set codex require_approval # review Codex's writes first
auxly trust set copilot read_only     # let Copilot read but not write
```

Pending writes show up as reviewable diffs in the TUI's **Approvals** tab — approve or reject with a keystroke.

---

## Skills

Auxly installs **8 slash commands** into every agent it configures. They work natively inside the agent's chat (Claude Code, Codex, Cursor, Gemini, Antigravity, Windsurf, …):

| Skill | What it does |
|-------|--------------|
| `/auxly-init` | Onboards you — scans the current conversation/system context and seeds your memory with what's already known. |
| `/auxly-sync` `<fact>` | Saves a new fact, preference, or detail with a smart delta-merge that auto-routes it to the right memory file. |
| `/auxly-memory` | Prints the consolidated profile (identity + preferences + infra) every agent currently shares. |
| `/auxly-learn` `[context]` | Inspects recent edits/context and proposes structured facts to save. |
| `/auxly-forget` `[query]` | Finds and cleanly prunes obsolete or outdated lines from memory. |
| `/auxly-pending` `[list\|approve\|reject]` | Manages the approval queue from inside the chat panel. |
| `/auxly-status` | Shows live diagnostics — connected clients, database sizes, tunnel info. |
| `/auxly-max` | Emits the "maximum memory" sync block to bootstrap additional local agents end-to-end. |

Under the hood these map to MCP tools (`auxly_skill_sync`, `auxly_memory_read`, `auxly_memory_write`, `auxly_memory_search`, `auxly_pending_list`, …) that any MCP client can call directly.

---

## Supported agents

`auxly setup` auto-detects what you have installed and writes the MCP configuration for each — no manual JSON editing:

| Agent | Integration |
|-------|-------------|
| **Claude Desktop** | MCP server entry |
| **Claude Code** (CLI) | `claude mcp add` + skills |
| **Codex** (IDE & CLI) | MCP + `codex mcp add` |
| **Cursor** (IDE & Agent CLI) | MCP + auto-approved tool allowlist |
| **Gemini CLI** | MCP server entry + skills |
| **Antigravity** (CLI / Agent / IDE) | MCP server entries |
| **GitHub Copilot** | shared memory via MCP/skills |
| **Windsurf**, **Kimi Code**, **Trae** | MCP + workspace rules |
| **Any CLI agent** | shell commands (`auxly read/write/search`) |

For each agent, Auxly also drops a workspace rules file (`.clauderules`, `.cursorrules`, `.geminirules`, …) so the agent knows to keep your memory in sync.

---

## TUI dashboard

`auxly ui` opens a full-screen terminal dashboard:

| # | Tab | What you see |
|---|-----|--------------|
| 1 | **Dashboard** | Today's writes, pending approvals, per-agent activity, brand-aware cards |
| 2 | **Activity** | Live audit feed, color-coded by provider, local vs. SSH-remote |
| 3 | **Files** | Browse and view your memory files |
| 4 | **Approvals** | Review pending diffs — approve or reject |
| 5 | **Analytics** | Writes per agent + (opt-in) live usage meters |
| 6 | **Settings** | Toggle features like Live Usage |
| 7 | **Remote** | Manage memory hosts and connected boxes over SSH |
| 8 | **Skills** | The installed slash commands at a glance |
| 9 | **Audit Trail** | Full, queryable history |

Keyboard-driven throughout: `1–9` jump tabs, `↑/↓` or `j/k` navigate, `Tab`/`[`/`]` cycle, `q` quits. Press `[u]` anywhere for the live usage popup.

---

## Remote memory over SSH

Run your agents on a server, a NAT'd box, or a teammate's laptop while keeping **one** memory host as the source of truth — over **plain SSH**. No daemon, no open listening port, no auth token, no custom protocol. A remote session is literally:

```bash
ssh host auxly mcp-server
```

The agent on the remote machine spawns that over SSH and speaks MCP over stdio; the host serves its memory and audits every access as if it were local.

### Connect from a machine (consumer side)

```bash
auxly connect          # interactive wizard: pick how the machines reach each other
auxly connect list     # show configured hosts + connected boxes
auxly connect auto     # one-command bootstrap when a host advertises an offer
```

Auxly is **network-agnostic** and stores **no network credentials** — you bring the reachability, it rides on top:

| Method | When |
|--------|------|
| **LAN** | Host and box on the same network |
| **VPN** | Your own overlay (Tailscale, WireGuard, …) — Auxly never configures it |
| **Bastion** | Reach the host through a jump host |
| **Public** | A reachable hostname/IP or custom SSH config entry |
| **Relay** | Serve a NAT'd box through a reverse tunnel you control |

### Serve your memory to other boxes (host side)

```bash
auxly host setup       # open a reverse tunnel through a relay you control
auxly host status      # see every served box and its tunnel state
auxly host clients     # list connected boxes
auxly host down        # stop serving
```

Multiple boxes stay connected **simultaneously** — each gets its own independent, self-healing tunnel, and the host keep-alive supervises them all. Connecting one box never disconnects another. Remote sessions are tagged **SSH-remote** in the Activity/Audit views with the connecting client's IP and OS, so you always know which writes came from where.

`auxly connect` also runs an OS-aware doctor that installs `auxly` on a macOS/Linux host automatically (and prints guided steps on Windows), so linking a new machine is usually a single command.

---

## Live Usage (opt-in)

Auxly can show each agent's **live subscription quota** — session and weekly usage — right in the dashboard, by reusing each agent's own locally-stored login token. It reads:

- **Claude** / **Claude Code**, **Codex (ChatGPT)** — session & week %, plus plan/tier
- **Gemini**, **Antigravity** — overall quota %
- **Cursor** — local AI-code activity (no network call)

This is the **only** feature that makes outbound network calls, it is **off by default**, and you enable it in **Settings**. Tokens are never logged, cached, or forwarded; each provider is called only for its own usage. Antigravity needs a one-time login:

```bash
auxly usage show              # print quota for every agent
auxly usage auth antigravity  # one-time browser consent for Antigravity
```

---

## Git sync

Your memory is a folder — so version it. Auxly auto-commits on write and pushes only when you ask:

```bash
auxly sync     # commit + push to your configured remote
```

```yaml
# git.yaml
auto_commit: true
auto_push: false
commit_message_prefix: "auxly:"
branch: main
```

---

## Command reference

| Command | Description |
|---------|-------------|
| `auxly init` | Create the memory vault and run first-time setup |
| `auxly setup` | Detect and wire every local agent (MCP + skills) |
| `auxly ui` | Launch the TUI dashboard |
| `auxly list` / `view <file>` | List or view memory files |
| `auxly search <query>` | Search across all memory |
| `auxly write …` | Write a change (used by agents/wrappers) |
| `auxly trust list \| set <provider> <level>` | Manage access control |
| `auxly tail` | Stream the audit log |
| `auxly stats` | Memory & write statistics |
| `auxly sync` | Commit + push memory to Git |
| `auxly connect …` | Link this machine to a remote memory host |
| `auxly host …` | Serve this machine's memory to other boxes |
| `auxly usage show \| auth` | Live agent quota (opt-in) |
| `auxly mcp-server` | Run the MCP server (invoked by agents) |
| `auxly update` | Self-update to the latest release |

---

## Configuration

### Environment variables

| Variable | Purpose |
|----------|---------|
| `AUXLY_MEMORY_PATH` | Override the memory folder location |
| `AUXLY_INSTALL_BASE` | Override the download/update base (default `https://auxly.io`) |
| `AUXLY_PROVIDER` | Override the provider id for a write |
| `AUXLY_LLM_BASE` | Override the LLM endpoint used by smart sync |

Auto-update polls `auxly.io/version` and, when a newer release exists, prints a one-line notice; `auxly update` performs a one-click self-update from the official release channel.

---

## Security & privacy

- **Local-first.** Memory lives only on your machine; nothing is uploaded unless you push to your own Git remote.
- **No telemetry.** Auxly phones home only for version checks and the opt-in Usage feature.
- **Credentials stay put.** Auxly stores no SSH keys, VPN config, or network secrets. Usage tokens are read in place and never persisted or logged.
- **Auditable by design.** Every read and write is recorded with who/what/when/why and reviewable in the TUI.
- **You hold the keys.** Trust levels and the approval queue mean no agent writes anything you didn't allow.

Found a vulnerability? See [SECURITY.md](SECURITY.md) for private disclosure.

---

## Contributing

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for the build, test, and PR flow.

```bash
make build && go test ./...
```

---

## License

[MIT](LICENSE) © Tzamun Arabia IT Co.

<div align="center">
<sub>Built by <a href="https://auxly.io">Tzamun Arabia IT Co.</a> — your memory, every agent, on your machine.</sub>
</div>

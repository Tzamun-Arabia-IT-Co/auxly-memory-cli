<div align="center">

<img src="assets/brand/auxly-banner.png" alt="Auxly" width="380" />

### One memory. Every AI agent. On your machine.

**Auxly is a local-first, file-based memory layer that every AI agent you use — Claude, Codex, Gemini, Copilot, Cursor, Antigravity, and any CLI agent — reads from and writes to as a single shared source of truth.**

No cloud. No database. No vendor lock-in. Just Markdown files you own, with an audit trail you can read and a review queue you control.

[![Release](https://img.shields.io/github/v/release/Tzamun-Arabia-IT-Co/auxly-memory-cli?label=release)](https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/releases)
[![Downloads](https://img.shields.io/github/downloads/Tzamun-Arabia-IT-Co/auxly-memory-cli/total?label=downloads&color=brightgreen)](https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/releases)
[![Stars](https://img.shields.io/github/stars/Tzamun-Arabia-IT-Co/auxly-memory-cli?style=flat)](https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/stargazers)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8.svg)](go.mod)
![Platforms](https://img.shields.io/badge/platforms-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)

<br />

<img src="screenshots/Dashboard-new.png" alt="The Auxly dashboard — connected agents, memory stats, live activity, and remote connections in one terminal view" width="860" />

<sub>One local dashboard for every agent you use — connected brands, memory by category, recent writes, and live remote connections.</sub>

</div>

---

## Contents

- [Why Auxly](#why-auxly)
- [How it works](#how-it-works)
- [Install](#install)
- [Quick start](#quick-start)
- [The memory vault](#the-memory-vault)
- [Trust & access control](#trust--access-control)
- [Skills (slash commands)](#skills-slash-commands)
- [Supported agents](#supported-agents)
- [Setup guide](#setup-guide)
- [The dashboard](#the-dashboard)
- [Remote memory over SSH](#remote-memory-over-ssh)
- [Live Usage](#live-usage)
- [Claude Code statusline](#claude-code-statusline)
- [Git sync](#git-sync)
- [Connect any MCP agent](#connect-any-mcp-capable-agent-manual-setup)
- [Command reference](#command-reference)
- [Configuration](#configuration)
- [Security & privacy](#security--privacy)
- [Contributing](#contributing)
- [License](#license)

---

## Why Auxly

### The problem

Every AI agent keeps its own memory in its own walled garden. Tell Claude your stack, then open Codex — it knows nothing. Switch to Gemini — start over. Your context is fragmented across a dozen tools, none of them talk to each other, and none let you see or correct what they "remember" about you.

### What Auxly does

Auxly gives all of your agents **one** memory — a folder of Markdown files on your own machine — and wires every agent to it through the [Model Context Protocol (MCP)](https://modelcontextprotocol.io). Teach one agent something once; every other agent knows it instantly. Because the memory is plain Markdown under your control, you can read it, edit it, diff it, and version it in Git like any other file.

### The benefits

| | Benefit |
|---|---|
| 🧠 **Shared context** | Say it once to any agent — all your other agents inherit it. No more re-explaining your stack, preferences, or projects per tool. |
| 📂 **You own the data** | Memory is Markdown in `~/.auxly/memory/`. Open it in any editor, grep it, commit it. Nothing is locked inside a vendor's cloud. |
| 🔒 **Local-first & private** | No server, no telemetry, no embeddings, no Docker. Memory never leaves your machine unless *you* push it to a Git remote. |
| 🛂 **You stay in control** | Per-agent trust levels decide whether a write lands instantly, queues for your approval, or is denied. |
| 🧾 **Fully auditable** | Every read and write is logged append-only with who, what, when, and why — surfaced in a live dashboard. |
| 🌐 **Works across machines** | Share one memory host with NAT'd servers and laptops over plain SSH — no daemon, no open port, no token. |
| 🆓 **Free & open** | MIT-licensed Go binary. Single static file, zero runtime dependencies. |

---

## How it works

Auxly is a single static Go binary that plays three roles at once:

```mermaid
flowchart LR
    agents["Claude &middot; Codex &middot; Gemini<br/>Copilot &middot; Cursor &middot; any CLI agent"]
    mcp["MCP server<br/>stdio: read &middot; write &middot; search &middot; sync"]
    gate{"Trust gate<br/>auto &middot; approve &middot; read-only"}
    vault[("~/.auxly/memory/*.md")]
    audit["Audit log<br/>JSONL + SQLite index"]
    git["git push (optional)"]

    agents -->|MCP over stdio| mcp
    mcp --> gate
    gate -->|accepted write| vault
    vault --> audit
    vault -.-> git
```

1. **MCP server** — `auxly mcp-server` exposes tools (read, write, search, sync, …) to any MCP-capable agent over stdio. Agents call them like any other tool.
2. **Trust gate** — every write is checked against the writing provider's trust level: write directly, queue for human approval, or reject.
3. **Memory vault** — accepted writes land as Markdown in `~/.auxly/memory/`, optionally auto-committed to Git.
4. **Audit** — every access is recorded to an append-only JSON Lines log and indexed in SQLite for instant querying in the dashboard.

The only "database" anywhere is a local SQLite file used purely to index the audit log. There are **no embeddings, no background daemon, and no network calls** in normal operation.

---

## Install

### macOS & Linux

```bash
curl -fsSL https://auxly.io/cli | sh
```

Install **and** wire up your local agents in one go:

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
go install github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli@latest
```

### From source

```bash
git clone https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli.git
cd auxly-memory-cli
make build         # produces ./auxly
# Apple Silicon dev builds: codesign --force --sign - ./auxly
```

Prebuilt binaries, `.deb`, and `.rpm` packages are on the [Releases page](https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/releases). Binaries are CGO-free single files — nothing to extract, no shared libraries to install.

---

## Quick start

Auxly is built around **one command** — just run `auxly`:

```bash
auxly
```

- **First run** walks you through a short setup wizard and creates your memory vault.
- **Every run after that** opens the full-screen **dashboard**.

That's the whole entry point — there's no separate "init" or "ui" step to remember (those exist as explicit aliases, but you never need them).

Next, connect your AI agents to the shared memory:

```bash
auxly setup
```

This detects every AI agent installed on your machine (Claude, Codex, Gemini, Cursor, Antigravity, …) and wires each one to Auxly via MCP — plus installs the `/auxly-*` slash commands. No manual config editing.

Now, inside any connected agent's chat, start teaching it:

```
/auxly-init                  # scans the conversation and seeds your memory
/auxly-sync  I prefer pnpm and deploy on Vercel
/auxly-memory                # shows the profile every agent now shares
```

From here on, anything any agent learns about you can be saved with `/auxly-sync`, and every other agent picks it up automatically.

---

## The memory vault

Your memory lives in `~/.auxly/memory/` as human-readable Markdown, organized by topic. Smart sync files each new fact under the right category — the agent picks the best-fit category from the taxonomy, with a keyword router as fallback when it's unsure:

```
~/.auxly/memory/
├── identity.md        # who you are, role, expertise
├── personal.md        # private life facts (family, health, finances) — never shared to a remote unless you grant it
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

Edit any of these by hand at any time — Auxly treats the files as the source of truth.

---

## Trust & access control

You decide what each provider is allowed to do. Trust levels live in `trust.yaml`:

| Level | Behavior |
|-------|----------|
| `auto` | Writes land in memory immediately |
| `require_approval` | Writes queue in `.pending/` for you to review and approve/reject |
| `read_only` | Provider can read but never write |

```bash
auxly trust list                       # show current levels
auxly trust set claude auto            # trust Claude to write directly
auxly trust set codex require_approval # review Codex's writes first
auxly trust set copilot read_only      # let Copilot read but not write
```

Pending writes show up as reviewable diffs in the dashboard's **Approvals** tab — approve or reject with a keystroke.

---

## Skills (slash commands)

`auxly setup` installs **10 slash commands** into every agent it configures. They work natively inside the agent's chat:

| Skill | What it does |
|-------|--------------|
| `/auxly-init` | Onboards you — runs the training, scans the current conversation/context, and seeds your memory with what's already known. |
| `/auxly-sync` `<fact>` | Saves a new fact, preference, or detail with a smart delta-merge — the agent files it under the best-fit category (with a keyword router as fallback). |
| `/auxly-memory` | Prints the consolidated profile (identity + preferences + infrastructure) every agent currently shares. |
| `/auxly-learn` `[folder] [topic]` | Reads the memory vault — optionally a single folder, optionally focused on a topic — and grounds the agent in it for the session. No args = learn everything. |
| `/auxly-max` | Exhaustive self-harvest — scans the whole session and writes every fact up into the vault, one category at a time (private facts go to `personal.md`). Push-only. |
| `/auxly-forget` `[query]` | Searches memory and cleanly prunes obsolete or outdated lines. |
| `/auxly-pending` `[list\|approve\|reject]` | Manages the approval queue from inside the chat panel. |
| `/auxly-status` | Shows whether the agent is connected and the MCP link is live, plus diagnostics. |
| `/auxly-bootstrap` | Generates a copyable onboarding block to paste into a tool that doesn't have Auxly installed (e.g. ChatGPT). |
| `/auxly-remote-connect` | Detects and connects this machine to a remote Auxly memory host (or reports the active link). |

Under the hood these map to MCP tools (`auxly_skill_sync`, `auxly_memory_read`, `auxly_memory_write`, `auxly_memory_search`, `auxly_pending_list`, …) that any MCP client can call directly.

---

## Supported agents

`auxly setup` auto-detects what you have installed and writes the MCP configuration for each — no manual JSON editing:

| Agent | Integration |
|-------|-------------|
| **Claude Desktop** | MCP server entry (+ importable skills — see [Setup guide](#setup-guide)) |
| **Claude Code** (CLI) | `claude mcp add` + skills |
| **Codex** (IDE & CLI) | MCP + `codex mcp add` |
| **Cursor** (IDE & Agent CLI) | MCP + auto-approved tool allowlist |
| **Gemini CLI** | MCP server entry + skills |
| **Antigravity** (CLI / Agent / IDE) | MCP server entries |
| **GitHub Copilot** | shared memory via MCP/skills |
| **Warp** (terminal) | MCP — `~/.warp/.mcp.json` |
| **Void** (editor) | MCP — `~/.void-editor/mcp.json` |
| **Windsurf**, **Kimi Code**, **Trae** | MCP + workspace rules |
| **Android Studio** | MCP via the Gemini Agent or JetBrains AI Assistant — [manual setup](#connect-any-mcp-capable-agent-manual-setup) |
| **Any MCP client / CLI agent** | paste an MCP entry ([manual setup](#connect-any-mcp-capable-agent-manual-setup)) or call `auxly read/write/search` |

For each agent, Auxly also drops a workspace rules file (`.clauderules`, `.cursorrules`, `.geminirules`, …) so the agent knows to keep your memory in sync.

---

## Setup guide

### Automatic setup (recommended)

```bash
auxly setup
```

This detects every supported agent on your machine, writes each one's MCP configuration, installs the `/auxly-*` slash commands, and drops a workspace rules file so the agent keeps your memory in sync. Re-run it any time you install a new agent — it's idempotent and only updates what's needed.

**Verify a connection** from inside the agent's chat:

```
/auxly-status
```

…or just open the dashboard (`auxly`): every connected agent appears on the grid, and its reads/writes show live in the **Activity** tab.

### Claude Desktop skills (one manual step)

Claude Desktop is the **only** agent that needs a manual touch. `auxly setup` wires its **MCP connection** automatically, but Claude Desktop doesn't load skills from disk — so the `/auxly-*` slash commands have to be **imported once**:

1. **Run `auxly setup`.** It exports the skills to `~/Downloads/auxly-skills-v<version>/` as ready-to-import `.zip` files — one per skill.
2. Open **Claude Desktop → Settings → Capabilities → Skills** (older builds: **Settings → Skills**).
3. **Add each `.zip`** from that folder (use *Upload skill* / drag-and-drop). You only do this once.
4. **Restart Claude Desktop** if the new skills don't show up right away.

The export folder is version-stamped (`…-v<version>/`), so when a release updates the skills you'll know to re-import the new set. *(Every other agent — Claude Code, Codex, Gemini, Cursor, … — picks up skills automatically; this import step is Claude-Desktop-only.)*

### Connect any other MCP agent

`auxly setup` covers the agents listed above. **Any** other tool that speaks MCP — Android Studio, Perplexity, a homegrown client — can share the exact same memory with a one-time copy-paste config. The full walkthrough with the ready-to-paste JSON is at the end of this README:

➜ **[Connect any MCP-capable agent (manual setup)](#connect-any-mcp-capable-agent-manual-setup)**

---

## The dashboard

`auxly` opens a full-screen terminal dashboard:

| # | Tab | What you see |
|---|-----|--------------|
| 1 | **Dashboard** | Today's writes, pending approvals, and a live grid of your connected agents |
| 2 | **Activity** | Live audit feed, color-coded by provider, local vs. SSH-remote |
| 3 | **Files** | Browse, view, edit, and download your memory files |
| 4 | **Approvals** | Review pending diffs — approve or reject |
| 5 | **Memory Org** | On-demand memory organization with a preview-and-confirm review (see below) |
| 6 | **Analytics** | Writes per agent + (opt-in) live usage meters |
| 7 | **Settings** | Trust levels and Live Usage — plus an **Agents** sub-tab to show/hide which agents appear on the dashboard |
| 8 | **Remote** | Manage memory hosts and connected boxes over SSH |
| 9 | **Skills** | The installed slash commands at a glance |
| 0 | **Audit Trail** | Full, queryable history with a **Type** column and `f` to filter (Memory Org / Writes / Sessions / Approvals) |

The agent grid is **dynamic** — it shows only the agents detected or active on this machine, so it stays readable whether you run two agents or twenty. Any agent that connects and writes appears automatically (even one wired by hand); hide the ones you don't want to see under **Settings → Agents**.

Keyboard-driven throughout: `1–9`/`0` jump tabs, `↑/↓` or `j/k` navigate, `Tab`/`[`/`]` cycle, `q` quits. Press `[u]` anywhere for the live usage popup.

<div align="center">

<img src="screenshots/audit-trail.png" alt="The Audit Trail tab — full, queryable history of every memory access" width="820" />

<sub>The <strong>Audit Trail</strong> tab (<code>0</code>): every read and write — local and SSH-remote — with a <code>Type</code> filter.</sub>

</div>

### Memory Organization

The **Memory Org** tab (`5`) runs an AI pass that consolidates and re-files your memory vault — deduplicating facts, moving misplaced lines to the right category, and tidying each file — **without writing anything until you approve it**.

1. **Pick a provider + model.** Any installed CLI agent (Claude, Codex, Gemini, Cursor, Antigravity) runs headless on your existing subscription — no API key. Or choose a local/API endpoint (Ollama, OpenAI, Gemini, or any OpenAI-compatible Custom URL, with models auto-fetched). The idle screen shows roughly how many tokens and files will be sent before you launch.
2. **Run.** The model reorganizes a copy of your vault in an isolated, read-only sandbox. Press `esc` any time to cancel the run.
3. **Review every change.** A two-pane before/after diff per file, color-coded by added/removed lines. Approve (`a`), reject (`r`), or edit (`e`) each file; `A` approves all. Approving auto-advances to the next file.
4. **Submit.** Only the files you approved are written. A confirmation screen lists exactly what changed, and every write is recorded in the **Audit Trail** (tagged **Memory Org**) so you have durable history.

**Scope & safety:** organize only ever touches your *user-memory* files. Agent setup/instruction files (`agents.md`, `CLAUDE.md`, `AGENTS.md`, `providers.md`, …) are never read or rewritten. Nothing is saved until you approve, and the agent runs with no tools, an empty working directory, and a scrubbed environment so it can never reach your real vault on its own.

---

## Remote memory over SSH

Run your agents on a server, a NAT'd box, or another laptop while keeping **one** memory host as the source of truth — over **plain SSH**. There is **no daemon, no open listening port, no auth token, and no custom protocol**. A remote session is literally:

```bash
ssh host auxly mcp-server
```

The agent on the remote machine spawns that over SSH and speaks MCP over stdio; the host serves its memory and audits every access as if it were local. Anything that gives you `ssh host` already gives you Auxly — bring your own LAN, VPN, bastion, or relay.

### Two roles

```mermaid
flowchart LR
    subgraph consumer["CONSUMER box"]
        c["your agent runs here<br/>auxly connect &hellip;"]
    end
    subgraph host["MEMORY HOST"]
        h["memory lives here<br/>audits every access"]
    end
    c -->|"ssh host auxly mcp-server"| h
    h -.->|"memory over stdio"| c
```

| Role | What it does | Command |
|------|--------------|---------|
| **Memory host** | Serves the shared memory and audits every access | runs `auxly mcp-server` (invoked over SSH) |
| **Consumer box** | Where an agent runs; reaches the host's memory | `auxly connect` |

### Connecting a machine to a host

On the **consumer** box, run the wizard and pick how the two machines reach each other:

```bash
auxly connect          # wizard: LAN / VPN / bastion / public / relay
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

### Serving your memory to NAT'd boxes (relay)

If a box can't reach your machine directly (it's behind NAT), your machine opens an outbound **reverse tunnel** through a relay you both can reach, and the box reaches your memory through it. On the **host**:

```bash
auxly host setup       # open the reverse tunnel via a relay; provision the box
auxly host status      # every served box + its live tunnel state
auxly host clients     # list connected boxes
auxly host down        # stop serving
```

**Multiple boxes stay connected at the same time** — each gets its own independent, self-healing tunnel, supervised by a single keep-alive. Connecting one box never disconnects another.

### Choose what each box can see

A remote never gets your whole vault by default — every served box carries its **own** per-remote file-sharing allow-list. In the dashboard's **Remote** tab, highlight a connected box and press **`s`** to open its **Share files** checklist (listed in taxonomy order), then toggle individual files and set **read** vs. **write** access for that box specifically.

It's **fail-closed**: `personal.md` (and the aggregate profile) are **never** shared unless you explicitly check them, and the default for a newly connected box is *all non-personal files, read-only*. So a trusted laptop can read everything while a CI box sees only `projects.md` — and your private life facts stay on your machine.

### See where writes come from

Sessions opened from a remote machine appear in the **Activity** and **Audit Trail** tabs tagged **SSH-remote**, annotated with the connecting client's **IP** and **OS** — so you always know which writes came from a remote agent versus a local one. `auxly connect` also runs an OS-aware doctor that installs `auxly` on a macOS/Linux host automatically (and prints guided steps on Windows), so linking a new machine is usually a single command.

---

## Live Usage

Auxly can show each agent's **live subscription quota** — session and weekly usage — right in the dashboard, by reusing each agent's own locally-stored login token. It reads:

- **Claude** / **Claude Code**, **Codex (ChatGPT)** — session & week %, plus plan/tier
- **Gemini**, **Antigravity** — overall quota %
- **Cursor** — local AI-code activity (no network call)

This is the **only** feature that makes outbound network calls, it is **off by default**, and you enable it in **Settings**. Tokens are never logged, cached, or forwarded; each provider is called only for its own usage. Antigravity needs a one-time login:

```bash
auxly usage show              # print quota for every agent
auxly usage auth antigravity  # one-time browser consent for Antigravity
```

<div align="center">

<img src="screenshots/agent-usage.png" alt="Live Usage — session and weekly quota for Claude, Codex, Gemini, Antigravity, and Cursor side by side" width="820" />

<sub>Session and weekly quota for every connected agent, reusing each one's own login — no API key.</sub>

</div>

---

## Claude Code statusline

Auxly ships a productized statusline for **Claude Code** — a rich, multi-line status bar that surfaces your working context, your memory link, and your live plan usage without leaving the editor. Wire it in (additive and fully reversible — your existing statusline is backed up and restored on uninstall):

```bash
auxly statusline install          # replace your statusline with Auxly's full 4-line view
auxly statusline install --wrap   # keep your own statusline and append the Auxly segment
auxly statusline uninstall        # restore your backed-up original
```

It renders four lines: **where** (folder · branch · model · version), **session** (thinking · effort · tokens · context bar), **Auxly memory** (link · role · last op · pending), and **live Claude plan usage** (5h + weekly bars with reset countdowns).

The render path **never makes a network call** — it reads only the last-good usage snapshot on disk, so it's safe to run on every prompt. When Live Usage is enabled, the usage line stays **live** during an active session: after printing instantly it triggers a guarded, detached background refresh that updates the snapshot for the next render (debounced, and rate-limited by the same circuit breaker as the dashboard). You can also manage it in the dashboard under **Settings → Customizations**, with a live preview before you apply.

> Statusline support is Claude Code–specific (other CLIs don't expose a scriptable statusline yet) — your memory still works everywhere.

<div align="center">

<img src="screenshots/statusline.png" alt="Settings → Customizations — Claude Code statusline picker with a live preview" width="820" />

<sub>Settings → Customizations: pick your statusline mode with a live preview before applying.</sub>

</div>

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

## Connect any MCP-capable agent (manual setup)

Auxly is a standard **stdio MCP server**, so *any* MCP-capable tool can share the same memory — even ones Auxly doesn't auto-detect, or whose config lives somewhere `auxly setup` can't write (Android Studio's Gemini settings sync to your Google account, for example). It's the same three pieces everywhere.

**1. Add the server entry** wherever your agent keeps MCP servers — its config file, or an "Add MCP server" dialog:

```json
{
  "mcpServers": {
    "auxly-memory": {
      "command": "/absolute/path/to/auxly",
      "args": ["--path", "/Users/you/.auxly/memory", "mcp-server"],
      "env": {
        "AUXLY_MEMORY_PATH": "/Users/you/.auxly/memory",
        "AUXLY_PROVIDER": "your-agent-id"
      }
    }
  }
}
```

**2. Fill in the three values:**

| Field | What to put | How to find it |
|-------|-------------|----------------|
| `command` | Absolute path to the `auxly` binary | `which auxly` → e.g. `/usr/local/bin/auxly` |
| `--path` / `AUXLY_MEMORY_PATH` | Your vault folder | Default `~/.auxly/memory` (use the full absolute path) |
| `AUXLY_PROVIDER` | A short, unique id for *this* agent | e.g. `perplexity`, `android-studio` — this is how the audit trail and dashboard label its writes, so **never reuse another agent's id** or its writes get mislabeled |

**3. Reload the agent.** Some clients pick the server up instantly; others need a restart or a one-time "approve this server" before the tools go live.

**4. Verify.** From the agent's chat run `/auxly-status` (if it has skills), or just open `auxly` — the moment the agent connects and writes, it appears on the dashboard grid, is labeled by its `AUXLY_PROVIDER` id in the **Audit Trail**, and can be hidden or re-shown under **Settings → Agents**.

> **Schema variations:** most clients use the `{ "mcpServers": { … } }` wrapper above (Claude Desktop, Warp, Void, Cursor, JetBrains AI Assistant, …). A few accept only the inner `{ "auxly-memory": { … } }` object — if the wrapped form is rejected, paste just the inner block. On Windows, use a full path with escaped backslashes (`"C:\\Users\\you\\auxly.exe"`) or a forward-slash path.
>
> **App-specific entry points:** Android Studio → the Gemini **"Configure MCP servers"** dialog, or **JetBrains AI Assistant → Settings → MCP** (which can also *Import from Claude* once you've run `auxly setup`). Perplexity and most desktop clients → their **Settings → Connectors / MCP** panel.

---

## Command reference

| Command | Description |
|---------|-------------|
| `auxly` | First run: setup wizard. After that: open the dashboard. |
| `auxly setup` | Detect and wire every local agent (MCP + skills) |
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

Auxly is config-light: it works with zero setup, and everything it *does* persist lives under `~/.auxly/` as plain text you can read, edit, and version.

### Config files

All optional — Auxly creates and manages these for you; edit them by hand any time.

| File | What it controls |
|------|------------------|
| `~/.auxly/memory/trust.yaml` | Per-provider trust levels (`auto` / `require_approval` / `read_only`) — or use `auxly trust set …` |
| `~/.auxly/memory/git.yaml` | Git sync remote and behavior (see [Git sync](#git-sync)) |
| `~/.auxly/settings.json` | Dashboard preferences: `liveUsage` opt-in (off by default) and `hiddenAgents` — toggled in **Settings** |
| `~/.claude/settings.json` | Claude Code statusline wiring — managed by `auxly statusline install/uninstall` |

### Environment variables

| Variable | Purpose |
|----------|---------|
| `AUXLY_MEMORY_PATH` | Override the memory folder location (default `~/.auxly/memory`) |
| `AUXLY_INSTALL_BASE` | Override the download/update base (default `https://auxly.io`) |
| `AUXLY_PROVIDER` | Override the provider id for a write |
| `AUXLY_LLM_BASE` | Override the LLM endpoint used by smart sync |

Auto-update polls `auxly.io/version` and, when a newer release exists, prints a one-line notice; `auxly update` performs a one-click self-update from the official release channel.

---

## Security & privacy

- **Local-first.** Memory lives only on your machine; nothing is uploaded unless you push to your own Git remote.
- **No telemetry.** Auxly phones home only for version checks and the opt-in Usage feature.
- **Credentials stay put.** Auxly stores no SSH keys, VPN config, or network secrets. Usage tokens are read in place and never persisted or logged.
- **Auditable by design.** Every read and write is recorded with who/what/when/why and reviewable in the dashboard.
- **You hold the keys.** Trust levels and the approval queue mean no agent writes anything you didn't allow.

Found a vulnerability? See [SECURITY.md](SECURITY.md) for private disclosure.

---

## Contributing

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for the build, test, and PR flow, and our [Code of Conduct](CODE_OF_CONDUCT.md).

```bash
make build && go test -race ./...
```

---

## License

[MIT](LICENSE) © Tzamun Arabia IT Co.

<div align="center">
<br/>
<sub>Built with care by</sub>
<br/><br/>
<img src="assets/brand/tzamun-banner.png" alt="Tzamun Arabia IT Co." width="200" />
<br/><br/>
<sub><a href="https://auxly.io">auxly.io</a> · your memory, every agent, on your machine</sub>
</div>

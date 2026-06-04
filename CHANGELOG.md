# Changelog

All notable changes to Auxly Memory CLI are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.9] - 2026-06-04

### Added — Statusline now works for Cursor CLI and Antigravity CLI, not just Claude Code

- `auxly statusline` is now **provider-aware**: it renders natively for **Claude
  Code, Cursor CLI, and Antigravity CLI**, parsing each agent's session payload
  (Cursor's `param_summary` / `max_mode` / `autorun` and role-based transcripts;
  Gemini/Antigravity's `model.name`) and showing **that agent's own plan usage** on
  line 4 — Claude's 5h+weekly, Cursor's plan/auto, Antigravity's overall. Each
  provider reads **only its own cache key**, so one agent's usage can never leak into
  another's statusline.
- `auxly statusline install --agent claude|cursor|antigravity|all` wires the chosen
  agent's config (`~/.claude/settings.json`, `~/.cursor/cli-config.json`, or
  `~/.gemini/antigravity-cli/settings.json`). It is **additive and fully reversible**
  per agent: the prior command — including a hand-rolled `statusline.sh` — is backed
  up and restored verbatim on `uninstall`. Each agent's own statusLine extras
  (Cursor's `padding`/`updateIntervalMs`/`timeoutMs`, Antigravity's `enabled`) and
  every unrelated settings key are preserved.
- The installed command carries an explicit `--provider <agent>` flag so render never
  has to guess; a payload-shape **auto-detect** is the fallback for a hand-edited
  command.
- **Settings → Customizations** is now a multi-agent page: an agent switcher (`a` to
  cycle) over Claude / Cursor / Antigravity, each with its own replace / wrap / none
  choice and a **live preview rendered with that agent's model + usage line** (the
  preview self-refreshes, so Cursor/Antigravity read `↻ live`, not `⧗ as of`). The
  option cursor always starts on **①**, applying an agent **auto-advances** to the
  next available one, and when an agent already runs Auxly with a saved backup the
  third option becomes **"Restore my original statusline"** (showing the exact command
  it puts back). The old "Claude Code only" banner is gone.

### Added — Keep remote machines current automatically

- **Opt-in Auto-Update** (`autoUpdate` setting + a **Settings** toggle, off by
  default). When on, `auxly` self-updates to the latest published release **in place
  after an interactive session finishes** — never mid-run, and never on the hot
  statusline path — so the next launch runs the new binary. Enable it on remote
  machines and they stay current on every publish without a manual `auxly update`.
- **Statusline as a follows-you preference**: `auxly connect auto` now also installs
  the Auxly statusline on the connecting machine for its detected agents. It's
  **idempotent and non-destructive** — only agents with no statusline yet are wired;
  a machine running its own statusline is left untouched.

### Fixed

- **Cursor usage no longer shows "no quota data" for an idle plan.** A `200 OK` from
  Cursor's usage endpoint now always emits the plan (Total)/Auto bars — even at 0% —
  so a brand-new or just-reset plan renders as full meters instead of looking broken.
  Errors are reserved for genuine transport/auth failures.
- **Dropped Cursor's misleading API bar.** Cursor's endpoint reports a non-zero
  `apiPercentUsed` even for plan-only users who never touch an API key, so it was
  noise on the statusline. Only the meaningful plan (Total) and Auto quotas are shown.
- **Statusline no longer renders another agent's usage.** Provider detection no longer
  treats `used_percentage` as a Cursor signal (Claude Code sends it too, which made a
  Claude session misread as Cursor), and the installed command bakes in `--provider`
  so render is deterministic rather than guessing from payload shape.

### Documentation

- README statusline section rewritten for the three agents: `--agent` install matrix,
  per-agent config-file table, and the corrected "works beyond Claude Code" framing.

## [1.0.8] - 2026-06-04

### Changed — Statusline plan-usage is now live, not a frozen snapshot

- The Claude Code statusline's plan-usage line (`🔋 Claude · …`) now stays
  **live during a normal coding session**, instead of showing `⧗ as of HH:MM`
  whenever the dashboard wasn't open to feed the cache. Previously nothing
  refreshed `~/.auxly/usage-cache.json` outside the TUI, so the line was a
  frozen snapshot.
- The render path **still never makes a network call** (unchanged hard rule).
  Instead, after printing the cached snapshot, the statusline **triggers a
  detached, out-of-band refresh** (`statusline --refresh-usage`, hidden) that
  updates the cache for the next render — the Stream-Deck pattern: show
  last-good instantly, refresh behind the scenes.
- The trigger is **gated on the Live Usage opt-in**, only fires when the
  snapshot is going stale (≥ the usage TTL), and is **debounced by a short
  lockfile** so rapid prompts never fork a pile of refreshers. The usage
  Manager's existing TTL + post-429 cooldown remain the network rate limiters,
  so this adds no provider hammering.

### Documentation

- Reworked the README: a real dashboard hero plus dashboard / audit-trail /
  live-usage / statusline screenshots, the two flow diagrams redrawn as
  brand-colored Mermaid, an explicit "interactive TUI (mouse + keyboard) with
  full CLI parity" message, a step-by-step Claude Desktop skills import, a
  dedicated "Connect any MCP-capable agent" section with copy-paste JSON, and a
  config-files reference.

## [1.0.7]

### Added — Memory Organization tab

- **Dedicated "Memory Org" dashboard tab (`5`)**: an on-demand AI pass that
  consolidates, deduplicates, and re-files the memory vault behind a
  **preview-and-confirm** review. Nothing is written until the user approves.
- **Full provider + model picker**: any installed CLI agent (Claude, Codex,
  Gemini, Cursor, Antigravity) runs headless on the user's existing
  subscription — no API key — or a local/API endpoint (Ollama, OpenAI, Gemini,
  or any OpenAI-compatible Custom URL with auto-fetched models).
- **Per-file before/after review**: two-pane, color-coded add/remove diff;
  approve / reject / edit each file (`a` / `r` / `e`), `A` approves all, and
  approving auto-advances to the next undecided file.
- **Cancel a running organize**: `esc` on the running screen tears down the
  agent subprocess or HTTP request via context cancellation.
- **Pre-run preview**: the idle and confirm screens estimate how many tokens
  and files will be sent before launching.
- **Post-write confirmation + history**: the done screen lists exactly which
  files were written with `+added / -removed` counts; every write is recorded
  in the Audit Trail.
- **Audit Trail `Type` column + filter**: each entry is classified
  (Memory Org / Write / Approval / Session); press `f` to cycle the filter.

### Added — Claude Code statusline (`auxly statusline`)

- A productized Go renderer for a rich **4-line Claude Code statusline**:
  where (folder · branch · model · version), session (thinking · effort ·
  tokens · context bar), Auxly memory (link · role · last op · pending), and
  live Claude plan usage (5h + weekly bars with reset countdowns + freshness).
- **Never makes a network call** — the usage line reads only the last-good
  snapshot the Live Usage subsystem persists to disk.
- `auxly statusline` (full) · `--segment` (Auxly lines only) · `--wrap` (run the
  user's own statusline, then append the Auxly segment).
- `auxly statusline install [--wrap]` / `uninstall` — **additive + reversible**:
  backs up the user's prior command and restores it verbatim; atomic settings.json
  write; never destructively overwrites a non-Auxly statusline.
- **Settings → Customizations sub-tab**: opt-in selector (Use Auxly / Wrap mine /
  None) with a **live preview from real data**, a confirm dialog, and a
  Claude-Code-only banner. Applies in-process via the same install code as the CLI.
- Applying an option shows an **in-progress "Applying…" state** that holds until the
  write completes and the confirmation lands (the apply runs as an async command, so
  the UI never blocks and input is frozen until it resolves).

### Added — Dashboard

- **Recent Memory Changes feed**: the dashboard now shows the latest writes
  (time · type · file · ±lines) so it answers "what just happened" at a glance.
- **Memory by category**: a per-category breakdown with proportional bars,
  turning the bare entry total into a picture of the vault's composition.
- **Last-write freshness line** in the header (`✎ Last write: 4m ago · …`).
- The recent feed names the **writing agent** and shows a **date** on
  older rows; category bars have a visible track and tag the personal tier 🔒.
- **Pending approvals inline**: when the queue is non-empty, the waiting items
  are listed on the dashboard with a pointer to the Approvals tab.
- **Remote access scope**: each connected box shows what it can reach
  (`🔑 read · 6 file(s)`), resolved from the same sharing ACL the host enforces.
- All of these render only when the terminal has room; short panes keep the
  existing compact layout (the responsive fit guarantee is preserved).

### Changed

- Organize is scoped to **user-memory files only** on both input and output —
  agent setup/instruction files (`agents.md`, `CLAUDE.md`, `AGENTS.md`,
  `providers.md`, the generated aggregate) are never read or rewritten.
- Organize moved out of Settings into its own tab; Settings keeps trust
  levels, Live Usage, and the Agents sub-tab.
- Dashboard tab order is now Dashboard · Activity · Files · Approvals ·
  Memory Org · Analytics · Settings · Remote · Skills · Audit Trail (`0`).

### Fixed

- **Codex** now runs outside a git repo via `--skip-git-repo-check` (was
  exiting 1 in the isolated working directory).
- **Resilient JSON parsing** of agent output: a string-aware extractor returns
  the first balanced object (dropping surrounding prose/log noise), and a
  lenient repair pass salvages unescaped quotes/newlines that weaker models
  emit — the most common cause of "invalid character … after key:value pair".
- The agent-run timeout is detected on the run context (not the parent), and a
  user cancel is reported distinctly from a genuine failure.
- The Direct-LLM path honors the cancel/timeout context for its HTTP request.
- Fixed a doubled `Authorization: Bearer` header on the Direct-LLM path.

### Security

- Organize agents run with **no tools**, an **empty working directory**, a
  **scrubbed environment** (no `AUXLY_*`), and a **read-only sandbox** where
  supported, so a spawned agent can never locate or write the real vault on its
  own. The vault payload is passed as a positional argument / JSON body, never
  shell-expanded. The preview-and-confirm gate is the safety boundary:
  `ApplyOrganizeChanges` is the only writer, and it runs only after explicit
  per-file approval.

### Foundation (earlier in this line)

- Added the plan/apply split for memory organization (preview proposal vs.
  apply), the organize safety gate, and `IsOrganizableFile` scoping.
- Scrubbed hardcoded PII from source and test fixtures for public release.

## [1.0.5]

- Match remote ACL by hostname/name, not just target IP.

## [1.0.4]

- TUI: cap the agent grid at 3 columns, auto-refresh the Remote screen, and
  dedupe connections.

## [1.0.3]

- TUI: repaint the content viewport in sub-mode key handlers.

[1.0.9]: https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/releases/tag/v1.0.9
[1.0.8]: https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/releases/tag/v1.0.8
[1.0.7]: https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/releases/tag/v1.0.7
[1.0.5]: https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/releases/tag/v1.0.5
[1.0.4]: https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/releases/tag/v1.0.4
[1.0.3]: https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/releases/tag/v1.0.3

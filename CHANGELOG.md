# Changelog

All notable changes to Auxly Memory CLI are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.11] - 2026-06-05

Polish release: a vault **export** to ~/Downloads, a dashboard **force-update** for
busy boxes, a tool-wide pass on **progress bars** — one shared `▰/▱` style, honest
motion, and live activity during long opaque waits — and a richer **statusline** that
now carries git working-tree state.

### Added

- **The statusline shows Auxly's own version and an update hint.** Line 3 now leads with
  `💾 Auxly v1.0.11`, and when a newer release is available it appends an amber `⬆ <version>`
  (e.g. `💾 Auxly v1.0.11 ⬆ 1.0.12`). The check is **network-free** — it reads only the cached
  update result the CLI/TUI already refresh (`~/.auxly/.update-check.json`) via the new
  `update.Cached()` accessor, never fetching at render time — and the brand+version segment
  is pinned so it survives even on a narrow terminal. (Note: line 1's `🔖 v…` is the *agent's*
  version, e.g. Claude Code's; this is Auxly's own.)
- **The statusline is now responsive — it fits the terminal width instead of wrapping.**
  Each of the four lines is built from prioritized segments; when the terminal is too narrow
  for a line, its lowest-priority segments are dropped (then a clean ellipsis truncation as a
  last resort) so every line stays on a single row. Line 1 sheds `🪙 tokens` / `🔖 version`
  before `📊 context` / `🤖 model` (folder is pinned); line 4 drops the freshness stamp and
  trailing usage windows before the `🔋` brand and the session window. Width comes from
  `$COLUMNS` then a non-blocking `/dev/tty` ioctl; when it can't be determined, lines render
  unconstrained exactly as before. Width is measured ANSI- and emoji-aware (go-runewidth),
  so colour codes and wide glyphs are counted correctly.
- **The statusline now has dedicated lines per concern.** Line 1 = agent + context (folder ·
  model · effort · thinking · `🪙` tokens/window `out:N` · `📊` context bar · version);
  line 2 = git only (branch · ahead/behind · changed `+`/`-` · commit · age); line 3 = Auxly
  memory; line 4 = plan usage. Git context moved off line 1 onto its own line, and the
  session/context details merged up onto line 1. (`out:N` is the assistant's **output**
  tokens, shown only when the agent reports them.)
- **The git line shows rich working-tree state.** Next to the branch it surfaces:
  **ahead/behind** vs upstream (`↑2 ↓1` unpushed/unpulled, shown only when nonzero), the
  count of **changed files** (`📝 N`), the **`+added` / `-removed`** line totals of
  uncommitted work (green / red), and the short **HEAD hash with its relative age**
  (`⌥ bc5b1ae · 5h`) — e.g. `🌿 dev ↑2 📝 26 +1289 -55  ⌥ bc5b1ae · 5h`.
  The line totals include **untracked new files** (every line of a brand-new file is an
  addition), so the numbers match what your shell/Warp git segment reports rather than
  under-counting tracked-only changes. Everything degrades independently and all git reads
  share one hard 500 ms deadline — untracked scanning is bounded (file count + per-file
  bytes, binaries skipped) — so a slow/stuck or huge repo can never freeze the terminal.

### Fixed

- **The banner's “Auxly-Memory CLI” and “Tzamun Arabia IT Co” are clickable links again.**
  Each name carries its OSC-8 terminal hyperlink and clicks through to its site. Two bugs
  had to be fixed: (1) wrapping the string in OSC-8 *first* and then styling it shreds the
  escape (leaks the raw URL as text) — so the plain label is styled first and the OSC-8
  wraps it from the **outside**; (2) more subtly, `lipgloss.Underline` renders the text
  **character by character** with a `\x1b[0m` reset between every letter, and those interior
  resets fragment the OSC-8 hyperlink span — so terminals (Warp especially) showed the
  underline but dropped the click. The link cue is now **bold + accent colour**, which keeps
  the name one contiguous styled span inside a single clean hyperlink region.
- **The box-update progress bar no longer sits at 0% the whole time.** The Remote-tab
  progress bar advances by recognising milestone markers in the streamed output, but it
  only knew the *connect/doctor* flow's markers — so a `host update` (or reconnect /
  forget) run, whose lines are `Updating X (a → b)…` → `X updated to …` → `statusline
  applied`, mapped to 0% until the final jump to 100%. Those host-action markers now
  advance the bar (≈55% → 90% → 95% → done).
- **The progress bar no longer freezes at one number then jumps to done.** Short SSH
  actions like **reconnect** run their heavy phase (`connect auto`) entirely server-side
  with output captured, not streamed — so the TUI is blind to it and the bar sat at 35%
  until completion. The bar now **creeps forward continuously** between observed
  milestones (decelerating toward 90%, only the done event reaches 100%), so it always
  shows motion; the reconnect's completion line is also a recognised milestone now.

### Changed

- **Unified loading indicators across the tool.** Every in-flight operation now shows the
  same `▰/▱` motion: **Memory Org's "Organizing" screen** gained the identical creeping
  bar as the Remote tab; the dashboard's **self-update** and **update-all-boxes** banners
  gained a sweeping marquee instead of a bare `⏳ …` line. Together with the earlier glyph
  unification, every bar in the tool now shares one look and one motion model.
- **Loading bars stay visibly alive during long opaque waits.** A creeping bar used to
  rush to its ceiling and then sit frozen (e.g. the Memory Org model run holding at 90%
  for a minute). Now (a) a moving **glint** sweeps the filled portion so the bar keeps
  animating even while the fill holds, and (b) the creep is two-phase — a brisk ramp to
  ~80% then a slow frame-throttled crawl toward 97% — so the number itself keeps inching
  instead of slamming to the top.
- **Onboarding wizard progress bars now reflect real state** (full audit of every bar in
  the TUI). The first-run **migration** bar used a hard-coded denominator and the step
  advanced the instant the (atomic) migration finished, so it was *always* drawn at 0% —
  it's now an **animated marquee** that shows activity. The **onboarding** bar was a
  meaningless looping animation (`spinFrame % 20`); it now fills by how many agents have
  actually resolved (pending → success/auth/manual). All other bars (usage meters,
  analytics charts, memory-composition, statusline threshold) were verified correct.
- **Every bar in the TUI now shares one look** — the `▰`/`▱` half-block meter. The
  connect/update progress bar and the "Memory by category" composition histogram used
  solid `█`/`░` blocks; the analytics charts used bare `█` with no track. They all now
  route through a single `renderMeter` primitive (filled `▰` in a semantic colour, the
  remainder dimmed `▱`), matching the usage meters and the statusline threshold bar.
  Change the glyphs in one place and every bar follows.

### Added

- **Export your whole memory vault to ~/Downloads.** New `auxly export [--dest <dir>]`
  command and a **`[e]` action on the Files tab** copy every memory `.md` into a fresh
  **timestamped folder** (`auxly-memory-export-<when>/`). Each file is **tagged with its
  name and the export time** three ways — the folder name, the file name
  (`projects__2026-06-05_021718.md`), and a header comment inside the file — and a
  `MANIFEST.txt` records the set. Unreadable files are skipped rather than aborting the
  export.
- **Dashboard `[f]` force-update for all boxes.** The connected-box update prompt now
  offers `[B]` (update idle boxes) and `[f]` (force all, including live ones — ends
  their session). Previously the dashboard only had `[B]`, which silently skips live
  boxes, so a fleet whose outdated boxes were all serving sessions could never be
  updated from the dashboard. `auxly host update --all --force` now honors `--force`
  for the whole sweep (it was ignored before — only the single-box path used it).

## [1.0.10] - 2026-06-04

Fleet-management release: manage, update, and wire your remote boxes from the host;
a relay-first connect wizard with a per-file permissions step; usage that actually
renders on remote statuslines across versions; and a cleaner Settings → General.

### Added — Manage remote-box updates + permissions from the host

- **Per-box "update available" badge** on the **Remote** tab: a throttled, async SSH
  sweep (`auxly host versions`) flags any connected box running an older auxly as
  `⬆ 1.0.9→1.0.10`. Network-bound work stays off the UI thread and only runs when
  you're a host with boxes (no boxes ⇒ no SSH, no `/version` call — still local-first).
- **One-click box update**: press **`u`** on a box to bump its auxly over SSH
  (`auxly host update <name>`), or **`B`** on the **Dashboard** to update *all*
  outdated boxes at once. A box **serving a live session is skipped** (use
  `--force` for a single box). The Dashboard shows a prompt — *"N connected boxes
  need an update — press [B]"* — whenever any box is behind.
- **Per-box permission column**: each connected box now shows its effective memory
  access — `read-only`, `read+write`, `read+write·Nf` (per-file grant), or
  `read+write*` (via the default below).
- New `auxly host versions [--json]` and `auxly host update [name|--all] [--force]`
  commands back the above (and are scriptable on their own). A skipped (live) box
  offers an in-TUI **`[f]` force-update** instead of dropping to a terminal.
- **The remote statusline mirrors your preference**: updating a box installs its
  statusline in the **same wrap-vs-replace mode** you use locally, and — when you
  use **Live Usage** — makes the box's **plan-usage line actually render**, across
  versions. On a box new enough to take `statusline install --enable-usage` the
  opt-in is **persisted** (the box self-refreshes); on any box it then **primes the
  usage cache during sync** by running the box's `statusline --refresh-usage`, so the
  usage line shows on the very next render even on boxes that can't store the opt-in
  (the render gates on cached data, not the setting). The per-box result is reported
  honestly — "Live Usage on the box", "usage refreshed now (update the box to keep it
  self-refreshing)", or "couldn't be primed — the box needs a fetchable agent token".
- **Statusline sync manager** in **Settings → Customizations** (press `s`): a master
  **auto-sync** toggle (push your statusline to boxes whenever you change it),
  **per-box** include/exclude, **all/none**, and **sync-now** — all boxes (`y`) or
  one at a time (`⏎`). Backed by `auxly host statusline [names…|--all]`. A box is
  re-synced even when it's already on the latest version (config edits are
  non-disruptive), so the statusline/usage applies without a version bump.

### Changed — Connect wizard puts relay first and adds a permissions step

- **Relay is now the first connection method** (`1`) in the **`c` → Connect** wizard —
  it's the primary flow (serve THIS Mac's memory to a NAT'd/shared box). lan/vpn/
  bastion/public shift to `2`–`5`.
- **The relay flow has a permissions step right after the name.** The wizard is now
  `method → relay server → name → permissions → connect`: before anything is set up,
  you pick what the box may access per file — **Off / Read / Read+Write** (`←/→` to
  cycle, `a` all-read, `n` none). Non-personal files default to **Read+Write** (a
  connected box is normally a full peer of your memory); `personal.md` is **Off** by
  default with its exposure warning. The choice is applied to the box once provisioned, and
  the result panel confirms how many files were made readable/writable. (Consumer
  methods, where this Mac only *reads another host's* memory, have nothing to share
  and submit straight from the name step.) The standalone `s` "Share files" action on
  an existing box is unchanged.

### Fixed

- **Settings → General navigation no longer feels stuck on the Live Usage row.**
  Live Usage and Auto-Update were two separate cursor stops rendered on the *same*
  line, so pressing ↓ moved the cursor sideways within one line instead of to a new
  row. They now render on **separate lines** (on normal-height terminals), so ↑/↓
  moves one visible row at a time; a click on either row toggles the right one. Short
  terminals keep both on one line to preserve the no-scroll fit guarantee.
- **No more phantom "Claude Code (Recommended)" agent card.** Memory Org was
  attributing its writes to the picker's *display label*, which the dashboard then
  rendered as a bogus agent. Writes are now attributed to the canonical brand id,
  and historical entries fold into the real **Claude Code CLI** card.

### Added — Opt-in default read/write for known remotes

- **`defaultRemoteWrite`** setting (off by default): when on, a **known** box
  (listed in `clients.yaml`) with no explicit per-file grant defaults to
  **read+write** instead of read-only. The sharing model stays fail-closed for
  everyone else — an **unmatched/unknown remote is never granted write** by this
  flag, and an explicit per-file `write_files` grant always takes precedence.

### Added — Keep the remote current as part of connecting (opt-in)

- **`connect --update-remote`** (and a persisted **`updateRemotesOnConnect`**
  setting, off by default): when this machine connects to a host and finds an
  **older auxly** there, it now bumps it **in place over the same SSH** the
  connection doctor already uses — no separate `auxly update` on the remote. It is
  **driven from the connecting (already-updated) side**, so it works even when the
  remote is too old to self-update.
- The same step **ensures the Auxly statusline on the remote** for its detected
  agents (`auxly statusline install --agent all` over SSH).
- **Live-session guard:** a host that's serving a live `mcp-server` relay is
  **skipped** (it'll pick up the update on its next idle connect) so a binary is
  never swapped out from under an active session. Everything is best-effort and
  narrated — a failure never aborts the connect.

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

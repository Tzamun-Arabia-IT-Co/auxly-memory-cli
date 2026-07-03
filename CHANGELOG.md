# Changelog

All notable changes to Auxly Memory CLI are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.3.1] - 2026-07-03

A UX and performance follow-up to 1.3.0, driven by real-vault feedback. Faster
memory organization, a simpler password-based encryption option, a stable
dashboard, and full TUI parity — every organize mode and user-facing command is
now reachable from the dashboard, not just the CLI. Each change shipped through
an adversarial review pass; the temporary-decrypt path alone caught 6 findings
(2 critical) before merge.

### Added

- **Password-based encryption.** `auxly encrypt init --passphrase` lets you
  encrypt the vault with your own password (age scrypt) instead of managing a
  keypair and a 60-character backup key. The password is cached in the OS
  keychain so reads stay transparent, and `AUXLY_VAULT_PASSPHRASE` unlocks
  headless runs. The keypair mode remains the default. A forgotten passphrase
  has no recovery — the init prompt says so plainly.
- **Organize encrypted files without a dead end.** When a CLI-agent organize run
  meets encrypted files, you can now choose to skip them for that run, or
  temporarily decrypt them — they are re-encrypted automatically when the run
  finishes. A crash-recovery marker re-encrypts anything left plaintext by an
  interrupted run on the next `auxly doctor` or dashboard open, and git sync
  refuses to commit while a temporary decrypt is in progress.
- **All organize modes in the TUI.** The Memory Org tab gained a mode selector —
  Consolidate, Split projects, and Find contradictions all run from the
  dashboard now, with results routed to the Approvals tab for review.
- **Full TUI parity.** Vault encryption (status, init, per-file encrypt/decrypt),
  index status and rebuild, trust-tuning suggestions, `auxly join` pairing, git
  sync, capture-hook install/status, and the doctor report are all reachable
  from the dashboard. Only inter-process plumbing stays CLI-only.

### Changed

- **Memory organize is much faster.** Root-caused with direct timing: the model
  spent ~90s *deliberating* to emit a handful of edits, not generating output.
  Organize now runs the Claude Code agent at `--effort low` (mechanical re-filing
  needs no deep reasoning) and emits move/merge/delete **operations** instead of
  rewriting every file's full content — on a real ~16k-token vault a first run
  dropped from 136s (or a 280s+ timeout when it chunked) to ~50–95s. A content-hash
  ledger then skips unchanged files, so every rerun after the first is instant.
  Tune with `AUXLY_ORGANIZE_EFFORT` / `AUXLY_ORGANIZE_DELTA=0`.
- **`[F]` re-run everything.** After a run, the dirty-file ledger correctly reports
  "already tidy — nothing changed"; `F` on the Memory Org screen forces a full
  re-organize when you want one, so the skip is never a dead end.
- **Split a header-organized `projects.md` by its sections.** A `projects.md`
  arranged under `## Project` headers now splits deterministically by those
  headers — each section moves verbatim into `projects/<slug>.md`, no LLM guessing,
  no per-bullet fragility. Flat files still use the LLM attribution path.

### Fixed

- **Splitting a bold-bulleted `projects.md` no longer fails every run.** The
  split guard rejected any bullet the model returned with its `**bold**` markers
  stripped, aborting the whole migration. It now matches on an emphasis-normalized
  form while writing the original text verbatim; an unmatched bullet stays safely
  in `projects.md` and is reported instead of killing the run, and nested bullets
  move as a unit.
- **The dashboard no longer hides sections at random.** A momentary empty audit
  or store read used to blank the memory-by-category bars, last-write line, and
  activity feed until the next refresh; populated sections are now retained, the
  first paint shows structure instead of a blank skeleton, and the live activity
  feed is the last thing dropped under height pressure so recent changes stay
  visible.
- **`auxly host invite` copies the token to your clipboard** on mint (and `[y]`
  in the TUI), so a long single-use token no longer has to be selected by hand.

### Security

- The temporary-decrypt organize path was hardened before release: git sync and
  auto-commit refuse while plaintext is exposed (a concurrent commit would
  otherwise push it into history permanently), the crash-recovery marker is a
  locked merged set so concurrent runs can't erase each other's recovery, consent
  is explicit that decrypted content is briefly visible on the process command
  line, and the TUI blocks quitting mid-re-encrypt.
- TUI encryption input is isolated from global keys — entering a passphrase that
  contains a digit or `q` can no longer switch tabs or quit the app mid-entry,
  and key material is cleared from memory when you leave the panel.

## [1.3.0] - 2026-07-03

The first release with vault encryption at rest, one-command SSH pairing,
interactive recall playgrounds, fact decay & review, and automated contradiction
sweeps. Vault security, analytics privacy, and agent trust-tuning are front and
center. Every sprint shipped through an adversarial review pass — 70+ findings
across ten sprints, every one fixed before merge.

### Added

- **Vault encryption at rest.** Run `auxly encrypt init` to initialize standard `age` X25519 encryption. Keys live securely in the macOS Keychain (using stdin via `security -i` to avoid argv exposure) or a `0600` file fallback outside the vault, making git sync safe for ciphertext. Supports per-file encryption, decryption, and status checks. It is strictly fail-closed: decryption failure never returns empty content, and CLI-agent organize passes are refused on encrypted files.
- **One-command pairing.** Pair a consumer box to a memory host via `auxly host invite` (TUI `[i]`), generating a secure single-use token pinned to the host SSH key. Joining with `auxly join <token>` validates the pin directly on the connection using temporary `known_hosts` and strict host-key checking, provisioning the link and burning the invite atomically. Note that pairing authorizes Auxly access, not OS-level user accounts.
- **Fact decay & review.** Prevent memory bloat with `auxly review` and the TUI Review tab (`-` key). Both list stale facts (old and recall-silent) tracked via a first-seen ledger so consolidation rewrites do not reset fact age, allowing you to archive them to `.archive/` (never deleted) or restamp them.
- **Recall playground.** Access the playground with `?` in the TUI Memory tab to run experimental queries through the real recall pipeline. Displays score bars and floor cuts, and lets you cycle via `Tab` to preview what specific client ACL lenses allow them to read. Experimental queries never write to recall analytics.
- **Recall analytics.** Every agent recall is recorded as query and per-fact SHA256 hashes only — raw text never touches the database. Powers per-file hit stats, fallback-rate health indicators, and `auxly stats --recall` metrics.
- **Contradiction sweep.** Spot conflicting or redundant facts across files using `auxly organize --contradictions`. Leverages embeddings to find similar pairs judged by a single LLM call; losing sides are queued as pending with verdict comments, and superseded facts retain a dated trace instead of being erased.
- **Trust auto-tuning.** Approve/reject decisions accumulate as per-agent evidence; `auxly trust suggest` recommends promotions to `auto` (≥50 decisions, ≥95% approved) or demotions to `read_only` (≥30% rejected), surfaced in `auxly doctor` and the TUI settings. Opt out via `tuning: off`.
- **Memory browser.** Browse and edit your entire memory vault directly inside the TUI's Memory tab (`=` key). Edits and deletions flow through the pending queue for verification, and duplicate-content deletions are rejected.
- **Approvals upgrade.** Features colorized diffs, TTL countdown badges, and bulk approvals by agent (`A`) or file (`F`) with conflict-skip safety. TUI approval actions now count toward trust evidence.
- **Dashboard upgrades.** Introduces a 1-second incremental, deduped live activity feed, a 30-day vault-size sparkline, and 7-day per-agent write bars.
- **Capture parity.** `auxly hooks install --agent claude|codex|gemini|kimi` extracts message text only, filters tool output, and generates injection-safe shell wrappers.

### Fixed

- **System hardening.** Configures SQLite `audit.db` write-ahead logging (WAL) and `busy_timeout` for reliable multi-process operations.
- Over 70 adversarial-review findings fixed across sprints S12 to S21 before merging.

## [1.2.0] - 2026-07-02

The biggest release since 1.0: memory data-safety, per-project memory,
seamless remote setup with self-healing links, passive auto-capture, and the
groundwork for signed Windows releases. Every sprint shipped through an
adversarial review pass — 26 findings caught and fixed before release.

### Added

- **Per-project memory files.** Project facts now live in `projects/<slug>.md`
  (one file per repo/product) instead of one growing monolith. Agents route
  project facts automatically from the workspace; list/recall/organize/sharing
  all understand the new layout.
- **`auxly organize --split-projects`.** One command migrates an existing
  `projects.md` monolith into per-project files. Safety-first by construction:
  the original is backed up, every piece goes through the pending queue for
  your approval, a mechanical permutation check rejects any dropped or reworded
  fact (no force override), and the monolith cleanup is only ever queued for
  bullets already approved into a sub-file — a rejected split can never lose a
  fact.
- **Passive auto-capture (opt-in).** `auxly hooks install` adds a Claude Code
  Stop hook that runs `auxly capture` after each session: an LLM pass extracts
  durable facts from the transcript and queues them as pending changes — memory
  learns from your sessions, you still approve every write.
- **Session primer.** `skill_init` now returns a compact "who you are / top
  preferences / this project / last 7 days" primer (~800 tokens, ACL-gated) so
  every session starts grounded without a manual recall.
- **Write-time supersede.** When a new fact contradicts an existing one in the
  same file, the old line is replaced with a dated `(updated …; was: …)` trace
  instead of piling up stale duplicates. Disable with `AUXLY_SUPERSEDE=off`.
- **Pending queue, grown up.** `auxly pending` table (agent, target, age, ±);
  agent attribution on every entry; bulk `auxly approve|reject --all / --agent
  <name> / --file <target>`; expired entries auto-archive after 30 days
  (`AUXLY_PENDING_TTL_DAYS`, 0 disables); trust changes are audit-logged.
- **Seamless remote connect.** `auxly connect <box>` now provisions the box
  end-to-end from the host's screen — installs auxly, wires its agents to THIS
  machine's memory, and proves the link with a real end-to-end read
  (`connect-mcp --selftest`) before claiming success. The old
  standalone-vault behavior lives under `--standalone`.
- **Self-healing remote links.** Keep-alive service self-heals on any long-
  lived auxly start; tunnels reconnect with exponential backoff and audit after
  repeated failures; an hourly reconciler re-wires drifted clients; a fallback
  chain recovers moved host binaries; agents on a box show a "MEMORY LINK LOST"
  banner instead of silently reading stale local files.
- **Remote health at a glance.** `auxly host clients` health table
  (reachable/auxly/wired/link/last-activity), `auxly host versions --health`,
  live link verdicts in the TUI Remote tab, `auxly doctor` probes every remote
  memory link with the real selftest and flags duplicate/orphan topology.
- **Simpler connect wizard.** Three choices instead of five (relay / direct /
  bastion) — "direct" auto-detects LAN vs public from the address; the OS
  question is gone (auto-probed and persisted); the host field Tab-completes
  from `~/.ssh/config`.
- **Recall quality.** Relevance floor (default 0.35, `AUXLY_RECALL_MIN_SCORE`)
  with substring fallback instead of confidently-wrong low-score hits; gentle
  recency boost; index handle reuse + freshness signature make repeat recalls
  markedly faster on large vaults.
- **Windows trust groundwork.** Release pipeline carries a dormant SignPath
  Authenticode signing hook (activates when credentials land) and RELEASING.md
  documents the full $0 path: SignPath OSS signing, Defender false-positive
  submission, winget distribution.

### Fixed

- **Data-safety core (Sprint 1).** Atomic vault writes, cross-process locking,
  conflict detection on approve, and a fact-loss guard on organize.
- `--force` combined with bulk approve flags is now rejected instead of
  silently force-applying every conflicted entry.
- Provisioning failures no longer hide behind a green "provisioned!" banner —
  every wiring error surfaces and fails the command.
- The first approve targeting a brand-new `projects/<slug>.md` no longer fails
  with a missing-directory error.
- An explicit sharing grant naming `projects.md` now also covers
  `projects/<slug>.md` sub-files instead of silently revoking access after a
  split.
- OS auto-detection actually persists now — profiles no longer default to
  "linux", which had blocked detection since the feature shipped.
- Retrying a connection method no longer wipes a previously declared OS;
  bracketed IPv6 addresses parse correctly in the wizard.
- Windows release exe embeds PE version-info to reduce Defender false
  positives.

## [1.1.5] - 2026-06-17

Fixes Memory Organize hanging / failing with CLI agents. The organize code was
unchanged since 1.1.2, but newer agent CLIs changed behavior — they narrate
("Let me read the files…", "the input was truncated, I'll use the MCP tools")
instead of emitting the JSON the consolidation expects, or they refuse to run
headless at all. Verified working end-to-end with Claude, Codex, Gemini,
Antigravity (agy), and Cursor.

### Fixed

- **Agents narrating instead of returning JSON.** A blunt RESPONSE CONTRACT is now
  front-loaded at the top of the organize prompt: it tells the agent it is a
  non-interactive text→JSON transformer, the vault is complete in the prompt
  (nothing truncated, no files to read, no tools), and its entire reply must be one
  JSON object. This stops the "Let me read… / input truncated / use MCP tools"
  failure that produced `invalid character 'L' looking for beginning of value`.
- **Auto-retry on a non-JSON reply.** If the first reply still isn't valid JSON, the
  run retries once with a corrective ("your previous reply was prose, JSON only now")
  before failing — a safety net for stubborn agents.
- **Cursor never ran headless.** Cursor blocked on a "Workspace Trust Required" prompt
  in organize's isolated temp dir and produced no output. It now runs with
  `--mode ask` (read-only Q&A — no shell, no edits, so a prompt-injection in the vault
  has no tools to abuse) paired with `--trust`. Security test enforces the pairing:
  `--trust` is never allowed without read-only `--mode ask`.
- **Larger vaults timing out.** Default organize timeout raised from 600s to 900s
  (still overridable via `AUXLY_ORGANIZE_TIMEOUT`) so a full-vault consolidation has
  room to finish. For very large vaults a fast direct-API provider (Gemini/OpenAI) or
  trimming bloated files remains the quickest path.

## [1.1.4] - 2026-06-16

Adds Auxly slash-command **skills** to the Kimi Code CLI. The MCP server already
auto-configured for Kimi; this completes the experience so `/auxly-init`,
`/auxly-sync`, `/auxly-memory`, and the rest are available there too — no manual
steps, exactly like Claude and Cursor.

### Fixed

- **Kimi Code CLI skills now auto-install.** `installAuxlySkills` only wrote
  `SKILL.md` folders for Claude/Codex/Gemini, so Kimi got the MCP server but none
  of the slash commands. Kimi also requires the skills directory to be **registered**
  (dropping files in a conventional folder isn't enough). Setup now writes all Auxly
  skills to `<kimiHome>/auxly-skills/<name>/SKILL.md` and registers that path in
  `extra_skill_dirs` of Kimi's `config.toml` via an idempotent in-place edit (handles
  both the current `~/.kimi-code` and legacy `~/.kimi` homes). Runs on both
  `auxly setup` and `auxly connect`, so new installs are wired automatically.

## [1.1.3] - 2026-06-15

Follow-up to 1.1.2 — smoother updates for package-manager installs and on Windows.

### Fixed

- **`auxly update` on npm/pip installs.** It now detects a package-manager-vendored
  binary (npm under `node_modules/auxly-cli/`, pip under `~/.cache/auxly/`) and
  directs you to the owning manager (`npm install -g auxly-cli@latest` /
  `pip install -U auxly-cli`) instead of attempting an in-place self-replace — which
  the manager would clobber anyway, and which failed with "Access is denied" on a
  locked Windows `.exe`. The dashboard `[U]` update applies the same redirect.
- **OS-aware install hint.** When a self-update can't proceed, the fallback hint now
  shows the Windows PowerShell installer (`irm https://auxly.io/cli.ps1 | iex`)
  instead of the Unix `curl … | sh` line.

## [1.1.2] - 2026-06-15

**Windows support, fixed end-to-end** — the full setup → MCP → statusline → dashboard
flow now matches macOS/Linux. Every change is `GOOS`-gated or reuses an existing
cross-platform helper, so macOS/Linux behavior is unchanged.

### Fixed

- **Claude Desktop / IDE MCP config now written on Windows.** `knownIDETargets` built
  config paths from a raw `%APPDATA%` with no fallback; when `APPDATA` was empty
  (non-interactive / SSH-provisioned sessions) the paths collapsed to bare relative
  strings that `writeMCPConfigEntry` silently skipped. Paths now route through
  `detect.AppSupportDir` (which falls back to `…\AppData\Roaming`), so the written path
  matches the detected one for Claude Desktop, Cursor, and Antigravity.
- **Statusline renders on Windows.** The installed `statusLine.command` used a backslash
  Windows path that the agent's POSIX shell (sh/bash via Git Bash) mangled into a broken
  path, so the statusline showed blank even though the exe ran fine directly. The path is
  now normalized to forward slashes, which cmd.exe, PowerShell, and sh all accept.
- **MCP stability for env-less servers.** Provider attribution cold-started a PowerShell
  CIM query on every logged request when `AUXLY_PROVIDER` was unset (e.g. Claude Code CLI),
  stalling strict clients into closing. It is now memoized once per process.
- **Self-update on Windows.** `SelfUpdate` (and dev-mode `auxly update`) renamed a file
  *over* the running, locked `.exe` and failed; they now rename the live image aside
  first, with rollback on error.
- **Host tunnel status on Windows.** `hostTunnelsLive` used the Unix-only `pgrep`, so a
  Windows host always showed "tunnels down"; it now enumerates `ssh.exe` via CIM.
- **Agent auto-detection on Windows.** The onboarding wizard split `PATH` on `:` and never
  appended `.exe`, so it found no installed CLIs; it now uses `exec.LookPath` (correct `;`
  separator + `PATHEXT`).
- **`quoteIfNeeded`** now also quotes cmd.exe / POSIX shell metacharacters in the
  statusline command path.

### Changed

- **`auxly setup` now auto-installs the statusline** for any detected agent that has none
  (idempotent, non-destructive — a user's own statusline is left alone). Previously only
  `auxly connect` wired it, so a fresh local setup — the canonical Windows onboarding path
  — never configured one.
- **Faster dashboard on Windows.** Live-session discovery, attribution, and liveness now
  come from a single cached process snapshot per refresh, replacing the previous
  one-PowerShell-per-connection storm on every 1-second tick.

## [1.1.1] - 2026-06-10

### Fixed

- **Reconnect self-heals a downed host tunnel.** Pressing `[r]` Reconnect in the TUI now
  brings the host keep-alive online first, so re-wiring a box succeeds even when the
  reverse tunnel was the very thing that was down. `connect` now warns instead of
  hard-failing when the tunnel is temporarily unreachable, and the TUI shows an honest
  `● tunnels down` indicator.

## [1.1.0] - 2026-06-09

**Semantic Recall — find relevant memory by meaning, not keyword.**

### Added

- **`auxly_memory_recall` MCP tool.** A new tool that surfaces the most relevant
  memory snippets for a natural-language query using cosine similarity over a local
  vector index — ask "what do I remember about my staging server?" and Auxly returns
  attributed chunks (file + line range), not just exact keyword hits. When no embedding
  model is available the tool falls back to substring search so it always returns
  something useful, even fully offline.

- **Local SQLite BLOB vector index.** Memory is chunked by heading, content-hashed, and
  stored as embedding vectors in `semantic.db` alongside the audit log. The index is
  built lazily on first recall call — no setup required. Explicit rebuild:
  `auxly index rebuild`. Inspect with `auxly index status` (provider, model, dim,
  chunk count).

- **Markdown chunker.** Splits memory files into heading-delimited chunks with content
  hashing and line-range provenance, so recall results can be attributed back to their
  exact location in the source file.

- **Embedding client — local-first with cloud opt-in.** Runs against a local embedding
  model (Ollama `nomic-embed-text` by default) with a 500 ms timeout. Cloud endpoint
  requires explicit opt-in (`AUXLY_EMBED_ALLOW_CLOUD=1`). Off by default — memory never
  leaves your machine unless you enable it.

- **Per-process circuit breaker.** If the embedding model is slow or offline the circuit
  breaker opens after the first timeout and recall falls back to substring search for the
  rest of the process lifetime — no latency penalty on subsequent calls.

- **`auxly index rebuild` / `auxly index status`.** CLI commands to wipe-and-rebuild the
  semantic index from scratch, or to inspect the current index (provider, model, dimension,
  chunk count) without triggering a rebuild.

- **ACL on recall.** The same per-file `canRead` guard that governs keyword search
  pre-filters the vector index at load time *and* at render time — remote/shared sessions
  can never surface files they aren't allowed to read; `unified_memory.md` is
  hard-excluded at both layers.

### Fixed

- **Orphaned and shadowed chunks pruned on write.** When a memory file is updated, stale
  vectors for overwritten or removed content are removed in the same transaction.
  WAL mode is now enabled on the semantic index for better concurrent read throughput.

- **Cloud embed calls gated on effective URL host.** The circuit breaker no longer trips
  on localhost overrides (`AUXLY_LLM_BASE=http://localhost/…`); prevents accidental
  cloud calls in dev/test environments.

- **SSH connect-wizard viewport repaints on keypress.** The connect form no longer blanks
  out while the user types — a `tea.ClearScreen` was firing on every input event, now it
  only fires when the wizard mode changes.

- **MCP sync responses are ~100 k tokens leaner per session.** Write confirmations no
  longer echo the full `unified_memory.md` (47 KB / ~12 k tokens) back to the agent on
  every write. All non-onboarding, non-bootstrap tool responses now use a compact
  one-line footer instead of the full taxonomy table (~225 tokens), cutting token
  accumulation by ~100 k tokens across a typical `/auxly-max` session.

## [1.0.22] - 2026-06-07

**Dashboard: the Active Connections list no longer hides the rich panels — it scrolls in place.**

### Fixed

- **A long list of connected remote boxes no longer pushes the dashboard into compact
  mode.** Previously, each remote took two lines in the full layout, so a handful of
  connected machines made the left column tall enough to drop the **Memory by Category**
  bars and the **Recent Memory Changes** feed (the dashboard fell back to a stripped-down
  layout with empty space below). The Active Connections panel is now **height-bounded and
  scrolls in place**, so the rich panels stay visible at the same terminal size.

### Added

- **Scrollable Active Connections panel.** When more remotes are connected than fit, the
  panel shows a bold-amber `▲ N more above` / `▼ N more below — Shift+J` affordance and
  scrolls within itself. Scroll with **`Shift+J` / `Shift+K`** (advertised; lowercase
  `j`/`k` still navigate the agent cards), or the universal fallbacks **`PgDn`/`PgUp`**,
  **`Ctrl+↓`/`Ctrl+↑`**, and the **mouse wheel** over the left box — so it works on any
  keyboard, including mouseless Linux/SSH sessions and compact layouts without Page keys.

## [1.0.21] - 2026-06-06

**npm + pip distribution channels, plus install-robustness hardening.**

### Added

- **`npm install -g auxly-cli`** and **`pip install auxly-cli`** — thin wrapper packages
  that download the prebuilt auxly binary for your platform and **verify it against the
  minisign-signed checksum manifest** (same pinned key as the Go binary) before use. npm
  verifies in-process with Node stdlib (BLAKE2b-512 + ed25519, zero deps); pip ships a
  pure-Python wheel that downloads + verifies on first run and caches under `~/.cache/auxly`.
  Both **require a valid signature by default** (`AUXLY_ALLOW_UNSIGNED=1` to override).

### Fixed

- **Installers no longer fail-closed on a non-manifest 200.** The staged-verify logic
  assumed a 404 for an absent checksum manifest, but a CDN can answer 200 with an SPA/HTML
  page (auxly.io's `/dl` did this for the first signed release). The self-updater and both
  installers now content-sniff the manifest and treat a non-checksum 200 as "absent" →
  staged skip, instead of refusing to install.
- **`AUXLY_REQUIRE_SIGNATURE=1` is now honored by the shell installers** — strict mode
  refuses a checksum-only install when minisign isn't available, rather than silently
  downgrading.
- **Dev-mode `auxly update`** now locates the module root via `go.mod` (it was hardcoded to a
  removed `auxly-cli/` subdir → `chdir …/auxly-cli: no such file`) and installs over the
  currently-running binary (it was a fixed `~/.local/bin` path, so a box whose `auxly`
  resolved elsewhere rebuilt fine but kept running the stale binary).

## [1.0.20] - 2026-06-06

**Security hardening release.** A full security audit of the codebase (delegated to
independent reviewers) surfaced findings across the MCP trust boundary, path handling,
remote/SSH execution, software distribution, and file permissions. **Every Critical, High,
and Medium finding is closed**, each behind an independent adversarial-review gate, with no
change to existing functionality — verified by a live functional smoke test plus multiple
independent regression reviews. This release also stops the statusline usage meter from
freezing.

### Security

- **MCP provider identity is now server-side only.** A connected agent could previously
  claim another provider's identity in a write call to inherit its `trust.yaml` level and
  bypass its own `require_approval` / `read_only` gate. The server now derives the provider
  from launcher attribution + process ancestry and **ignores any client-supplied `provider`
  argument** (logging a `provider_mismatch` when they differ). The `provider` field was
  removed from the MCP write schema; the trusted CLI `--provider` flag is unchanged.
- **Agents can no longer approve their own pending writes.** The MCP approve/reject path is
  now **human-only** (the tools redirect to the CLI/TUI); an agent on `require_approval`
  cannot self-approve a queued change.
- **Path-traversal and symlink escape are blocked at a shared boundary.** A new
  `internal/safepath` guard validates every vault/workspace path: a pending change targeting
  `../../.ssh/authorized_keys`, a workspace read that climbs out of its root, and a symlink
  inside the vault pointing outside it are all refused. Legitimate relative subpaths still
  resolve normally.
- **Remote SSH execution is hardened.** `ssh_args` in a remote profile are validated against
  options that load external config or execute commands (`ProxyCommand`, `LocalCommand`, the
  `Include` directive, `Control*`, `-F`, `-S`); the host-binary path rejects flag-smuggling
  and shell metacharacters. Auxly's own generated connection args are unaffected, and
  legitimate identity paths and Windows binary paths still pass.
- **Releases and self-update are now cryptographically verifiable (staged rollout).** The
  release checksum manifest is signed with **minisign**, and `auxly update` plus both
  installers verify the downloaded binary against the signed manifest before trusting it,
  using a public key pinned into the binary. Staged: releases published before signing
  existed install unverified so nothing breaks; verification is enforced once a signed
  manifest is present — or always, with `AUXLY_REQUIRE_SIGNATURE=1`. The install base URL is
  pinned to HTTPS (localhost allowed for dev).
- **Sensitive on-disk files use tight permissions.** Config and credential files
  (`trust.yaml`, `remotes.yaml`, `clients.yaml`), the audit log, and the pending queue are
  now `0600`/`0700`; memory `.md` files and IDE MCP-config JSON stay world-readable (`0644`),
  matching what `setup` writes.

### Fixed

- **The statusline usage meter no longer freezes at "⧗ as of HH:MM".** The plan-usage line
  refreshes through a short-lived background process on each render (the render itself stays
  network-free), but the post-429 circuit breaker lived only in memory — so a rate-limited
  provider (Anthropic's usage endpoint, while an active session shares the same OAuth token)
  got re-probed every few minutes and its snapshot never advanced, while other providers
  stayed live. The cooldown is now **persisted to disk**, so every refresher backs off
  together and the line self-heals once the limit clears — and it stops firing repeat 429s
  at your token.

## [1.0.19] - 2026-06-06

**Remote-box updates and the relay statusline, fixed.** Follow-ups from live testing a
connected Windows box.

### Fixed

- **`[u]` Update of a Windows box no longer hangs the TUI on "Updating…".** The update's
  install-over-SSH lingers (the same Windows quirk as the connect install), so
  `updateRemoteAuxly` blocked until its timeout even though the box had already updated. It
  now runs the install and a version poll **concurrently on an isolated connection** and
  returns the moment the box reports the new version — reaping the lingering session. (Same
  fix shape as v1.0.18's connect install.)
- **The Windows installer swaps a *running* `auxly.exe` instead of failing on the lock.** A
  box with a live MCP session locks `auxly.exe`, so `Invoke-WebRequest -OutFile` over it
  failed with `exit status 1`. The installer now downloads to a temp file, moves the running
  binary aside (Windows allows renaming a running `.exe`), and drops the new one in place —
  non-disruptive. (Ships via the `auxly.io/cli.ps1` proxy.)
- **Relay-connected boxes no longer show "Local" in their statusline.** `detectRole` read the
  box's `remotes.yaml` with a mini-parser that missed the profile `name` when it sat on a
  YAML sequence line (`- name: …`, the real on-disk format), so a relay box — whose host is
  the tunnel's `localhost` — fell through to "local". The parser now strips the sequence
  dash, so the line correctly reads `remote→<host-name>`.

### Docs

- README "What's New" → 1.0.18/1.0.19: one-click Windows host-push connect, and `[u]` Update
  working on a live Windows box.

## [1.0.18] - 2026-06-06

**One-click Windows connect — the "Connect new" flow no longer hangs on "Installing…", and
no longer reports a green "Done" on failure.** Provisioning a Windows box from the Remote tab
could stall for minutes on the install step and then fail — while the panel header still read
a green "✓ Done". Two root causes:

1. **Hang:** the Windows `irm | iex` installer over SSH *completes on the box but leaves the
   SSH session lingering*. Because v1.0.17 reuses one SSH connection (`ControlMaster`), that
   lingering install **held the shared socket**, so the follow-up out-of-band
   `auxly --version` verify reused the poisoned socket and blocked behind it — the connect
   waited out both timeouts and aborted before ever reaching the automatic key-authorize and
   agent-wire steps. (Which is why a Windows box used to need a manual `auxly connect auto`
   on the box plus hand-authorizing its key.)
2. **False success:** `auxly host setup --provision` printed the provisioning failure as a
   warning but still **exited 0**, so the TUI rendered a green "✓ Done" over a failed connect.

### Fixed

- **Install + readiness poll now run on their own isolated SSH connection** (no shared
  `ControlMaster`). A lingering Windows install session can no longer wedge the socket the
  later steps reuse — the exact poisoning that made the verify hang. POSIX `curl | sh` is
  unchanged.
- **Install and verify run concurrently:** the install fires while `auxly --version` is
  polled; the moment the box answers, the connect proceeds and reaps the lingering install
  session — so it never SITS on "Installing…" waiting out a session that already finished.
- **On-box `auxly` commands after install now run on a fresh connection.** The agent-wire
  (`connect use`) and `[r] reconnect` (`connect auto`) reused the shared `ControlMaster`
  opened by the OS probe *before* auxly was installed — so that session still carried the
  pre-install PATH and the call failed with `'auxly' is not recognized`. They now use an
  isolated post-install connection that sees the updated PATH (the same reason the readiness
  poll resolved auxly while the muxed wire didn't).
- **`auxly host setup --provision` now exits non-zero when provisioning fails** — including
  when agent wiring fails — so the TUI shows the real outcome ("Finished with issues" + the
  error) instead of a misleading green "✓ Done". The relay/connection rows are already
  persisted, so re-running (or `[r] reconnect`) resumes cleanly.
- **Wired Windows boxes no longer show "unreachable" in the version column** (and are no
  longer skipped by `[u]` Update or statusline sync). The version sweep probed a bare
  `auxly --version`, which a non-interactive Windows SSH session can't resolve; it now falls
  back to the installer's absolute path (`%LOCALAPPDATA%\Programs\auxly\auxly.exe`) over
  PowerShell, the same PATH-independent probe the connect flow already used.
- Net result: adding a Windows box from the TUI reaches its existing auto-chain every time —
  **authorize the box's key here → save the connection → wire the box's agent (MCP + skills +
  statusline)** — with no commands on the box, and an honest success/failure header.

## [1.0.17] - 2026-06-06

**Reliable remote provisioning — no more `MaxStartups` floods.** Connecting/provisioning a
box opened ~10 separate SSH connections in a rapid burst (OS probe ×4, key check, install,
verify, hostname, wire). On a box with a low sshd `MaxStartups` cap (Windows default `10`)
that burst was throttled and reset mid-handshake (`kex_exchange_identification: Connection
reset by peer`), leaving the connect half-saved and auxly not installed. Now those commands
**reuse a single SSH connection**.

### Fixed

- **`sshConnArgs` enables SSH `ControlMaster` + `ControlPersist` multiplexing** (off-Windows
  clients) so every short remote command during a connect/provision shares one underlying
  connection — collapsing the pre-auth handshake burst that tripped `MaxStartups`. The control
  socket lives at `~/.ssh/auxly-cm/%C` (per-target, auto-expiring after 30s idle).
  `ControlMaster` is unsupported on a Windows *client*, so it's skipped there — and the only
  Windows-as-client case (a box dialing its host) makes a single connection anyway, so it
  loses nothing. Tests: `TestSSHConnArgsMultiplexing`, `TestSSHControlPathStable`.

## [1.0.16] - 2026-06-06

**Deterministic remote auto-wiring + Remote-tab management hardening.** Adding a box
(Windows / macOS / Linux) now configures everything on the remote automatically —
the MCP launcher, `/auxly-*` skills, and the statusline — even while the relay tunnel
is still coming up. Plus a sweep of fixes to the Remote tab's connection management.

### Fixed

- **Adding a box wires its agent reliably — no more silent half-connect.** The
  agent-wiring step (`connect use` / `connect auto`) gated ALL config writes on a
  host-reachability probe. On a freshly-added box the host's keep-alive supervisor
  dials the reverse tunnel a few seconds *later*, so the box's first probe hit a
  not-yet-listening port and the function aborted **before** injecting the MCP
  launcher / skills / statusline — leaving the box "connected" on the host but
  unconfigured. Now `probeHostReachable` retries across the tunnel-startup window
  (bounded per attempt) and wiring proceeds **even if the probe never answers** (the
  launcher, skills, and statusline are local writes that take effect at runtime under
  the agent's full environment).
- **`connect use` (host-push path) now installs the statusline too**, matching
  `connect auto`. Previously only the on-box path installed it.
- **No duplicate remotes/clients.** Dedup by name OR host+method+user+port, so
  re-adding the same box never creates a second row.
- **Deletes persist, and connected boxes stay visible when the host tunnel is down.**
  `host forget` removes the local record first (bounded best-effort remote cleanup),
  and the Remote tab lists connected boxes regardless of host-tunnel state.
- **Provision atomicity.** The connection is recorded before the (sometimes slow)
  agent-wiring, so a stalled wire never leaves a relay with no matching client row.

### Added

- **Hot-reload relay supervisor.** `auxly host tunnel` reconciles `host.yaml` on an
  interval — adding or removing a box starts/stops only that relay's tunnel, so the
  other boxes' live sessions are never dropped.
- **Runtime keep-alive + connect-retry resilience** for the box→host launcher
  (ssh keepalives + bounded transport-failure retry).
- **README: per-OS Uninstall section** (macOS, Linux, Windows, Homebrew) with a
  memory-vault safety warning.

### Changed

- OS-neutral "this machine" wording across the Remote tab (was hardcoded "Mac").

## [1.0.15] - 2026-06-05

**Windows remote-command robustness.** Bounds every over-SSH command so a remote
that leaves its SSH session lingering can never hang the CLI. Surfaced while
validating `auxly host setup --provision` against a real Windows box.

### Fixed

- **Remote `auxly` commands can no longer hang forever.** Installing/wiring a
  Windows box over SSH (`irm | iex`, `auxly connect use`, …) can complete on the
  box yet leave the PowerShell/SSH session open (a Windows OpenSSH
  session-lifetime quirk), which previously blocked the host CLI indefinitely.
  Now:
  - `runSSH` runs every remote command under a default 120s deadline
    (`exec.CommandContext`), so it fails-fast instead of hanging.
  - The Windows **install** step (`provisionConsumer` and `runDoctor`) is bounded
    by `runRemoteScriptTimeout` and then **verified out-of-band** (`auxly
    --version` on the box): a lingering-but-successful install resolves to success
    rather than a hang or false failure.

### Changed

- **Recommended Windows connect path is now the on-box pull** (host publishes its
  offer + tunnel; the box runs `auxly connect auto` / the `/auxly-remote-connect`
  skill locally). Documented in the README. The one-shot `host setup --provision`
  push-over-SSH remains available but can intermittently stall on Windows; it now
  degrades gracefully (bounded) instead of hanging.

## [1.0.14] - 2026-06-05

**Windows correctness sweep.** A full audit of the connect/host paths for
remaining POSIX-only assumptions found two that broke on Windows targets.

### Fixed

- **SSH key-auth / reachability checks no longer false-negative on Windows.**
  Five sites probed connectivity with `runSSH(p, "true")`, but `true` doesn't
  exist in Windows `cmd.exe`, so the check failed even when key auth worked —
  causing redundant key-install attempts and a misleading "passwordless SSH
  isn't set up" message in `auxly host setup`. Replaced with a shared
  `sshKeyAuthOK` helper that runs `exit 0` (a no-op that returns 0 on both POSIX
  shells and `cmd.exe`).
- **The statusline `--wrap` feature works on Windows.** It re-ran the user's
  previous statusline command with `sh -c`; on Windows there is no `sh`, so the
  wrapped command now runs via `cmd /c` on Windows (`sh -c` elsewhere).

## [1.0.13] - 2026-06-05

**Windows host/relay provisioning.** v1.0.12 fixed connecting *to* a Windows box
(`auxly connect`); this fixes the **other** direction — a memory **host**
provisioning a Windows box / publishing to a Windows relay through the
`auxly host` path, which still ran raw POSIX `sh -c` and failed with
`'sh' is not recognized`. All host-side provisioning is now OS-aware, validated
live against a real Windows 11 / PowerShell 5.1 host.

### Fixed

- **Host-side provisioning speaks PowerShell to Windows targets.** Four `host.go`
  sites now detect the target OS and route through the remote-shell layer instead
  of assuming POSIX:
  - `provisionConsumer` install — `irm <base>/cli.ps1 | iex` (TLS 1.2) on Windows
    instead of `curl … | sh`.
  - `authorizeRemoteKeyLocally` — generates the box's ed25519 key via PowerShell.
    Uses `ssh-keygen -N '""'` for the empty passphrase (the `[string]::Empty`
    splat is silently dropped on PowerShell 5.1 and would hang on a passphrase
    prompt).
  - `writeRelayOffer` — writes the offer to a Windows relay by base64-embedding
    the YAML and `[IO.File]::WriteAllText(…, UTF8Encoding $false)` (avoids the
    BOM that `Set-Content -Encoding utf8` adds on PS 5.1 and that would corrupt
    the YAML).
  - `reportTunnelLive` — checks the reverse-tunnel port with `Get-NetTCPConnection`
    on Windows instead of `ss`/`netstat`.
- A shared `remoteShellArgv` helper now backs both `runRemoteScript` and the
  host.go sites that build their own `ssh` invocations, so per-OS argv
  construction has one source of truth. POSIX targets are byte-for-byte unchanged.

### CI

- The `release` workflow now only runs in the public repo
  (`github.repository == …/auxly-memory-cli`), so a tag on the private mirror no
  longer triggers a spurious "release failed" run (it lacks the Homebrew tap token).

## [1.0.12] - 2026-06-05

**Windows support.** Connecting to a Windows machine as a remote — and running a
Windows machine as a memory **host** — now works end to end, validated live against a
real Windows 11 host (build 26100). The root cause was that a Windows host's default
SSH shell is `cmd.exe`, so Auxly's POSIX `sh -c` provisioning never ran (the
`'sh' is not recognized` failure). Auxly now detects the remote OS over SSH and speaks
**PowerShell** to Windows hosts.

### Fixed

- **`auxly connect` to a Windows host works.** A new remote-shell layer detects the
  target OS over SSH — `uname -sm` for POSIX, falling back to a PowerShell
  `-EncodedCommand` probe for Windows (the old `cmd /c ver` probe is mangled by
  OpenSSH-for-Windows when the default shell is `cmd.exe`). Every Windows command is
  then run through `powershell -NoProfile -NonInteractive -EncodedCommand <UTF-16LE
  base64>`, which sidesteps cmd.exe/SSH quoting entirely. POSIX hosts are unchanged.
- **Cross-platform agent detection & configuration.** Agent config locations
  (Claude Desktop, Cursor, Copilot, Perplexity, Gemini) now resolve under
  `%APPDATA%` / `%LOCALAPPDATA%` on Windows and `~/.config` on Linux, instead of
  assuming macOS `~/Library/Application Support`. `getBinaryPath` resolves
  `auxly.exe` (via `%LOCALAPPDATA%\Programs\auxly` or PATH) on Windows.

### Added

- **A clean Windows host is auto-installed over SSH.** When `auxly` is missing on a
  reachable Windows host, Auxly installs it via PowerShell
  (`irm https://auxly.io/cli.ps1 | iex`, TLS-1.2 hardened for older Windows), then
  re-probes — falling back to `%LOCALAPPDATA%\Programs\auxly\auxly.exe` if the fresh
  PATH isn't yet live in the SSH session. Verified by wiping auxly from a real box and
  letting `auxly connect` reinstall it.
- **Windows as a memory host.** A Windows machine can serve its own vault to your other
  agents: a consumer's `auxly connect-mcp` launches `auxly mcp-server` on the Windows
  host over SSH (verified with a live MCP `initialize` handshake). Keep-alive uses
  Windows Task Scheduler (`schtasks`), already OS-dispatched alongside launchd/systemd.
- **Self-healing remote OS.** The first successful connect records the detected OS back
  onto the saved profile, so later steps that can't probe (e.g. installing the SSH key
  over a password session) use the correct shell family. Windows admin key installs
  target `%ProgramData%\ssh\administrators_authorized_keys` with the ownership/ACL
  (`takeown` + `icacls`) that Windows OpenSSH requires.

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

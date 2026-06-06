# Changelog

All notable changes to Auxly Memory CLI are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

# Auxly Memory & Skills Rework — Working Plan

> **Status:** ✅ IMPLEMENTED (2026-05-31) — all sections built, full suite
> race-green, binary rebuilt+signed v1.0.0. UNCOMMITTED pending review.
> Verified: taxonomy router, max/learn/bootstrap handlers (live MCP), personal.md
> migration, §10 remote ACL (unit + integration + live-binary smoke). Needs
> real-world check by user: live 2-box SSH remote flow, TUI share-modal visuals,
> an actual organize run (LLM re-filing).
>
> Branch: `dev` · Last updated: 2026-05-31

---

## 0. Why this exists

What started as three gaps in the `/auxly-*` skills + the On-Demand Organization
prompt grew into a coherent memory-routing + privacy rework. Scope:

1. **`/auxly-max` doesn't push memory up** (§1) — static placeholder that the
   `SKILL.md` turns into a vault *pull*. → exhaustive self-harvest, slice-by-file.
2. **`/auxly-learn` redefinition** (§2) — flip to inbound "read & internalize"
   with `[folder] [topic]` scoping.
3. **No private bucket** (§3) — add `personal.md` (additive; family/legal/health),
   everything else stays. Per-remote sharing (§10) governs exposure.
4. **Organize doesn't re-file misplaced facts** (§4) — prompt principle #4 forbids
   relocation. → re-classification + integrity + tier-boundary rules.
5. **Taxonomy-aware routing** (§8) — one canonical category list injected into
   every write skill (via the shared footer) so placement is right the first time.
6. **`/auxly-bootstrap`** (§9) — new skill: copyable onboarding block for tools
   without Auxly (the old `max` intent, split out).
7. **Per-remote file sharing** (§10) — TUI checklist of which files each remote
   sees + read-only/read-write; `personal.md` is one row, off by default.

Sections 5–7 are cross-cutting (dependency order, open-threads ledger,
touch-point index). **All checkboxes in §6 are resolved.**

---

## 1. `/auxly-max` — fix to exhaustive self-harvest (Option 1)

### Agreed behavior
`/auxly-max` = the deliberate **"dump my full session into the vault now"** sweep.
The current agent scans its entire context, extracts **every** fact, and writes
them all up. The tool can't read the agent's mind — so it returns a strong
**directive** and the agent performs the writes via `sync` / `memory_write`.

### Harvest strategy: SLICE-BY-CATEGORY (not one blind dump)
The `max` directive walks the **canonical taxonomy (§8)** and writes **one focused
slice per file**: collect all infra facts → `infra.md`; all project facts →
`projects.md`; all personal/family facts → `personal.md`; etc. Benefits:
- Correct placement the first time (agent is thinking per-category).
- Small, atomic, trust-gated, auditable diffs instead of one giant write.
- Less for organize (§4) to repair.
- Agent reconciles each slice against the file's **existing** content to avoid
  duplicating facts already saved.
Fixed slice order for predictability (e.g. identity → personal → preferences →
infra → products → projects → daily → business → agents).

### Why it's not redundant (the overlap we resolved)
Clean three-way split by **commit scope**:

| Command | Scope | Writes? |
|---------|-------|---------|
| `sync [content]` | one named fact | ✅ commits |
| `learn` (see §2) | (being redefined) | — |
| `max` | **all** the agent knows | ✅ commits everything |

`max` vs `sync` = `git add -A` vs `git add file`. Different intent, not duplication.

### Work
- Rewrite `toolSkillMax()` (`mcp/server.go:789`) to return the
  "scan-extract-write-all-now" directive (drop the static "alignment protocol").
- Fix `SKILL.md` + `cmd/setup.go` text to **stop doing the vault pull**
  (`auxly_skill_memory`); `max` is push-only.
- Update TUI/description text that still calls it "Bootstrap Sync / cross-agent"
  (`tui/skills.go:61-66`, `mcp/server.go:343,1177`) so docs match new behavior.
- **Safety:** harvest writes go through the existing trust gate
  (`toolWriteScoped` → trust per provider). The agent is the quality filter.

### Split out (not parked)
The **cross-agent bootstrap prompt** (the paste-into-another-tool onboarding
prompt, artifact at `~/.auxly/memory/onboarding_prompt_claude.txt`) is **no longer
part of `max`** — it becomes its own skill **`/auxly-bootstrap` (§9)**. So the old
`max` is split in two: `max` = self-harvest (§1), `bootstrap` = onboard a foreign
tool (§9).

---

## 2. `/auxly-learn` — redefine to inbound "read & internalize" (LOCKED)

### Current behavior (to be replaced)
`learn [context]` is **outbound + propose-only**: requires a `context` arg, splits
it on `.` into sentences (naive — comment says "Simulate AI analysis"), prints a
`[Proposed]` list, writes nothing, tells you to `/auxly-sync` them.
(`toolSkillLearn`, `mcp/server.go:1094`.) The old "extract facts from a snippet"
role is **dropped** (it overlapped `max`'s extraction; no longer needed).

### Agreed new behavior
`learn` becomes an **inbound directive**: the agent **reads the unified memory
vault and internalizes / grounds itself in it** — "learn everything we already
know about the user and operate from it for the rest of the session." Direction
flips: context→facts becomes vault→agent's working understanding.

Mechanics: `toolSkillLearn` returns the **vault content** (respecting tier/ACL —
see §3) wrapped in a strong **"absorb this and behave accordingly"** directive, so
one call both loads and instructs.

### Signature & scoping — `/auxly-learn [folder] [topic]`
Both args optional:
- `/auxly-learn` → learn **everything** (full vault internalize, ACL-filtered).
- `/auxly-learn <folder>` → read only that category/file (e.g. `infra`, `projects`).
- `/auxly-learn <folder> <topic>` → that file, **focused on the topic** (e.g.
  `infra nginx`).
Rules:
- `folder` is validated against the **§8 canonical taxonomy**; an unknown name
  returns the valid category list (which doubles as teaching the taxonomy).
- §3 ACL applies: `/auxly-learn personal` on a remote without personal access is
  denied at the payload level (not just prompt).
- `topic`: simplest impl = tool returns the scoped file + topic in the directive,
  agent focuses; optional enhancement = server-side substring/section prefilter to
  shrink payload.

### Resulting taxonomy

| Command | Direction | Role |
|---------|-----------|------|
| `sync` | push (one) | commit a single named fact |
| `max` | push (all) | exhaustive self-harvest up |
| `learn` | **pull → internalize** | agent reads vault and learns it |
| `memory` | pull → display | show the consolidated profile |

### `learn` vs `memory` distinction — RESOLVED
- `memory` = **user-facing**: "show me my profile" → pretty consolidated display.
- `learn` = **agent-facing**: directive + content → "absorb this, think with it
  loaded." Similar payload, different intent/framing (acceptable, like max↔sync).

### Tier interaction (§3)
`learn` reads only the personal bucket the **current session is allowed** to see.
Local session → reads everything. Remote session with personal ACL = `none` →
`learn` skips the personal bucket entirely (payload-level, not prompt-level).

### Work
- Rewrite `toolSkillLearn()` (`mcp/server.go:1094`): drop the sentence-splitter /
  propose logic; make `context` **optional/removed**; return tier-filtered vault
  content + the internalize directive.
- Replace input schema (`mcp/server.go:393-395`): drop `context`; add optional
  `folder` (validated vs §8 taxonomy) + optional `topic`.
- Update `SKILL.md` + `cmd/setup.go:677` learn text: from "pass context → show
  proposed facts" to "read the vault and internalize"; set
  `argument-hint: "[folder] [topic]"`.
- Update prompt wiring `getPromptContent` (`mcp/server.go:1265-1267`) to pass
  folder/topic.
- Update TUI skills doc for `learn` (`tui/skills.go`).

---

## 3. Personal bucket + per-remote access control (REVISED — user's model)

> **Superseded the earlier multi-tenant/owner-identity design.** User chose the
> simpler, single-user reality of Auxly: one **personal bucket**, exposed to
> remotes **per connection**. No owner-identity layer needed. This resolves the
> former blocking question — target is **single-user / multi-machine**.

### Problem
The remote/relay feature shares a host vault with remote peers. Today there is no
way to keep the human's *personal* facts out of what a remote can read/write.

### Agreed model
- **One personal bucket** holding *everything personal, not business* —
  `personal.md` now, can grow into a `personal/` folder later. Organize/§4 treat
  "personal" as a tier either way.
- **The rest of the vault stays shareable.**
- **Per-remote ACL:** access to `personal.md` is controlled by the **per-remote
  file-sharing selection (§10)** — it's one checklist row, **unchecked by default**.
  The remote's `read-only`/`read-write` access level governs writes. (This
  generalizes the earlier "none/read/read-write on personal" into the §10 model.)

### Hard rules
- **Access enforced at the PAYLOAD level, not the prompt.** If a remote is `none`,
  the host **never sends** `personal.md` over the wire. Prompt instructions are a
  second line of defense, never the first.
- **Safe default = private.** New remote gets the shareable vault but NOT personal
  until explicitly opted in. `read` = remote can ground itself on you without
  modifying; `read-write` = remote may add personal facts.
- **Personal organize uses the LOCAL LLM only** — never ship name/journal/private
  prefs to a remote/cloud model.

### Decisions (user said "yes" to proposed defaults — 2026-05-31)
1. **Personal/shared line — ACCEPTED:**
   - Personal: `identity`, private `preferences`, `daily` journal.
   - Shared: `products`, `projects`, `business`, `agents`.
   - **`infra.md` — CONFIRMED SHARED** (work remotes need it). Tier remains
     per-file configurable so the user can flip specific files to personal later.
2. **File vs folder — ACCEPTED:** start as `personal.md`, allow `personal/` folder
   later. Build the bucket concept tier-aware regardless.
3. **ACL ownership side — ACCEPTED (host decides):** the host decides "can this
   connecting remote touch *my* personal bucket?"

### Personal content — concrete examples + migration seed (from live vault audit 2026-05-31)
`personal.md` is **additive** (Reading B): all other files stay as-is; only
genuinely-private life facts live here. Audit found these currently **misfiled**:
- **Family** → today in `identity.md` `## Family` (Wife Hanan, pregnancy/first
  child, clinic). Mirrored in `unified_memory.md`. → should move to `personal.md`.
- **Civil Dispute No. 4772176104** (personal legal/financial matter, divorce
  reference) → today in `projects.md:16`. Not a project. → candidate for `personal.md`.
- **DATA GAP:** user referenced "son and daughters" but the vault holds **only**
  the pregnancy/first-child entry — no existing-children facts. To capture into
  `personal.md` when built (user supplies details).
Migration: on first run, move the `identity.md` Family block (and the legal case)
into `personal.md`; route *new* personal facts there going forward.

### Work
- Add personal bucket to the store layer (`internal/memory`, `store`).
- Add `personal.md` to the **canonical taxonomy (§8)** so all consumers know it.
- Extend `sync` auto-router (`toolSkillSync`, `mcp/server.go:806`) to route
  personal facts into the personal bucket.
- Personal access is implemented via the **per-remote file-sharing selection
  (§10)** — `personal.md` is one checklist row, default unchecked. Enforced at the
  payload level on the host.
- Remote connect flow (`connect` / `host`) prompts for file sharing at setup; TUI
  Remote tab edits the per-remote selection (§10).
- One-time migration of the Family block + legal case into `personal.md`.

This still threads through §1 `max` (harvest personal → personal bucket), §2
`learn` (only read personal you're allowed), §4 organize (re-file within tier).

---

## 4. Organize prompt — add re-classification + tier privacy

### Gap
User expected organize to be the **authoritative re-filer**: read everything, put
each fact in the file that matches its meaning, and move misfiled facts to the
correct home. Instead, prompt **principle #4 forbids relocation**:

> "BOUNDARY RESPECT … Do not mix up the sections."

It conflates *keep the file schema stable* (good) with *don't move facts between
files* (bad — blocks re-filing). The prompt also **never defines what each file is
for**, so the model can't know the "correct place."

(Prompt defined twice, identical: `internal/memory/organize.go:70` and `:368`.)

### Two-layer mental model
1. `sync` (write-time) = shallow *first-guess* keyword router → lands wrong on
   ambiguous content.
2. `organize` (cleanup pass) = the *authoritative* re-filer that fixes layer 1.

### Prompt fix (draft — replaces principle #4, adds #5)

```
4. RE-CLASSIFICATION (PRIMARY JOB): Read every fact and place it in the file
   that matches its MEANING, regardless of which file it currently sits in.
   Move misfiled facts to their correct home. The file taxonomy is fixed —
   do not invent or remove files — but fact membership is yours to correct.
   [INJECT the canonical taxonomy from §8 here — single source of truth]

5. INTEGRITY ON MOVE: Every fact must end up in EXACTLY ONE file — the correct
   one. Never drop a fact while relocating it, and never leave the same fact
   duplicated across two files. De-duplicate only AFTER routing, within each
   target file.

6. TIER BOUNDARY: Never move a fact across the personal/shared boundary (§3) —
   re-file WITHIN a tier only. Personal facts stay in personal.md.
```

Then the existing dedup / summarize / token / JSON-output principles follow — they
now run **after** routing.

### Why it works mechanically (minimal code change)
- Organizer **already receives the full vault** in one payload (`vaultPayload`) →
  it *can* see a misfiled fact and move it.
- Output schema is **an array of all files** → rewriting two files in one pass
  (moving a fact between them) is already supported. Only the **prompt** changes.

### Tier interaction
Once §3 tiers exist, re-classification must stay **within a tier** — fix the file,
never promote a personal fact into shared (or vice-versa). So lock tier model
first; this prompt inherits "re-file within tier only."

---

## 5. Dependency order (proposed)

```
§3 ownership model — RESOLVED: single-user + personal bucket + per-remote ACL
        │  (still defines the "personal" tier the others depend on)
        ▼
§3 personal bucket + ACL (store, sync routing, remote ACL, TUI)
        │
        ├──▶ §1 max rework (harvest personal → personal bucket)
        ├──▶ §2 learn redefine (read only the personal you're allowed)
        └──▶ §4 organize prompt (re-file within tier; personal → local LLM only)
```

The personal/shared **tier definition** (§3) still needs to land before §1/§2/§4,
because all three reference it — but the heavy owner-identity layer is no longer
needed, so §3 is now a much smaller lift.

---

## 6. Open threads / TODO before locking

- [x] **§3 personal/shared line** — split ACCEPTED; only `infra.md` left to confirm.
- [x] **§3 `infra.md`** — SHARED by default + per-file override. CONFIRMED.
- [x] **§3 file vs folder** — `personal.md` now, `personal/` later. ACCEPTED.
- [x] **§3 ACL ownership side** — host decides. ACCEPTED.
- [x] **§2 `learn`** — LOCKED: inbound read & internalize.
- [x] **§2** `learn` vs `memory` distinction — RESOLVED (agent-facing vs user-facing).
- [x] **Cross-agent bootstrap** — RESOLVED: its own new skill `/auxly-bootstrap` (§9).
- [x] **`providers.md`** — system metadata, **shared**, NOT part of the user-fact
      taxonomy/router. Stays as-is.
- [x] **Organize cross-tier** — never re-files across tiers (§4 rule #6). CONFIRMED.
- [x] **Per-remote file sharing** — RESOLVED: §10 (4 decisions locked).

---

## 7. Touch-point index (for implementation later)

| Concern | File:line |
|---------|-----------|
| `max` handler | `mcp/server.go:789` (`toolSkillMax`) |
| `max` tool desc | `mcp/server.go:343` ; prompt `:1176-1177,1251` |
| `max` skill text | `cmd/setup.go:628-634` ; live `~/.claude/skills/auxly-max/SKILL.md` |
| `max` TUI doc | `tui/skills.go:61-66,531-537` |
| `learn` handler | `mcp/server.go:1094` (`toolSkillLearn`) |
| `learn` skill text | `cmd/setup.go:677` ; `~/.claude/skills/auxly-learn/SKILL.md` |
| `sync` handler + auto-router | `mcp/server.go:806` (`toolSkillSync`) |
| `memory` handler | `mcp/server.go:735` (`toolSkillMemory`) |
| Scoped write + trust | `mcp/server.go:568` (`toolWriteScoped`) |
| Organize prompt (×2) | `internal/memory/organize.go:70` and `:368` |
| Organize entry (CLI) | `cmd/organize.go` |
| Organize entry (TUI) | `tui/settings.go` |
| `sync` keyword router / file map | `mcp/server.go:814-881` |
| Memory priority map | `mcp/server.go:744-753` |

---

## 8. Taxonomy-aware routing (shift-left) — get placement right at write time

### Idea
Don't lean on organize to fix misfiling. Teach the **agent** the category taxonomy
so it files correctly the FIRST time, and have `max` push **slice-by-slice**.
Organize (§4) then becomes a safety net, not the primary sorter.

### Applies to EVERY write-capable skill (not just `max`)
Any skill that can write must carry the taxonomy: `sync`, `max`, `init`, and any
future write skill. **Single injection point:** bake the canonical taxonomy into
the shared **`appendSkillSyncFooter()`** helper (already appended to nearly every
skill output) — so every skill inherits taxonomy-awareness automatically and no
new skill can forget it.

### Three layers of correctness
| Layer | Mechanism | Role |
|-------|-----------|------|
| Write-time | agent knows taxonomy → picks right `category` | get it right first |
| Harvest | `max` pushes slice-by-slice per file (§1) | structured, no blind dump |
| Cleanup | organize re-files leftovers (§4) | safety net |

### Single source of truth (the anti-drift rule)
Define the category taxonomy **ONCE** and inject it into every consumer: skill
prompts (`init`, `sync`, `max`, `learn`), the `sync` tool description, and the
organize prompt. Today the taxonomy is **implicit and duplicated** across three
drifting places, none of which actually *define what goes in each file*:
- `sync` keyword auto-router (`server.go:814-881`)
- organize prompt principle #4 (names files, no definitions)
- memory priority map (`server.go:744-753`)

### Canonical taxonomy (draft — add `personal.md`)
```
identity.md    : who the user is — name, role, professional bio, persona
personal.md    : PRIVATE life — family, relationships, health, personal legal/
                 financial matters. Per-remote ACL (§3); local-LLM only.
preferences.md : coding style, workflow, editor/tool choices
infra.md       : servers, IPs, OS, networking, hardware, services
products.md    : the user's products / portfolio
projects.md    : repos, active work, workspaces, project status
daily.md       : dated journal / session work log
business.md    : strategy, financial, company-level facts
agents.md      : AI-agent activity and onboarding events
```

### Work
- Create one canonical taxonomy definition (constant/shared block) — Go source +
  reused in prompt strings.
- **Inject taxonomy via `appendSkillSyncFooter()`** so ALL skill outputs carry it.
- `sync` keeps its `category` param (already exists); server keyword router
  becomes the **fallback** when the agent doesn't specify. Expose taxonomy in the
  `sync` skill/description so the agent specifies knowingly.
- Ensure `init` / `max` explicitly reference the taxonomy when directing writes;
  `learn` validates its `folder` arg against it.
- §4 organize prompt references the same taxonomy instead of re-listing files.

> This is foundational — §1 (slice harvest), §3 (`personal.md` as a category), and
> §4 (re-classification) all reference this canonical taxonomy.

---

## 9. `/auxly-bootstrap` — NEW skill (cross-tool onboarding prompt)

### Purpose
Generate a **copyable instruction block** to paste into an agent/tool that does
**NOT** have Auxly installed (e.g. ChatGPT web, a colleague's machine, a brand-new
tool), so that foreign agent can read/write the user's memory. This is the
*original* intent of `/auxly-max` (the saved `~/.auxly/memory/onboarding_prompt_claude.txt`),
now given its own home so `max` stays the self-harvest (§1).

### Behavior (important nuance)
- Running it **only SHOWS the copy block** — it does **not** sync anything itself.
- The user copies the block → pastes into the foreign agent → **that** agent does
  the actual reading/writing by following the block's instructions.
- The block is **dynamically tailored** to the live environment: absolute `auxly`
  binary path, provider, and (if applicable) the active local gateway port — so
  the three fallbacks work on the user's machine.

### Block contents (from the existing artifact)
Three fallback paths for the foreign agent:
- **Option A:** call the MCP `auxly_memory_write` tool (if it has MCP).
- **Option B:** run the `auxly write …` CLI with the absolute binary path.
- **Option C:** just output styled markdown for the user to save manually.

### Tier note
The block contains *instructions*, not personal data. If the foreign agent later
**reads** memory, the §3 personal ACL still governs what it can access.

### Optional
`/auxly-bootstrap [target]` — tailor wording per target tool (e.g. `chatgpt`,
`cursor`); defaults to a generic block. (Original artifact was per-provider:
`onboarding_prompt_claude.txt`.)

### Work
- New MCP tool `auxly_skill_bootstrap` + handler `toolSkillBootstrap()` that
  generates the dynamic block (restore/adapt the old `toolSkillMax` prompt text).
- New skill: `~/.claude/skills/auxly-bootstrap/SKILL.md` + `cmd/setup.go` skill
  generation + add to the skills list/`.toml` set.
- Add to `getPrompts()` + `getPromptContent()` (`mcp/server.go`).
- Add TUI skills doc entry (`tui/skills.go`).
- Keep `onboarding_prompt_claude.txt` as the template seed (or regenerate live).

### Independence
Does not depend on §3 tiers — it only emits instructions. Can be built anytime;
naturally pairs with the §1 `max` rework (they split the old `max` in two).

---

## 10. Per-remote file-sharing selection (generalizes §3 ACL)

> This **supersedes/generalizes** §3's "personal none/read/read-write." Instead of
> one personal toggle, each remote gets a **checklist of which files it can see**;
> `personal.md` is simply one row (default unchecked). §3's hard rules
> (payload-level enforcement, safe-default-private) still apply.

### Model
Per remote store: a **shared-files selection** (which files it may read) + one
**access level** (`read-only` | `read-write`) governing writes to those files.

### Decisions — LOCKED (user "great" 2026-05-31)
1. **Read vs write axis:** checklist = **visibility (read)** per file; a single
   per-remote **access toggle** (`read-only` / `read-write`) governs writes to all
   its shared files. NOT per-file read/write.
2. **Scope:** **host-side, inbound clients only** — you pick what each box
   connecting to *your* vault can see. Outbound "hosts I connect to" are their
   vaults (they control what I see).
3. **Default for a new remote:** all **shared** files checked, **`personal.md`
   unchecked**, access = **read-only**. User loosens per remote.
4. **Live edit:** changing the selection applies on the remote's **next request**
   (host filters the payload each time) — no reconnect needed. Editable any time.

### TUI flow
Remote tab → highlight an inbound client → key **`s`** (share) → modal checklist:

```
┌─ Share files with: ERPai (192.168.1.168) ──────────────┐
│  Access:   ( ) Read-only      (•) Read & write          │
│  [x] identity.md   [x] preferences.md  [x] infra.md      │
│  [x] products.md   [x] projects.md     [x] daily.md      │
│  [x] business.md   [x] agents.md                         │
│  [ ] personal.md   PRIVATE — family, legal           ⚠   │
│  space toggle · a all · n none · w access · enter save   │
└──────────────────────────────────────────────────────────┘
```
File list comes from the **§8 canonical taxonomy** (new files appear
automatically). `personal.md` flagged + off by default.

### Storage + enforcement
- Per inbound-client entry: `shared_files: [...]` + `access: read|write`
  (alongside the existing client/trust record — `clients.yaml` / `trust.yaml`).
- **Enforced at the payload level on the host**: a remote's request returns/permits
  ONLY its selected files. Never "send all, ask nicely."

### Work
- Config schema: add `shared_files` + `access` per inbound client.
- Host vault-serving path: filter served files by the requesting client's
  selection (read) and reject writes when `read-only` or file unselected.
- TUI: `s` key + checklist modal in the Remote tab (`tui/ssh.go`); taxonomy-driven.
- `connect` / `host` wizard: optional "select files to share" step at setup
  (defaults applied if skipped).
- Write trust check composes with this (selection gates visibility; trust +
  access level gate writes).

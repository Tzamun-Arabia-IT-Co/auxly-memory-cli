// Package skills is the SINGLE SOURCE OF TRUTH for Auxly slash-command skills.
//
// Before this package the skill bodies were copied in cmd/setup.go (install) and
// tui/skills.go (Claude Desktop export); the copies drifted and the export began
// shipping stale text. Both consumers now read from the registry below, so they
// can never diverge again, and skills_test.go fingerprints the content so a skill
// edit can't slip out without bumping Version.
package skills

// Version is the Auxly skills serial (semver), intentionally decoupled from the
// tool's release VERSION. Bump it by significance whenever a skill body changes —
// and ONLY then, never on an unrelated tool release.
//
// The tool ships many releases where skills are untouched; re-downloading the
// same skills each time is noise. Conversely a skill can change without a tool
// release and MUST trigger a fresh download. So the serial tracks skill CONTENT.
//
// It lives in the download FOLDER name (~/Downloads/auxly-skills-v<Version>/),
// not the .zip names: Claude identifies a skill by its .zip filename, so renaming
// the zip would make Claude treat an update as a brand-new skill (forcing a manual
// delete). The folder carries the version instead — Claude never imports it, a new
// version lands as a new folder, and the stable zip names update the skill in place.
const Version = "1.0.0"

// UpdateReminder is appended to every installed / exported SKILL.md so each agent
// keeps proactively syncing. Kept here so install and export share one copy.
const UpdateReminder = "\n\nIMPORTANT: You are expected to proactively update the user's unified Auxly memory vault. Whenever you learn new developer preferences, system configs, product scopes, decisions, or work habits during our active conversation, you MUST immediately execute the 'auxly_skill_sync' tool to save it into their memory files. Keep the memory synchronized!"

// Skill is one slash command's installable definition.
type Skill struct {
	Name    string // slash-command name, e.g. "auxly-sync"
	Body    string // full SKILL.md text (frontmatter + body); UpdateReminder is appended at write time
	Desktop bool   // include in the Claude Desktop ZIP export? (remote-connect is install-only)
}

// All returns the canonical, ordered skill registry.
func All() []Skill { return registry }

// Map returns name → full SKILL.md body, for the installer.
func Map() map[string]string {
	m := make(map[string]string, len(registry))
	for _, s := range registry {
		m[s.Name] = s.Body
	}
	return m
}

// DesktopSkills returns the subset exported as Claude Desktop ZIPs.
func DesktopSkills() []Skill {
	out := make([]Skill, 0, len(registry))
	for _, s := range registry {
		if s.Desktop {
			out = append(out, s)
		}
	}
	return out
}

var registry = []Skill{
	{Name: "auxly-init", Desktop: true, Body: `---
name: auxly-init
description: Run the onboarding training, scan current context, and synchronize existing chat context/preferences to Auxly.
---
# /auxly-init

You must immediately invoke the 'auxly_skill_init' MCP tool to align your session instructions, scan current context and system prompts, and synchronize existing facts/preferences to the Auxly vault. Show the beautiful onboarding guide and confirmation card!`},

	{Name: "auxly-memory", Desktop: true, Body: `---
name: auxly-memory
description: Retrieve and display a consolidated markdown profile of the user's identity, preferences, and system infrastructure.
---
# /auxly-memory

You must immediately invoke the 'auxly_skill_memory' MCP tool to retrieve and display the consolidated profile of the user's identity, preferences, and infrastructure. Do not ask for further clarification, simply run the tool and show the output!`},

	{Name: "auxly-max", Desktop: true, Body: `---
name: auxly-max
description: Exhaustive self-harvest — scan your whole session and write every fact up into the memory vault, slice by category.
---
# /auxly-max

You must immediately invoke the 'auxly_skill_max' MCP tool to load the harvest directive. Then perform an EXHAUSTIVE SELF-HARVEST of your entire session: scan everything you have learned and write it ALL up via the 'auxly_skill_sync' tool, working ONE category at a time (collect all infra facts, then all project facts, etc.), reconciling each slice against what is already saved so you never duplicate. Route the user's OWN private-life facts — family, relationships, health, and their personal legal/financial matters (their own lawsuit/court case, divorce, custody, personal loan, salary) — into personal.md via category 'personal'; a company/business legal or money matter is NOT personal. Judge by context: a personal legal case is never a 'project' or 'business' entry. This pushes memory UP only — do NOT pull or read the vault. Finally, present a beautiful success message confirming the full session has been harvested into unified memory!`},

	{Name: "auxly-sync", Desktop: true, Body: `---
name: auxly-sync
description: Append and synchronize a new fact, preference, or system detail using smart automated delta-merges into memory files (preferences.md, identity.md, infra.md, products.md, projects.md, daily.md, etc.).
argument-hint: "<fact or preference statement to sync>"
---
# /auxly-sync

You must immediately invoke the 'auxly_skill_sync' MCP tool. Pass the user's statement as the 'content' argument AND set the 'category' argument to the best-fit category from the taxonomy shown in the tool's footer (identity, personal, preferences, infra, products, projects, daily, business, agents) — you understand the fact, so you pick the file; only omit 'category' if you are genuinely unsure, in which case the router will guess. Route the user's OWN private-life facts — their family, health, relationships, and their PERSONAL legal/financial matters (their own lawsuit, court case, divorce, custody, personal loan, salary) — to category 'personal'; a company/business legal or money matter is NOT personal (use 'business'). Judge by context, not the topic word, and when a fact is about the user's private life, 'personal' wins over any topical category (a personal legal case is never a 'project'). This performs a smart automated delta-merge into the chosen memory file. Run the tool and display the confirmation output!`},

	{Name: "auxly-pending", Desktop: true, Body: `---
name: auxly-pending
description: Manage pending memory changes awaiting human approval directly inside the active chat panel.
argument-hint: "[list | approve <id> | reject <id>]"
---
# /auxly-pending

You must immediately invoke the 'auxly_skill_pending' MCP tool, passing the provided arguments (such as action: list/approve/reject, and target ID) to manage the secure memory write queue. Simply run the tool and display the results!`},

	{Name: "auxly-status", Desktop: true, Body: `---
name: auxly-status
description: Show whether this agent is connected to Auxly memory and the MCP link is live.
---
# /auxly-status

Call the 'auxly_skill_status' MCP tool exactly ONCE and show its raw output to the user. That output IS the complete status: it confirms the MCP link is live, reports the memory connection (local or ssh-remote), and shows database stats.

HARD RULES — the single tool call is the entire task:
- Do NOT read any source code or files.
- Do NOT run shell/bash commands or other auxly CLI commands (no 'auxly stats', 'auxly connect test', 'auxly list', etc.).
- Do NOT investigate, diagnose, or "test" anything further — the tool reply already proves the MCP channel works.
- If the tool replies, the status is healthy. Just present it and stop.`},

	{Name: "auxly-forget", Desktop: true, Body: `---
name: auxly-forget
description: Search memory vault and prune obsolete or outdated bullet statements cleanly from memory files.
argument-hint: "<query string to search and delete>"
---
# /auxly-forget

You must immediately invoke the 'auxly_skill_forget' MCP tool, passing the user's provided input as the 'query' argument, to search across all memory files and delete matching obsolete lines cleanly. Simply run the tool and display the deletion diff!`},

	{Name: "auxly-learn", Desktop: true, Body: `---
name: auxly-learn
description: Read the memory vault (optionally a single folder, optionally focused on a topic) and ground yourself in it for the rest of the session.
argument-hint: "[folder] [topic]"
---
# /auxly-learn

You must immediately invoke the 'auxly_skill_learn' MCP tool to read the unified memory vault and internalize it — learn everything already known about the user and operate from it for the rest of the session. Pass the optional first argument as 'folder' to read only that category/file (e.g. 'infra', 'projects'), and the optional second argument as 'topic' to focus within it (e.g. 'infra nginx'). Empty args = learn everything. Absorb the returned content and behave accordingly.`},

	{Name: "auxly-bootstrap", Desktop: true, Body: `---
name: auxly-bootstrap
description: Get a copyable onboarding block to paste into a tool without Auxly installed.
---
# /auxly-bootstrap

You must immediately invoke the 'auxly_skill_bootstrap' MCP tool to generate a copyable onboarding block, then present that block to the user verbatim so they can paste it into a tool that does NOT have Auxly installed (e.g. ChatGPT). Running this only SHOWS the block — it does NOT sync anything itself; the foreign agent does the actual reading/writing by following the block's instructions. Simply run the tool and display the returned block!`},

	{Name: "auxly-remote-connect", Desktop: false, Body: `---
name: auxly-remote-connect
description: Detect and connect this machine to a remote Auxly memory host (or report the active link).
---
# /auxly-remote-connect

Immediately invoke the 'auxly_skill_remote_connect' MCP tool. Then act on what it returns:

1. If it reports an ACTIVE remote connection (host, client IP from SSH_CONNECTION, remote OS), just relay that — reads/writes are central and audited on the shared host. Nothing else to do.

2. If it reports a LOCAL vault but an offered host is available on this machine, it will include an ACTION block with an ` + "`auxly connect auto`" + ` command. RUN that exact command in a shell on this machine (it wires the MCP launcher + skills, no flags, no prompts). If the command reports the box's SSH key isn't authorized on the host yet, show the user the printed public key and the one-time step. On success, tell the user to RESTART this agent so it loads the remote memory — after restart, /auxly-remote-connect will show the live link.

3. If it reports a LOCAL vault with no offer, tell the user to run ` + "`auxly host setup`" + ` on the memory host first (that publishes the offer here).

You MAY run the ` + "`auxly connect auto`" + ` command yourself (it is non-interactive and safe). You must NOT hand-edit SSH keys or config files — connect auto handles that.`},
}

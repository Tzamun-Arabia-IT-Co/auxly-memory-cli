---
name: auxly-sync
description: Append and synchronize a new fact, preference, or system detail using smart automated delta-merges into memory files (preferences.md, identity.md, infra.md, products.md, projects.md, daily.md, etc.).
argument-hint: "<fact or preference statement to sync>"
---
# /auxly-sync

You must immediately invoke the 'auxly_skill_sync' MCP tool. Pass the user's statement as the 'content' argument AND set the 'category' argument to the best-fit category from the taxonomy shown in the tool's footer (identity, personal, preferences, infra, products, projects, daily, business, agents) — you understand the fact, so you pick the file; only omit 'category' if you are genuinely unsure, in which case the router will guess. Route the user's OWN private-life facts — their family, health, relationships, and their PERSONAL legal/financial matters (their own lawsuit, court case, divorce, custody, personal loan, salary) — to category 'personal'; a company/business legal or money matter is NOT personal (use 'business'). Judge by context, not the topic word, and when a fact is about the user's private life, 'personal' wins over any topical category (a personal legal case is never a 'project'). This performs a smart automated delta-merge into the chosen memory file. Run the tool and display the confirmation output!

IMPORTANT: You are expected to proactively update the user's unified Auxly memory vault. Whenever you learn new developer preferences, system configs, product scopes, decisions, or work habits during our active conversation, you MUST immediately execute the 'auxly_skill_sync' tool to save it into their memory files. Keep the memory synchronized!
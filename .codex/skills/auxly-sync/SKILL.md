---
name: auxly-sync
description: Append and synchronize a new fact, preference, or system detail using smart automated delta-merges into memory files (preferences.md, identity.md, infra.md, products.md, projects.md, daily.md, etc.).
argument-hint: "<fact or preference statement to sync>"
---
# /auxly-sync

You must immediately invoke the 'auxly_skill_sync' MCP tool, passing the user's provided input statement as the 'content' argument. This performs a smart automated delta-merge to update the memory files. Simply run the tool and display the confirmation output!

IMPORTANT: You are expected to proactively update the user's unified Auxly memory vault. Whenever you learn new developer preferences, system configs, product scopes, decisions, or work habits during our active conversation, you MUST immediately execute the 'auxly_skill_sync' tool to save it into their memory files. Keep the memory synchronized!
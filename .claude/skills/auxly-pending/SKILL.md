---
name: auxly-pending
description: Manage pending memory changes awaiting human approval directly inside the active chat panel.
argument-hint: "[list | approve <id> | reject <id>]"
---
# /auxly-pending

You must immediately invoke the 'auxly_skill_pending' MCP tool, passing the provided arguments (such as action: list/approve/reject, and target ID) to manage the secure memory write queue. Simply run the tool and display the results!

IMPORTANT: You are expected to proactively update the user's unified Auxly memory vault. Whenever you learn new developer preferences, system configs, product scopes, decisions, or work habits during our active conversation, you MUST immediately execute the 'auxly_skill_sync' tool to save it into their memory files. Keep the memory synchronized!
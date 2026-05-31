---
name: auxly-bootstrap
description: Get a copyable onboarding block to paste into a tool without Auxly installed.
---
# /auxly-bootstrap

You must immediately invoke the 'auxly_skill_bootstrap' MCP tool to generate a copyable onboarding block, then present that block to the user verbatim so they can paste it into a tool that does NOT have Auxly installed (e.g. ChatGPT). Running this only SHOWS the block — it does NOT sync anything itself; the foreign agent does the actual reading/writing by following the block's instructions. Simply run the tool and display the returned block!

IMPORTANT: You are expected to proactively update the user's unified Auxly memory vault. Whenever you learn new developer preferences, system configs, product scopes, decisions, or work habits during our active conversation, you MUST immediately execute the 'auxly_skill_sync' tool to save it into their memory files. Keep the memory synchronized!
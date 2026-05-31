---
name: auxly-learn
description: Read the memory vault (optionally a single folder, optionally focused on a topic) and ground yourself in it for the rest of the session.
argument-hint: "[folder] [topic]"
---
# /auxly-learn

You must immediately invoke the 'auxly_skill_learn' MCP tool to read the unified memory vault and internalize it — learn everything already known about the user and operate from it for the rest of the session. Pass the optional first argument as 'folder' to read only that category/file (e.g. 'infra', 'projects'), and the optional second argument as 'topic' to focus within it (e.g. 'infra nginx'). Empty args = learn everything. Absorb the returned content and behave accordingly.

IMPORTANT: You are expected to proactively update the user's unified Auxly memory vault. Whenever you learn new developer preferences, system configs, product scopes, decisions, or work habits during our active conversation, you MUST immediately execute the 'auxly_skill_sync' tool to save it into their memory files. Keep the memory synchronized!
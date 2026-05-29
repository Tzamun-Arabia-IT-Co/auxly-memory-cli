---
name: auxly-remote-connect
description: Show the active Auxly remote connection (host, client IP, OS) and confirm this is a shared remote memory vault over SSH.
---
# /auxly-remote-connect

You must immediately invoke the 'auxly_skill_remote_connect' MCP tool to report the active remote connection: the memory host, the client IP (from SSH_CONNECTION), and the remote OS, and to confirm reads/writes are central and audited on the shared host. For setting up or managing a connection, point the user to the `auxly connect` CLI wizard (run in a terminal). This is informational only — it does NOT perform key/SSH/config changes.

IMPORTANT: You are expected to proactively update the user's unified Auxly memory vault. Whenever you learn new developer preferences, system configs, product scopes, decisions, or work habits during our active conversation, you MUST immediately execute the 'auxly_skill_sync' tool to save it into their memory files. Keep the memory synchronized!
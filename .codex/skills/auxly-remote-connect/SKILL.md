---
name: auxly-remote-connect
description: Detect and connect this machine to a remote Auxly memory host (or report the active link).
---
# /auxly-remote-connect

Immediately invoke the 'auxly_skill_remote_connect' MCP tool. Then act on what it returns:

1. If it reports an ACTIVE remote connection (host, client IP from SSH_CONNECTION, remote OS), just relay that — reads/writes are central and audited on the shared host. Nothing else to do.

2. If it reports a LOCAL vault but an offered host is available on this machine, it will include an ACTION block with an `auxly connect auto` command. RUN that exact command in a shell on this machine (it wires the MCP launcher + skills, no flags, no prompts). If the command reports the box's SSH key isn't authorized on the host yet, show the user the printed public key and the one-time step. On success, tell the user to RESTART this agent so it loads the remote memory — after restart, /auxly-remote-connect will show the live link.

3. If it reports a LOCAL vault with no offer, tell the user to run `auxly host setup` on the memory host first (that publishes the offer here).

You MAY run the `auxly connect auto` command yourself (it is non-interactive and safe). You must NOT hand-edit SSH keys or config files — connect auto handles that.

IMPORTANT: You are expected to proactively update the user's unified Auxly memory vault. Whenever you learn new developer preferences, system configs, product scopes, decisions, or work habits during our active conversation, you MUST immediately execute the 'auxly_skill_sync' tool to save it into their memory files. Keep the memory synchronized!
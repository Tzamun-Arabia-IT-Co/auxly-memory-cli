# Security Policy

We take the security of Auxly seriously. Because Auxly handles your memory, agent
credentials (read in place for the opt-in Usage feature), and SSH connections to
remote hosts, we appreciate responsible disclosure of any vulnerability.

## Supported versions

Security fixes are provided for the latest released version. Please make sure you
are on the most recent release (`auxly update`) before reporting.

| Version | Supported |
|---------|-----------|
| Latest release | ✅ |
| Older releases | ❌ |

## Reporting a vulnerability

**Please do not open a public GitHub issue for security vulnerabilities.**

Instead, report privately:

- **Email:** hi@auxly.io
- Or use GitHub's [private vulnerability reporting](https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/security/advisories/new).

Please include:

- A description of the vulnerability and its impact.
- Steps to reproduce (proof-of-concept if possible).
- The affected version (`auxly version`) and platform.
- Any suggested remediation, if you have one.

## What to expect

- **Acknowledgement** within 3 business days.
- An initial assessment and severity classification within 7 business days.
- Coordinated disclosure: we will work with you on a fix and a public advisory,
  and credit you (if you wish) once a patched release is available.

## Scope & design notes

Auxly is **local-first** by design, which shapes its threat model:

- Memory is stored as plain files under `~/.auxly/` on your machine and is never
  uploaded unless you push it to your own Git remote.
- Auxly stores **no SSH keys, VPN configuration, or network secrets**. Remote
  memory rides on SSH you already control.
- The **Usage** feature is opt-in and off by default. It reads each agent's
  existing OAuth token *in place* to query that provider's own usage endpoint;
  tokens are never logged, cached, or forwarded elsewhere.
- Every memory read/write is recorded in an append-only audit log.

Reports that demonstrate a way to exfiltrate memory, leak credentials, bypass
trust levels, or execute code through a connected agent or remote session are
especially valuable.

## Out of scope

- Vulnerabilities in third-party agents, IDEs, or providers Auxly integrates with
  (report those to the respective vendor).
- Issues requiring a pre-compromised local machine or root access.
- Missing best-practice hardening without a demonstrable exploit.

Thank you for helping keep Auxly and its users safe.

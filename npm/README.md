# auxly-cli

Local-first unified memory system for AI agents. This package downloads the
prebuilt [`auxly`](https://auxly.io) binary for your platform and verifies it
against the project's **minisign-signed** checksum manifest before installing.

```bash
npm install -g auxly-cli
auxly --help
```

## How it works

- On install, a `postinstall` step downloads `auxly-<os>-<arch>` from the GitHub
  release matching this package's version.
- It fetches the signed `auxly-<version>-checksums.txt` + `.minisig`, verifies the
  **minisign signature** against the pinned public key (compiled into the Go binary
  too), and checks the binary's **SHA-256** against the signed manifest.
- The verified binary is vendored locally and exposed as the `auxly` command.

Set `AUXLY_REQUIRE_SIGNATURE=1` to hard-fail if a signed manifest is unavailable
(by default, an unsigned legacy release installs over HTTPS without verification).

Supported: macOS / Linux / Windows on x64 / arm64. Node ≥ 16.

License: MIT · <https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli>

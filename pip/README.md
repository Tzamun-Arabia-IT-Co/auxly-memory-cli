# auxly-cli

Local-first unified memory system for AI agents. This package installs a thin
launcher that downloads the prebuilt [`auxly`](https://auxly.io) binary for your
platform on first run and verifies it against the project's **minisign-signed**
checksum manifest.

```bash
pip install auxly-cli
auxly --help
```

## How it works

- `pip install auxly-cli` installs a small pure-Python package (one wheel, all
  platforms).
- The first time you run `auxly`, it downloads `auxly-<os>-<arch>` from the GitHub
  release matching this package's version, fetches the signed
  `auxly-<version>-checksums.txt` + `.minisig`, verifies the **minisign signature**
  against the pinned public key, checks the binary's **SHA-256** against the signed
  manifest, then caches and execs it. Later runs use the cache.

Set `AUXLY_REQUIRE_SIGNATURE=1` to hard-fail if a signed manifest is unavailable.
The cache lives under `$XDG_CACHE_HOME/auxly` (or `~/.cache/auxly`).

Supported: macOS / Linux / Windows on x64 / arm64. Python ≥ 3.8.

License: MIT · <https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli>

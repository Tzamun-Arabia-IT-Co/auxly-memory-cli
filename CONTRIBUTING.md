# Contributing to Auxly

Thanks for your interest in improving Auxly! This guide covers how to build, test,
and submit changes.

## Ground rules

- Be respectful — see our [Code of Conduct](CODE_OF_CONDUCT.md).
- Open an issue to discuss substantial changes before investing in a large PR.
- Keep changes focused: one logical change per pull request.
- Auxly is **local-first** — no telemetry, no required network services. Please
  preserve that principle in any contribution.

## Prerequisites

- **Go 1.26+** (see `go.mod`).
- Git.
- A POSIX shell for the helper scripts (macOS/Linux). On Windows, use WSL or Git Bash.

No CGO toolchain is required — Auxly builds CGO-free (`CGO_ENABLED=0`) on every
platform thanks to a pure-Go SQLite driver.

## Build & run from source

```bash
git clone https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli.git
cd auxly-memory-cli

make build      # compile ./auxly
make run        # build, then run
make tui        # build, then launch the dashboard
make install    # build and install to your Go bin / PATH
```

> **Apple Silicon:** after each local build, re-sign the binary so macOS doesn't
> kill it on launch:
> ```bash
> codesign --force --sign - ./auxly
> ```

## Tests

```bash
make test            # go test ./...
go test -race ./...  # with the race detector (please run before submitting)
go test ./cmd/ -run TestUpsert -v   # a single package / test
```

Please add tests for new behavior. Pure logic (config parsing, selection,
routing, tunnel argument building, etc.) should have table-driven unit tests.
Keep tests hermetic — never touch the real `~/.auxly/` (redirect `HOME` to a temp
dir, as the existing tests do).

## Code style

- `gofmt` and `goimports` are mandatory — CI checks `gofmt -l`.
- Run `go vet ./...` before submitting; it must be clean.
- Prefer many small, focused files over large ones; keep functions small.
- Match the surrounding code's conventions, naming, and comment density.

A quick pre-flight:

```bash
gofmt -l .            # should print nothing
go vet ./...          # should be clean
go test -race ./...   # should pass
```

## Commit messages

Auxly uses [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <short summary>

<optional body explaining what and why>
```

Types: `feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `perf`, `ci`.

Examples:

```
fix(remote): multi-box relays so connecting one box no longer drops the others
feat(usage): live per-agent quota meters (opt-in)
docs(readme): rewrite for the public release
```

## Pull request flow

1. Fork the repo and create a branch off `main` (e.g. `fix/relay-singleton`).
2. Make your change with tests; keep the diff focused.
3. Ensure `gofmt`, `go vet`, and `go test -race ./...` all pass.
4. Push and open a PR against `main` with a clear description and rationale.
5. Link any related issue. CI must be green before review.

## Project layout

| Path | What lives there |
|------|------------------|
| `cmd/` | CLI commands (cobra) — `init`, `setup`, `connect`, `host`, `usage`, … |
| `tui/` | The Bubble Tea terminal dashboard |
| `mcp/` | The MCP server and its tool handlers |
| `internal/` | Core packages — memory, audit, trust, detect, usage, update, session |
| `templates/` | Seed memory files, `trust.yaml`, `git.yaml` |
| `scripts/` | Installers (`install.sh`, `install.ps1`) and helpers |

## Reporting bugs & requesting features

Use the GitHub issue templates. For security issues, **do not** open a public
issue — see [SECURITY.md](SECURITY.md).

Thanks for contributing! 🧠

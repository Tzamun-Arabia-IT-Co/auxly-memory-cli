# Releasing auxly

The release itself is one command (CI runs goreleaser on tag push). This file
is the runbook for the parts a human owns — especially the **$0 Windows
trust track**: making Defender/SmartScreen treat auxly as a known-good
publisher without paying for a certificate.

## Standard release

```sh
git tag v1.2.0 && git push origin v1.2.0
```

CI (release workflow) runs goreleaser: builds all targets (CGO-free), embeds
Windows PE version-info, signs the checksum manifest with minisign, uploads
archives + raw binaries + deb/rpm, pushes the Homebrew cask.

## Windows trust track ($0) — status & runbook

Layered, in order of impact. Each layer is independent.

### 1. PE version-info (DONE, automated)

`scripts/gen-win-versioninfo.sh` embeds CompanyName/ProductName/version into
every exe at build time. Anonymous binaries are flagged far more often.

### 2. SignPath Foundation — free OSS Authenticode (DORMANT, needs application)

Free code-signing for open-source projects. Publisher shows
**"SignPath Foundation"** (not Tzamun) — expected and fine for OSS.

**Application (human, one-time):**
1. Apply at <https://signpath.org/apply> with: the public repo URL
   (github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli), the project's OSS
   license (MIT), a maintainer email tied to the org.
2. Requirements they check: public repo, OSI license, builds reproducible
   from source in CI (goreleaser on GitHub Actions qualifies), and their
   `SignPath Foundation` attribution note added to the README once approved.
3. On approval you get an organization on app.signpath.io with a project +
   `release-signing` policy. Create a **CI user** there and copy its API token.

**Activation (after approval):**
1. Add repo secrets: `SIGNPATH_API_TOKEN`, `SIGNPATH_ORG_ID` (and optionally
   `SIGNPATH_PROJECT` / `SIGNPATH_POLICY` if the slugs differ from
   `auxly-cli` / `release-signing`).
2. Export them in the release workflow's goreleaser step env.
3. Nothing else — `.goreleaser.yaml`'s `binary_signs` hook
   (`scripts/signpath-sign.sh`) is already wired and is a no-op until the
   token exists. The next tag ships signed exes automatically.

### 3. WDSI false-positive submission (per release, ~2 min human)

Microsoft's whitelist pipeline. Do this for EVERY release while unsigned
(and for the first few signed ones):

1. Go to <https://www.microsoft.com/en-us/wdsi/filesubmission>.
2. Sign in with any Microsoft account. Select **"Software developer"**.
3. Upload `auxly-windows-amd64.exe` from the fresh release (the raw binary,
   not the zip). One submission per exe you care about (amd64 is the one
   users hit).
4. Product: Microsoft Defender Antivirus + SmartScreen. Reason:
   "Incorrectly detected as malware/PUA". Note it is an open-source CLI,
   link the repo + release URL.
5. Analyst turnaround is ~1–3 business days; detection clears via cloud
   signature update (users need no action).

### 4. winget manifest (after 2 or 3 lands)

`winget install auxly` sidesteps browser SmartScreen entirely (no Mark of
the Web on package-manager installs). Submit once, then bump per release:

1. Fork <https://github.com/microsoft/winget-pkgs>.
2. `wingetcreate new https://github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/releases/download/vX.Y.Z/auxly_windows_amd64.zip`
   → package id `TzamunArabia.auxly`.
3. PR it; automated validation + human review (~days). Later releases:
   `wingetcreate update TzamunArabia.auxly -u <new zip url> -v X.Y.Z --submit`.

### Rejected options (for the record)

- **Azure Trusted Signing**: no free tier, and unavailable for Saudi-region
  accounts at the time of writing.
- **Sigstore/cosign for the exe**: Windows does not consult it; useless for
  Defender/SmartScreen (minisign already covers our own updater's integrity).

## Release checklist

- [ ] `go test ./...` green on the release commit
- [ ] VERSION / changelog updated
- [ ] tag pushed → CI release green
- [ ] WDSI submission for the new `auxly-windows-amd64.exe` (layer 3)
- [ ] winget version bump PR (once the package exists)

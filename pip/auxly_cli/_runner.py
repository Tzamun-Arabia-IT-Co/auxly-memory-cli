"""auxly console-script entry point.

The wheel ships only this thin Python package; the actual auxly binary is
downloaded + verified on FIRST RUN into a per-user cache, then exec'd. Subsequent
runs use the cache. This keeps the wheel pure-Python (one wheel, all platforms)
while still verifying the minisign signature like the native installers do.
"""

from __future__ import annotations

import os
import platform
import stat
import sys
import urllib.request
from pathlib import Path

from ._verify import manifest_has_hash, sha256_hex, verify_minisign

REPO = "Tzamun-Arabia-IT-Co/auxly-memory-cli"
__version__ = "1.0.20"


def _target() -> tuple[str, str]:
    sys_map = {"darwin": "darwin", "linux": "linux", "windows": "windows"}
    machine = platform.machine().lower()
    arch_map = {"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}
    osname = sys_map.get(platform.system().lower())
    arch = arch_map.get(machine)
    if not osname or not arch:
        raise SystemExit(f"auxly: unsupported platform {platform.system()}/{machine}")
    ext = ".exe" if osname == "windows" else ""
    return f"auxly-{osname}-{arch}{ext}", ext


def _cache_dir() -> Path:
    base = os.environ.get("XDG_CACHE_HOME") or os.path.join(Path.home(), ".cache")
    root = Path(base) / "auxly"
    d = root / __version__
    d.mkdir(parents=True, exist_ok=True)
    # Restrict to the owner so another local user can't pre-place / swap the cached
    # binary that we exec (the cached copy is run without re-fetching the manifest).
    for p in (root, d):
        try:
            os.chmod(p, 0o700)
        except OSError:
            pass
    return d


def _fetch(url: str) -> bytes:
    req = urllib.request.Request(url, headers={"User-Agent": "auxly-pip-installer"})
    with urllib.request.urlopen(req) as resp:  # nosec - https only, verified below
        return resp.read()


def _download_and_verify(dest: Path, bin_name: str) -> None:
    base = f"https://github.com/{REPO}/releases/download/v{__version__}"
    manifest_name = f"auxly-{__version__}-checksums.txt"
    sys.stderr.write(f"auxly: downloading {bin_name} (v{__version__})…\n")
    binary = _fetch(f"{base}/{bin_name}")

    # Every release this wrapper targets (it is version-locked to a release tag)
    # ships a signed manifest, so verification is REQUIRED by default — a missing or
    # junk manifest aborts rather than running an unverified binary.
    # AUXLY_ALLOW_UNSIGNED=1 relaxes this for emergencies.
    allow_unsigned = os.environ.get("AUXLY_ALLOW_UNSIGNED") == "1"
    try:
        manifest = _fetch(f"{base}/{manifest_name}").decode("utf-8")
        sig = _fetch(f"{base}/{manifest_name}.minisig").decode("utf-8")
    except Exception as e:  # noqa: BLE001
        if not allow_unsigned:
            raise SystemExit(
                f"auxly: signed manifest unavailable ({e}) and verification is required "
                "— set AUXLY_ALLOW_UNSIGNED=1 only if you accept an unverified install"
            )
        sys.stderr.write(f"auxly: AUXLY_ALLOW_UNSIGNED=1 — installing unverified ({e})\n")
        manifest = sig = None

    if manifest and sig:
        import re

        if not re.search(r"^[0-9a-f]{64}\s+\S", manifest, re.MULTILINE):
            if not allow_unsigned:
                raise SystemExit("auxly: fetched manifest is not a checksums file — refusing")
            sys.stderr.write("auxly: AUXLY_ALLOW_UNSIGNED=1 — manifest not a checksums file; unverified\n")
        else:
            verify_minisign(manifest.encode("utf-8"), sig)  # raises on failure
            if not manifest_has_hash(manifest, sha256_hex(binary)):
                raise SystemExit("auxly: binary SHA-256 not in signed manifest — refusing")
            sys.stderr.write("auxly: signature + checksum verified ✔\n")

    tmp = dest.with_suffix(dest.suffix + ".tmp")
    tmp.write_bytes(binary)
    tmp.chmod(tmp.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
    os.replace(tmp, dest)


def _binary_path() -> Path:
    bin_name, ext = _target()
    dest = _cache_dir() / f"auxly{ext}"
    if not dest.exists() or dest.stat().st_size == 0:
        _download_and_verify(dest, bin_name)
    return dest


def main() -> None:
    binary = _binary_path()
    args = [str(binary), *sys.argv[1:]]
    if os.name == "nt":
        import subprocess

        sys.exit(subprocess.run(args).returncode)
    os.execv(str(binary), args)  # replace this process on POSIX


if __name__ == "__main__":
    main()

"""Minisign + SHA-256 verification for the auxly pip wrapper.

Mirrors npm/lib/verify.js and internal/update/verify.go: the release CI signs the
checksum manifest with minisign (prehashed "ED" mode = ed25519 over BLAKE2b-512).
We verify that signature against the PINNED public key, plus the SHA-256 integrity
of the downloaded binary against the signed manifest.
"""

from __future__ import annotations

import base64
import hashlib

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

# Pinned minisign public key (base64). MUST match internal/update/verify.go.
PUBKEY_B64 = "RWQfIGHWpXR4MtPvcbWwN1J7mx9FGsCaHMmdIpGMZAKDvmILC2Of5Q/K"


def sha256_hex(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def manifest_has_hash(manifest_text: str, hash_hex: str) -> bool:
    """True if hash_hex is the first whitespace-delimited field of any line
    (full field match, never a substring)."""
    want = hash_hex.lower()
    for line in manifest_text.splitlines():
        parts = line.strip().split()
        if parts and parts[0].lower() == want:
            return True
    return False


def _ed25519_ok(pub_raw: bytes, message: bytes, sig: bytes) -> bool:
    try:
        Ed25519PublicKey.from_public_bytes(pub_raw).verify(sig, message)
        return True
    except InvalidSignature:
        return False


def verify_minisign(file_bytes: bytes, minisig_text: str) -> str:
    """Verify a minisign signature over file_bytes using the pinned key.

    Verifies BOTH the file signature and the global (trusted-comment) signature,
    exactly as the minisign CLI does. Raises ValueError on any failure; returns
    the trusted comment on success.
    """
    pub = base64.b64decode(PUBKEY_B64)
    if len(pub) != 42:
        raise ValueError("pinned public key malformed")
    pub_keyid = pub[2:10]
    pub_raw = pub[10:42]

    lines = minisig_text.split("\n")
    if len(lines) < 4:
        raise ValueError("minisig truncated")

    sig_blob = base64.b64decode(lines[1].strip())
    if len(sig_blob) != 74:
        raise ValueError("minisig signature block malformed")
    algo = sig_blob[0:2]
    sig_keyid = sig_blob[2:10]
    sig = sig_blob[10:74]
    if sig_keyid != pub_keyid:
        raise ValueError("minisign key id mismatch")

    if algo == b"ED":
        message = hashlib.blake2b(file_bytes, digest_size=64).digest()  # prehashed
    elif algo == b"Ed":
        message = file_bytes  # legacy
    else:
        raise ValueError(f"unsupported minisign algorithm {algo!r}")

    if not _ed25519_ok(pub_raw, message, sig):
        raise ValueError("minisign file signature is INVALID")

    # Global signature binds the trusted comment to the signature.
    trusted_line = lines[2]
    global_sig = base64.b64decode(lines[3].strip())
    if len(global_sig) != 64:
        raise ValueError("minisig global signature malformed")
    prefix = "trusted comment:"
    idx = trusted_line.find(prefix)
    trusted_comment = trusted_line[idx + len(prefix):] if idx >= 0 else ""
    if trusted_comment[:1].isspace():  # strip the single separator space
        trusted_comment = trusted_comment[1:]
    global_msg = sig + trusted_comment.encode("utf-8")
    if not _ed25519_ok(pub_raw, global_msg, global_sig):
        raise ValueError("minisign trusted-comment signature is INVALID")
    return trusted_comment

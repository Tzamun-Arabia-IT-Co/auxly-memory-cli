'use strict'
// Minisign verification for the auxly npm wrapper.
//
// The auxly release CI signs the checksum manifest with minisign (prehashed
// "ED" mode: ed25519 over BLAKE2b-512 of the file). This module verifies that
// signature against the PINNED public key — the same key compiled into the Go
// binary (internal/update/verify.go) — plus the SHA-256 integrity of the
// downloaded binary against the signed manifest. Pure Node stdlib, no deps:
// crypto.createHash('blake2b512') + crypto.verify(null, …, ed25519).

const crypto = require('crypto')

// Pinned minisign public key (base64). MUST match internal/update/verify.go.
const PUBKEY_B64 = 'RWQfIGHWpXR4MtPvcbWwN1J7mx9FGsCaHMmdIpGMZAKDvmILC2Of5Q/K'

// SPKI DER prefix for a raw 32-byte Ed25519 public key.
const ED25519_SPKI_PREFIX = Buffer.from('302a300506032b6570032100', 'hex')

function sha256Hex(buf) {
  return crypto.createHash('sha256').update(buf).digest('hex')
}

// True if hashHex appears as the first whitespace-delimited field of any manifest
// line (mirrors manifestHasHash in internal/update/verify.go — a full field match,
// never a substring).
function manifestHasHash(manifestText, hashHex) {
  const want = hashHex.toLowerCase()
  return manifestText.split('\n').some((line) => {
    const first = line.trim().split(/\s+/)[0]
    return first && first.toLowerCase() === want
  })
}

function ed25519PublicKey(raw32) {
  const der = Buffer.concat([ED25519_SPKI_PREFIX, raw32])
  return crypto.createPublicKey({ key: der, format: 'der', type: 'spki' })
}

// Verify a minisign signature (.minisig text) over fileBuf using the pinned key.
// Verifies BOTH the file signature and the global (trusted-comment) signature,
// exactly as the minisign CLI does. Throws on any failure.
function verifyMinisign(fileBuf, minisigText) {
  const pub = Buffer.from(PUBKEY_B64, 'base64') // [2 algo][8 keyid][32 key]
  if (pub.length !== 42) throw new Error('pinned public key malformed')
  const pubKeyId = pub.subarray(2, 10)
  const key = ed25519PublicKey(pub.subarray(10, 42))

  // .minisig layout (4 meaningful lines):
  //   0: untrusted comment: …
  //   1: base64( [2 algo][8 keyid][64 sig] )
  //   2: trusted comment: <text>
  //   3: base64( [64 global sig] )  — ed25519 over (sig || trusted-comment text)
  const lines = minisigText.split('\n')
  const sigLine = (lines[1] || '').trim()
  const trustedLine = (lines[2] || '')
  const globalLine = (lines[3] || '').trim()
  if (!sigLine || !globalLine) throw new Error('minisig truncated')

  const sigBlob = Buffer.from(sigLine, 'base64')
  if (sigBlob.length !== 74) throw new Error('minisig signature block malformed')
  const algo = sigBlob.subarray(0, 2).toString('latin1')
  const sigKeyId = sigBlob.subarray(2, 10)
  const sig = sigBlob.subarray(10, 74)
  if (!sigKeyId.equals(pubKeyId)) throw new Error('minisign key id mismatch')

  let message
  if (algo === 'ED') {
    message = crypto.createHash('blake2b512').update(fileBuf).digest() // prehashed
  } else if (algo === 'Ed') {
    message = fileBuf // legacy
  } else {
    throw new Error('unsupported minisign algorithm: ' + algo)
  }
  if (!crypto.verify(null, message, key, sig)) {
    throw new Error('minisign file signature is INVALID')
  }

  // Global signature binds the trusted comment to the signature.
  const prefix = 'trusted comment:'
  const idx = trustedLine.indexOf(prefix)
  const trustedComment = idx >= 0 ? trustedLine.slice(idx + prefix.length).replace(/^\s/, '') : ''
  const globalSig = Buffer.from(globalLine, 'base64')
  if (globalSig.length !== 64) throw new Error('minisig global signature malformed')
  const globalMsg = Buffer.concat([sig, Buffer.from(trustedComment, 'utf8')])
  if (!crypto.verify(null, globalMsg, key, globalSig)) {
    throw new Error('minisign trusted-comment signature is INVALID')
  }
  return { trustedComment }
}

module.exports = { sha256Hex, manifestHasHash, verifyMinisign, PUBKEY_B64 }

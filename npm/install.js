#!/usr/bin/env node
'use strict'
// postinstall: download the matching auxly binary for this platform from the
// GitHub release that corresponds to THIS package version, verify it against the
// minisign-signed checksum manifest (pinned key), and drop it in vendor/.
//
// Pure Node stdlib — no dependencies. Mirrors the security posture of
// scripts/install.sh + internal/update/verify.go (SHA-256 integrity + minisign).

const fs = require('fs')
const os = require('os')
const path = require('path')
const https = require('https')
const { sha256Hex, manifestHasHash, verifyMinisign } = require('./lib/verify')

const REPO = 'Tzamun-Arabia-IT-Co/auxly-memory-cli'
const { version } = require('./package.json')

function target() {
  const platform = { darwin: 'darwin', linux: 'linux', win32: 'windows' }[os.platform()]
  const arch = { x64: 'amd64', arm64: 'arm64' }[os.arch()]
  if (!platform || !arch) {
    throw new Error(`unsupported platform/arch: ${os.platform()}/${os.arch()}`)
  }
  const ext = platform === 'windows' ? '.exe' : ''
  return { platform, arch, ext, binName: `auxly-${platform}-${arch}${ext}` }
}

// GET with redirect following (GitHub release assets redirect to a CDN). Returns a Buffer.
function fetchBuffer(url, redirects = 0) {
  return new Promise((resolve, reject) => {
    if (redirects > 8) return reject(new Error('too many redirects'))
    https
      .get(url, { headers: { 'User-Agent': 'auxly-npm-installer' } }, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          res.resume()
          const next = new URL(res.headers.location, url).toString()
          return resolve(fetchBuffer(next, redirects + 1))
        }
        if (res.statusCode !== 200) {
          res.resume()
          return reject(new Error(`HTTP ${res.statusCode} for ${url}`))
        }
        const chunks = []
        res.on('data', (c) => chunks.push(c))
        res.on('end', () => resolve(Buffer.concat(chunks)))
      })
      .on('error', reject)
  })
}

async function main() {
  const { binName, ext } = target()
  const base = `https://github.com/${REPO}/releases/download/v${version}`
  const manifestName = `auxly-${version}-checksums.txt`

  console.log(`auxly: downloading ${binName} (v${version})…`)
  const bin = await fetchBuffer(`${base}/${binName}`)

  // Integrity + authenticity. The manifest + signature ship with every release;
  // if they are absent (an unsigned legacy release) we keep SHA over HTTPS only.
  let manifest, sig
  try {
    manifest = (await fetchBuffer(`${base}/${manifestName}`)).toString('utf8')
    sig = (await fetchBuffer(`${base}/${manifestName}.minisig`)).toString('utf8')
  } catch (e) {
    if (process.env.AUXLY_REQUIRE_SIGNATURE === '1') {
      throw new Error(`signature required but manifest/sig unavailable: ${e.message}`)
    }
    console.warn(`auxly: signed manifest unavailable (${e.message}); proceeding on HTTPS + no verification`)
  }

  if (manifest && sig) {
    const looksLikeManifest = /^[0-9a-f]{64}\s+\S/m.test(manifest)
    if (!looksLikeManifest) {
      if (process.env.AUXLY_REQUIRE_SIGNATURE === '1') {
        throw new Error('fetched manifest is not a checksums file')
      }
      console.warn('auxly: manifest did not look like a checksums file; skipping verification')
    } else {
      verifyMinisign(Buffer.from(manifest, 'utf8'), sig) // throws on failure
      const hash = sha256Hex(bin)
      if (!manifestHasHash(manifest, hash)) {
        throw new Error('downloaded binary SHA-256 is not in the signed manifest — refusing to install')
      }
      console.log('auxly: signature + checksum verified ✔')
    }
  }

  const vendorDir = path.join(__dirname, 'vendor')
  fs.mkdirSync(vendorDir, { recursive: true })
  const dest = path.join(vendorDir, `auxly${ext}`)
  fs.writeFileSync(dest, bin, { mode: 0o755 })
  console.log(`auxly: installed -> ${dest}`)
}

main().catch((err) => {
  console.error(`\nauxly install failed: ${err.message}`)
  console.error('You can install manually from https://github.com/' + REPO + '/releases')
  process.exit(1)
})

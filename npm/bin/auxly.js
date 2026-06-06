#!/usr/bin/env node
'use strict'
// Thin shim: exec the vendored auxly binary, forwarding args + stdio + exit code.
const path = require('path')
const { spawnSync } = require('child_process')

const ext = process.platform === 'win32' ? '.exe' : ''
const bin = path.join(__dirname, '..', 'vendor', `auxly${ext}`)

const res = spawnSync(bin, process.argv.slice(2), { stdio: 'inherit' })
if (res.error) {
  if (res.error.code === 'ENOENT') {
    console.error('auxly: binary not found — reinstall with `npm rebuild auxly-cli` or `npm i -g auxly-cli`')
  } else {
    console.error(`auxly: ${res.error.message}`)
  }
  process.exit(1)
}
process.exit(res.status === null ? 1 : res.status)

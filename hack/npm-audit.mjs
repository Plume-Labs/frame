#!/usr/bin/env node
// `npm audit --audit-level=high` with time-boxed exceptions.
//
// npm has no ignore list, so a single advisory with no fixed release blocks
// every PR. This wrapper keeps the same gate (any high or critical advisory
// fails) but lets .npm-audit-ignore.json waive a named advisory, on three
// conditions: a written reason, an expiry date, and nothing else. An expired
// waiver fails the gate again, so an exception cannot be forgotten.
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'

const RANK = { info: 0, low: 1, moderate: 2, high: 3, critical: 4 }
const MIN = RANK.high
const today = new Date().toISOString().slice(0, 10)

const waivers = JSON.parse(readFileSync(new URL('../.npm-audit-ignore.json', import.meta.url), 'utf8'))
const active = new Map()
let bad = false
for (const w of waivers) {
  if (!w.id || !w.reason || !/^\d{4}-\d{2}-\d{2}$/.test(w.expires ?? '')) {
    console.error(`::error::waiver ${JSON.stringify(w)} needs id, reason and expires (YYYY-MM-DD)`)
    bad = true
  } else if (w.expires < today) {
    console.error(`::error::waiver for ${w.id} expired on ${w.expires}: fix the dependency or renew it with a new reason`)
    bad = true
  } else active.set(w.id, w)
}

let out
try {
  out = execFileSync('npm', ['audit', '--json'], { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 })
} catch (e) {
  out = e.stdout // npm audit exits 1 when it finds something
  if (!out) throw e
}
const report = JSON.parse(out)

// An advisory is an object in a vulnerability's `via`; string entries only
// say "vulnerable through another package" and carry no advisory of their own.
const advisories = new Map()
for (const v of Object.values(report.vulnerabilities ?? {})) {
  for (const via of v.via ?? []) {
    if (typeof via === 'object' && RANK[via.severity] >= MIN) {
      const id = (via.url ?? '').split('/').pop() || String(via.source)
      advisories.set(id, via)
    }
  }
}

for (const [id, a] of advisories) {
  const w = active.get(id)
  if (w) {
    console.log(`waived  ${id}  ${a.name}  (until ${w.expires}): ${w.reason}`)
  } else {
    console.error(`::error::${id} ${a.name} [${a.severity}] ${a.title} ${a.url}`)
    bad = true
  }
}
if (!advisories.size) console.log('npm audit: no high or critical advisory')
process.exit(bad ? 1 : 0)

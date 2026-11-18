// Screenshots of the console on the `make demo` cluster: 1440x900 at 2x, the
// default theme and the dark one, fictional demo data only.
//
//   hack/demo.sh up
//   npm install --no-save --no-package-lock playwright@1.63.0
//   npx playwright install chromium
//   node hack/screenshots.mjs [outdir]        # default docs/img/screens
//
// It signs in the way hack/demo.sh verify does, with the one-shot bootstrap
// token, but through the browser, so the session cookie lands in the page and
// no passkey is needed. That uses the token up: run this instead of
// `hack/demo.sh verify` on a cluster, not after it.
import { chromium } from 'playwright'
import { mkdir, readFile } from 'node:fs/promises'
import { homedir } from 'node:os'
import { join } from 'node:path'

const port = process.env.DEMO_PORT ?? '18080'
const base = `http://localhost:${port}`
const stateDir =
  process.env.DEMO_STATE_DIR ??
  join(process.env.XDG_STATE_HOME ?? join(homedir(), '.local/state'), 'hearthrack-demo')
const outDir = process.argv[2] ?? 'docs/img/screens'
const email = process.env.DEMO_ADMIN_EMAIL ?? 'admin@hearthrack.demo'

// [file name, route fragment (#/<screen>/<tab>, see src/App.tsx NAV), text the
// screen must show before it is shot]
const screens = [
  ['overview', '#/overview', null],
  ['nodes', '#/nodes', 'hearthrack-demo-worker'],
  ['racks', '#/nodes/racks', 'rack-a'],
  ['jobs', '#/jobs', 'demo-training-run'],
  ['scheduling', '#/scheduling', 'demo-high'],
  ['alerts', '#/security/alerts', 'NodeDiskPressure'],
  ['volumes', '#/storage/storage-load', 'demo-datasets'],
]

const token = (await readFile(join(stateDir, 'bootstrap-token'), 'utf8')).trim()
await mkdir(outDir, { recursive: true })

const browser = await chromium.launch()
const context = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  deviceScaleFactor: 2,
})
const page = await context.newPage()

// What a screen could not read is the first thing to know when it is empty.
page.on('response', (r) => {
  if (r.status() >= 400 && /\/(api|apis)\//.test(r.url())) console.log(`HTTP ${r.status()} ${r.url()}`)
})
page.on('pageerror', (e) => console.log(`page error: ${e.message}`))

await page.goto(base)
const status = await page.evaluate(
  async ({ token, email }) => {
    const res = await fetch('/auth/bootstrap', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token, email }),
    })
    return res.status
  },
  { token, email },
)
if (status !== 204) throw new Error(`/auth/bootstrap answered HTTP ${status} (token already used?)`)

// The console has one theme (dark): `.dark` on <html> changes nothing today.
for (const [name, route, text] of screens) {
  await page.goto(`${base}/${route}`)
  await page.reload()
  if (text) {
    try {
      await page.getByText(text).first().waitFor({ timeout: 30_000 })
    } catch (err) {
      await page.screenshot({ path: join(outDir, `debug-${name}.png`) })
      throw err
    }
  }
  // Not `networkidle`: the console polls the apiserver, so it never goes idle.
  await page.waitForTimeout(4000) // first fetches, charts and counters settle
  const file = join(outDir, `${name}.png`)
  await page.screenshot({ path: file })
  console.log(`wrote ${file}`)
}

await browser.close()

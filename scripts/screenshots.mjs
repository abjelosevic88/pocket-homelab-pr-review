// README screenshots and a browser smoke test, against a demo instance (DEMO_DATA=true seeds a made-up shop's pull requests):
//
//   DATA_DIR=/tmp/ppr-demo DEMO_DATA=true ADMIN_EMAIL=demo@example.com ADMIN_PASSWORD=demodemo1 ADMIN_NAME=Maya PORT=18220 ./pocket-pr-review
//   docker run -d --name pprchrome --network host --shm-size=1g zenika/alpine-chrome:124 --no-sandbox --headless --disable-gpu --disable-dev-shm-usage \
//     --hide-scrollbars --remote-debugging-port=19272 --remote-debugging-address=127.0.0.1 '--remote-allow-origins=*' about:blank
//   CDP=http://127.0.0.1:19272 BASE=http://127.0.0.1:18220 node scripts/screenshots.mjs   # any Chrome with remote debugging
//   SHOTS=0 … node scripts/screenshots.mjs                                                  # checks only
// playwright-core comes from web/node_modules, or set PW_FROM to a package.json next to an install of it.
import { createRequire } from 'node:module'
import assert from 'node:assert/strict'

const require = createRequire(process.env.PW_FROM || new URL('../web/package.json', import.meta.url))
let pw
try { pw = require('playwright') } catch { pw = require('playwright-core') }
const { chromium } = pw
const base = process.env.BASE || 'http://127.0.0.1:18220'
const shots = process.env.SHOTS !== '0'
const only = process.env.ONLY ? new Set(process.env.ONLY.split(',')) : null
const dir = process.env.OUT || new URL('../docs/screenshots/', import.meta.url).pathname
const browser = process.env.CDP ? await chromium.connectOverCDP(process.env.CDP) : await chromium.launch()
const errors = []

async function session({ phone = false, dark = false } = {}) {
  const ctx = await browser.newContext({
    viewport: phone ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    deviceScaleFactor: phone ? 3 : 2, isMobile: phone, hasTouch: phone, colorScheme: dark ? 'dark' : 'light',
    userAgent: phone ? 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1' : undefined,
  })
  const page = await ctx.newPage()
  page.on('pageerror', e => errors.push(`pageerror: ${e}`))
  page.on('console', m => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) errors.push(m.text()) })
  await page.goto(base + '/login')
  const res = await page.evaluate(() => fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email: 'demo@example.com', password: 'demodemo1' }) }).then(r => r.status))
  assert.equal(res, 200, 'demo sign-in')
  return { ctx, page }
}
async function go(page, path, wait = 1000) {
  await page.goto(base + path, { waitUntil: 'networkidle' })
  // headless Chrome freezes CSS fade-ins: show everything at once
  await page.addStyleTag({ content: '*, *::before, *::after { transition: none !important; animation: none !important; }' })
  await page.evaluate(() => document.fonts.ready)
  await page.waitForTimeout(wait)
}
async function shot(page, name, full = false) { if (shots && (!only || only.has(name))) await page.screenshot({ path: dir + name + '.png', fullPage: full }) }

// ---- desktop ----
{
  const { ctx, page } = await session()
  await go(page, '/')
  assert.ok((await page.content()).includes('Coupons: create, redeem'), 'the list shows the demo pull request')
  await shot(page, 'desktop-list')
  await go(page, '/pr/1')
  assert.ok((await page.content()).includes('In one minute'), 'the overview opens first')
  await shot(page, 'desktop-overview')
  await go(page, '/pr/1?view=steps&step=1-the-coupons-table')
  await shot(page, 'desktop-step')
  await go(page, '/pr/1?view=steps&step=4-routes-for-checkout-and-admins')
  assert.ok((await page.content()).includes('part:'), 'router.go is split into parts')
  await shot(page, 'desktop-step-parts')
  await go(page, '/pr/1?view=steps&step=3-what-a-coupon-takes-off')
  await shot(page, 'desktop-step-comments')
  await go(page, '/pr/1?view=built')
  assert.ok((await page.content()).includes('Redeem has to be atomic'), 'the agent\'s edits are on the timeline')
  await page.locator('.tl-file-btn').nth(2).click()
  await page.waitForTimeout(300)
  await shot(page, 'desktop-built')
  await go(page, '/pr/1?view=files')
  await go(page, '/pr/1?view=steps&step=7-docs')
  await page.keyboard.press('c')
  await page.waitForTimeout(500)
  await shot(page, 'desktop-review')
  await page.keyboard.press('Escape')
  for (const p of ['/open', '/mine', '/done', '/archived', '/reviews', '/repo/acme/shop', '/pr/2', '/pr/4', '/settings/github', '/settings/walk', '/settings/notify', '/settings/apps', '/settings/ai', '/settings/data', '/settings/about']) await go(page, p, 400)
  await go(page, '/settings/walk')
  await shot(page, 'desktop-settings')
  await ctx.close()
}

// ---- phone ----
{
  const { ctx, page } = await session({ phone: true, dark: true })
  await go(page, '/')
  await shot(page, 'phone-list')
  await go(page, '/pr/1?view=steps&step=2-reading-and-redeeming-coupons')
  await shot(page, 'phone-step')
  await go(page, '/pr/1')
  await shot(page, 'phone-overview')
  await ctx.close()
}

await browser.close()
if (errors.length) {
  console.error('browser errors:\n' + errors.join('\n'))
  process.exit(1)
}
console.log('ok')

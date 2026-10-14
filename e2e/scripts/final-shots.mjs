// Final-state screenshots for the report: run page + app page + space overview.
import { chromium } from '@playwright/test'
import { mkdirSync } from 'node:fs'

const BASE = process.env.DEMO_BASE_URL || 'http://127.0.0.1:5173'
const ADMIN = {
  username: process.env.KITE_E2E_ADMIN_USERNAME || 'admin',
  password: process.env.KITE_E2E_ADMIN_PASSWORD || 'admin12345',
}
const ART = new URL('./artifacts/', import.meta.url).pathname
mkdirSync(ART, { recursive: true })

const browser = await chromium.launch()
const context = await browser.newContext({ viewport: { width: 1680, height: 950 }, locale: 'en-US' })
await context.addInitScript(() => {
  localStorage.setItem('i18nextLng', 'en')
  localStorage.setItem('work-mode', 'application')
  localStorage.setItem('current-space', 'demo')
})
const page = await context.newPage()

await page.goto(`${BASE}/login`)
await page.getByLabel('Username').fill(ADMIN.username)
await page.getByLabel('Password', { exact: true }).fill(ADMIN.password)
await page.getByRole('button', { name: 'Sign In with Password' }).click()
await page.waitForURL((u) => !u.pathname.startsWith('/login'))

// Workflows are addressed by numeric ID; resolve the demo pipeline and its
// latest run through the API (page.request shares the login cookies).
const wfList = await (await page.request.get(`${BASE}/api/v1/workflows`)).json()
const wf = (wfList.workflows ?? []).find((w) => w.name === 'demo-pipeline')
if (!wf) throw new Error('demo-pipeline workflow not found')
const runsRes = await page.request.get(`${BASE}/api/v1/workflows/${wf.id}/runs?limit=1`)
const runId = (await runsRes.json()).runs?.[0]?.id
if (!runId) throw new Error('no runs found for demo-pipeline')

await page.goto(`${BASE}/workflows/${wf.id}/runs/${runId}`)
await page.getByText('Build Image').first().waitFor()
await page.waitForTimeout(1500)
await page.screenshot({ path: `${ART}/16-run-final.png` })

await page.goto(`${BASE}/applications/demo/demo-app/clusters/dev1`)
await page.waitForTimeout(2500)
await page.screenshot({ path: `${ART}/17-app-deployed.png` })

await page.goto(`${BASE}/workflows`)
await page.waitForTimeout(2000)
await page.screenshot({ path: `${ART}/18-workflow-list.png` })

console.log('final screenshots saved')
await browser.close()

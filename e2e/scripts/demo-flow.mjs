// One-off live-env demo flow against the Vite dev server (127.0.0.1:5173):
// create space -> create app -> add gitea git + docker registry credentials ->
// create workflow (build-image -> deploy-app -> notify) -> run it to success.
//
// Run: source /data/cicd/credentials.env first (CICD_PASS is required):
//   set -a; . /data/cicd/credentials.env; set +a
//   node e2e/scripts/demo-flow.mjs   (deps from e2e/node_modules)
import { chromium } from '@playwright/test'
import { mkdirSync } from 'node:fs'

const BASE = process.env.DEMO_BASE_URL || 'http://127.0.0.1:5173'
const ADMIN = {
  username: process.env.KITE_E2E_ADMIN_USERNAME || 'admin',
  password: process.env.KITE_E2E_ADMIN_PASSWORD || 'admin12345',
}
// Real generated credential of the local CI/CD env — never in the repo.
const CICD = {
  username: process.env.CICD_USER || 'cicd',
  password: process.env.CICD_PASS || '',
}
if (!CICD.password) {
  console.error('missing CICD_PASS — source /data/cicd/credentials.env first')
  process.exit(1)
}
const GIT_REPO = 'http://gitea.local:3000/cicd/demo-app.git'
const REGISTRY_HOST = 'registry.local:5000'
const TAG = `run-${Date.now()}`
const OUTPUT_REF = `${REGISTRY_HOST}/demo/demo-app:${TAG}`
const NOTIFY_URL = 'http://127.0.0.1:9999'

const SPACE = 'demo'
const APP = 'demo-app'
const WF = 'demo-pipeline'

const ART = new URL('./artifacts/', import.meta.url).pathname
mkdirSync(ART, { recursive: true })

const log = (...a) => console.log(`[${new Date().toISOString().slice(11, 19)}]`, ...a)
const shot = (page, name) => page.screenshot({ path: `${ART}/${name}.png`, fullPage: false })

const browser = await chromium.launch()
const context = await browser.newContext({
  viewport: { width: 1680, height: 950 },
  locale: 'en-US',
})
await context.addInitScript(() => {
  localStorage.setItem('i18nextLng', 'en')
  localStorage.setItem('work-mode', 'application')
  localStorage.removeItem('current-space')
})
const page = await context.newPage()
page.setDefaultTimeout(20000)

// Field block in the workflow property drawer: <div class="space-y-1.5"><label/>control</div>
const field = (label) =>
  page.locator('div.space-y-1\\.5').filter({ has: page.getByText(label, { exact: true }) }).first()

async function pickSelect(fieldLoc, option) {
  await fieldLoc.locator('[data-slot="select-trigger"]').click()
  await page.getByRole('option', { name: option, exact: true }).click()
}

// HTML5 drop with the custom MIME the palette sets (no click-to-add exists).
async function dropNode(type, x, y) {
  await page.evaluate(
    ([t, px, py]) => {
      const pane = document.querySelector('.react-flow__pane')
      const r = pane.getBoundingClientRect()
      const dt = new DataTransfer()
      dt.setData('application/kite-workflow-node', t)
      pane.dispatchEvent(
        new DragEvent('drop', {
          bubbles: true,
          cancelable: true,
          dataTransfer: dt,
          clientX: r.left + px,
          clientY: r.top + py,
        })
      )
    },
    [type, x, y]
  )
  await page.waitForTimeout(400)
}

// Edge by real mouse drag between node handles (d3-drag listens on document).
async function connectNodes(fromId, toId) {
  const src = page.locator(`.react-flow__handle[data-nodeid="${fromId}"].source`)
  const dst = page.locator(`.react-flow__handle[data-nodeid="${toId}"].target`)
  const a = await src.boundingBox()
  const b = await dst.boundingBox()
  await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2)
  await page.mouse.down()
  await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 12 })
  await page.mouse.up()
  await page.waitForTimeout(300)
}

try {
  // ---------------------------------------------------------------- login
  await page.goto(`${BASE}/login`)
  await page.getByLabel('Username').fill(ADMIN.username)
  await page.getByLabel('Password', { exact: true }).fill(ADMIN.password)
  await page.getByRole('button', { name: 'Sign In with Password' }).click()
  await page.waitForURL((u) => !u.pathname.startsWith('/login'))
  log('logged in')

  // ---------------------------------------------------------- create space
  await page.goto(`${BASE}/applications`)
  const openPicker = () =>
    page.locator('header').getByRole('button', { name: /Spaces/ }).click()
  const spaceItem = () => page.getByRole('button', { name: new RegExp(`^${SPACE}$`) })
  await openPicker()
  if (!(await spaceItem().count())) {
    await page.getByRole('button', { name: /New Space/ }).click()
    const spaceDialog = page.getByRole('dialog')
    await spaceDialog.getByPlaceholder(/Space name/).fill(SPACE)
    // one cluster is preselected; fill its cpu/memory quota spinbuttons
    const quota = spaceDialog.locator('input[type="number"]')
    await quota.nth(0).fill('2')
    await quota.nth(1).fill('4')
    await shot(page, '01-space-dialog')
    await spaceDialog.getByRole('button', { name: 'Create', exact: true }).click()
    await page.getByText(/created on/).waitFor()
    log('space created')
    await openPicker()
  }
  await spaceItem().click()
  await page.waitForURL((u) => u.searchParams.get('space') === SPACE)
  await shot(page, '02-applications-empty-space')

  // ------------------------------------------------------ create application
  const appRow = page.getByRole('row').filter({ hasText: APP })
  if (!(await appRow.count())) {
    await page.getByRole('button', { name: /Create Application/ }).click()
    const wiz = page.getByRole('dialog')
    await wiz.getByPlaceholder('my-app').fill(APP)
    await wiz.locator('#container-image').fill(`${REGISTRY_HOST}/library/nginx:alpine`)
    await wiz.getByLabel('CPU Limit').fill('0.5')
    await wiz.getByLabel('Memory Limit').fill('512')
    await shot(page, '03-wizard-basic')
    await wiz.getByRole('button', { name: 'Next', exact: true }).click() // -> environment
    await wiz.getByRole('button', { name: 'Next', exact: true }).click() // -> config files
    await wiz.getByRole('button', { name: 'Next', exact: true }).click() // -> network
    await wiz.getByRole('button', { name: 'Next', exact: true }).click() // -> target cluster
    await wiz.locator('[data-slot="select-trigger"]').click()
    await page.getByRole('option', { name: /^dev1\b/ }).click()
    await wiz.getByRole('button', { name: 'Next', exact: true }).click() // -> preview
    await shot(page, '04-wizard-preview')
    await wiz.getByRole('button', { name: 'Create & Publish' }).click()
    // App detail URLs are /applications/:space/:name
    await page.waitForURL(new RegExp(`/applications/${SPACE}/${APP}`))
    log('application created & published')
  } else {
    log('application already exists, skipping wizard')
  }
  await shot(page, '05-app-detail')

  // ---------------------------------------------- credentials (global settings)
  await page.goto(`${BASE}/application-settings`)
  await page.getByText('Git Credentials').waitFor()
  // let the credential list query settle before existence checks
  await page.waitForTimeout(2000)
  // credential dialogs have unlabeled Field blocks (div + Label + Input)
  const credInput = (dlg, label) =>
    dlg
      .locator('div.space-y-1\\.5, div.space-y-1')
      .filter({ hasText: label })
      .first()
      .locator('input')

  // Git credential for the gitea source
  if (!(await page.getByText('gitea', { exact: true }).count())) {
    await page.getByTitle('Create Credential').nth(0).click()
    let cred = page.getByRole('dialog')
    await cred.getByPlaceholder('registry-mirror').fill('gitea')
    await cred.getByPlaceholder('git.example.com').fill('gitea.local:3000')
    await credInput(cred, 'Username').fill(CICD.username)
    await cred.locator('input[type="password"]').fill(CICD.password)
    await shot(page, '06-git-credential')
    await cred.getByRole('button', { name: 'Create', exact: true }).click()
    await page.getByRole('dialog').waitFor({ state: 'detached' })
    log('git credential created')
  } else {
    log('git credential already exists')
  }

  // Registry credential for image push/pull against dev-registry
  if (!(await page.getByText('dev-registry', { exact: true }).count())) {
    await page.getByTitle('Create Credential').nth(1).click()
    const cred = page.getByRole('dialog')
    await cred.getByPlaceholder('registry-mirror').fill('dev-registry')
    const hostInput = cred.getByPlaceholder('ghcr.io')
    const row = hostInput.locator('xpath=ancestor::div[contains(@class,"grid")][1]')
    await hostInput.fill(REGISTRY_HOST)
    await row.locator('input').nth(1).fill(CICD.username)
    await row.locator('input[type="password"]').fill(CICD.password)
    await shot(page, '07-registry-credential')
    await cred.getByRole('button', { name: 'Create', exact: true }).click()
    await page.getByRole('dialog').waitFor({ state: 'detached' })
    log('registry credential created')
  } else {
    log('registry credential already exists')
  }
  await shot(page, '08-credentials-done')

  // ------------------------------------------------------ create workflow
  await page.goto(`${BASE}/workflows`)
  await page.getByRole('heading', { name: 'Workflows' }).waitFor()
  // wait until the table query settles (row appears or empty state)
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[role="row"]').length > 1 ||
      document.body.innerText.includes('No workflows yet'),
    { timeout: 15000 }
  ).catch(() => {})
  const wfRow = page.getByRole('row').filter({ hasText: WF })
  log('workflow row count:', await wfRow.count())
  if (await wfRow.count()) {
    log('workflow already exists, skipping editor')
  } else {
    await page.goto(`${BASE}/workflows/new`)
    // The editor has no space selector: definitions are global (ID identity)
    await page.getByPlaceholder(/Name \*/).fill(WF)

    await dropNode('build-image', 220, 110)
    await dropNode('deploy-app', 560, 110)
    await dropNode('notify', 900, 110)
    await shot(page, '09-nodes-dropped')

    // build-image node props
    await page.locator('.react-flow__node[data-id="buildimage1"]').click()
    await pickSelect(field('Git Credential'), 'gitea')
    await pickSelect(field('Registry Credential'), 'dev-registry')
    await field('Repository URL').getByRole('textbox').fill(GIT_REPO)
    await field('Ref (branch / tag / SHA)').getByRole('textbox').fill('main')
    await field('Dockerfile Path').getByRole('textbox').fill('Dockerfile')
    await field('Output Image Ref').getByRole('textbox').fill(OUTPUT_REF)
    await shot(page, '10-build-node-props')

    // deploy-app node props (the app picker lists "space / name" composites)
    await page.locator('.react-flow__node[data-id="deployapp1"]').click()
    await field('Application').getByRole('combobox').click()
    await page.getByRole('option', { name: `${SPACE} / ${APP}`, exact: true }).click()
    await pickSelect(field('Cluster'), 'dev1')
    await field('Image').getByRole('textbox').fill('{{steps.buildimage1.outputs.imageRef}}')
    await shot(page, '11-deploy-node-props')

    // notify node props
    await page.locator('.react-flow__node[data-id="notify1"]').click()
    await field('Webhook URL').getByRole('textbox').fill(NOTIFY_URL)
    await shot(page, '12-notify-node-props')

    // edges: build -> deploy -> notify
    await connectNodes('buildimage1', 'deployapp1')
    await connectNodes('deployapp1', 'notify1')
    await shot(page, '13-workflow-wired')

    await page.getByRole('button', { name: /Validate/ }).click()
    await page.waitForTimeout(800)
    await shot(page, '14-validated')
    await page.getByRole('button', { name: 'Save', exact: true }).click()
    // Saving redirects to /workflows/:id (numeric ID identity)
    await page.waitForURL(/\/workflows\/\d+/)
    log('workflow saved')
  }

  // ------------------------------------------------------------- run it
  log('opening run action for workflow…')
  await page.goto(`${BASE}/workflows`)
  const row = page.getByRole('row').filter({ hasText: WF })
  await row.waitFor()
  await row.getByRole('button', { name: 'Actions' }).click()
  await page.getByRole('menuitem', { name: 'Run', exact: true }).click()
  await shot(page, '15-run-dialog')
  await page.getByRole('button', { name: /Run$/ }).click()
  log('run clicked, waiting for run page…')
  await page.waitForURL(/\/workflows\/\d+\/runs\/\d+/)
  const runId = page.url().match(/runs\/(\d+)/)[1]
  log(`run #${runId} started, waiting for completion…`)

  // poll the run API directly (UI chip text is unreliable across reloads)
  const deadline = Date.now() + 10 * 60 * 1000
  let final = null
  while (Date.now() < deadline) {
    const res = await context.request.get(`${BASE}/api/v1/runs/${runId}`)
    const run = await res.json()
    final = run.status
    log('run status:', final)
    if (['succeeded', 'failed', 'cancelled'].includes(final)) break
    await page.waitForTimeout(8000)
    await page.reload()
  }
  await shot(page, '16-run-final')
  // Workflows are addressed by numeric ID; resolve this one from the list
  // (it may have been created by an earlier run of the script).
  const wfListRes = await context.request.get(`${BASE}/api/v1/workflows`)
  const wfEntry = ((await wfListRes.json()).workflows ?? []).find((w) => w.name === WF)
  if (!wfEntry) throw new Error(`workflow ${WF} not found`)
  const wfRes = await context.request.get(`${BASE}/api/v1/workflows/${wfEntry.id}`)
  const wfSpec = await wfRes.json()
  const buildNode = (wfSpec.spec?.nodes ?? []).find((n) => n.type === 'build-image')
  const builtImage = buildNode?.props?.outputRef ?? OUTPUT_REF
  if (final !== 'succeeded') {
    const res = await context.request.get(`${BASE}/api/v1/runs/${runId}`)
    const run = await res.json()
    for (const n of run.nodes ?? []) {
      if (n.error) console.error(`  ${n.nodeId}: ${n.error}`)
    }
    throw new Error(`workflow run ended as: ${final}`)
  }
  log(`SUCCESS — image: ${builtImage}`)
} catch (err) {
  await shot(page, '99-failure').catch(() => {})
  console.error('FAILED:', err.message)
  process.exitCode = 1
} finally {
  await browser.close()
}

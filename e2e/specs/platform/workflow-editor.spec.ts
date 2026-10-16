import {
  expect,
  request as playwrightRequest,
  test,
  type APIRequestContext,
  type APIResponse,
  type Locator,
  type Page,
} from '@playwright/test'

import { authFile } from '../../env'

// A wide viewport: the canvas re-fits (and zooms in) once the dropped nodes
// measure, and the larger frame keeps the whole fitted graph visible even
// with the property drawer open.
test.use({ storageState: authFile, viewport: { width: 1680, height: 950 } })

// Workflow editor structural regression (the create-workflow half of
// demo-flow.mjs, turned into real tests): list render, palette drops (HTML5
// drop with the custom MIME — flow.tsx onDrop), the property drawer fields
// (property-drawer.tsx Field blocks), handle-to-handle edges (d3-drag on
// document), save → revision toast → list row, editor replay, the run-params
// and webhook-token dialogs (run-dialogs.tsx), the params/triggers dialogs
// (definition-dialogs.tsx) and the empty runs views. The run itself is never
// triggered: executing needs gitea/registry credentials no environment has.
// Fixtures are API-seeded with a unique stamp and torn down through the
// workflow module API (modules/workflow/internal/api/routes.go: DELETE
// /workflows/:id) plus the application space endpoints.

const STAMP = Date.now().toString(36)
const SPACE = `e2e${STAMP}-wfs`
const APP = `e2e${STAMP}-wfapp`
const WF_NAME = `e2e-wf-${STAMP}`
const WF_DESCRIPTION = 'workflow editor e2e fixture'
const GIT_REPO = `https://git.example.com/e2e/${APP}.git`
const OUTPUT_REF = `registry.example.com/e2e/${APP}:${STAMP}`
const NOTIFY_URL = 'http://127.0.0.1:9999/hook'
const IMAGE_TEMPLATE = '{{steps.buildimage1.outputs.imageRef}}'

interface ClusterView {
  name: string
  error?: string
}

async function expectJSON<T>(response: APIResponse, status: number): Promise<T> {
  const text = await response.text()
  expect(response.status(), text).toBe(status)
  return JSON.parse(text) as T
}

// Clusters that can actually take a space quota: the overview probe fails for
// registered-but-unreachable clusters.
async function deployableClusters(request: APIRequestContext) {
  const clusters = await expectJSON<ClusterView[]>(
    await request.get('/api/v1/clusters'),
    200
  )
  const usable: string[] = []
  for (const name of clusters.map((cluster) => cluster.name).sort()) {
    const overview = await request
      .get(`/api/v1/_clusters/${encodeURIComponent(name)}/overview`)
      .catch(() => undefined)
    if (overview?.ok()) usable.push(name)
  }
  return usable
}

// Space adoption: labeled namespace + managed quota per cluster (the workflow
// editor binds a create draft to the ?space= entry context, and the deploy-app
// palette item only appears once a space is bound).
async function seedSpace(
  request: APIRequestContext,
  space: string,
  clusters: string[]
) {
  const upsert = await expectJSON<{
    succeeded: string[]
    failed: Array<{ cluster: string; error: string }>
  }>(
    await request.put(`/api/v1/applications/spaces/${space}/quotas`, {
      data: {
        quotas: clusters.map((cluster) => ({
          cluster,
          cpu: '1',
          memory: '1Gi',
        })),
      },
    }),
    200
  )
  expect(upsert.failed).toEqual([])
}

// Minimal renderable AppSpec (render/spec.go Validate) as a draft — no
// cluster config, so no publish and nothing to offline.
function appSpec() {
  return {
    workload: {
      type: 'deployment',
      image: 'registry.k8s.io/pause:3.10.1',
      replicas: 1,
      limits: { cpu: '100m', memory: '64Mi' },
    },
  }
}

// ── editor interaction helpers (translated from e2e/scripts/demo-flow.mjs) ──

// Field block in the property drawer: <div class="space-y-1.5"><label/>control</div>
const field = (page: Page, label: string) =>
  page
    .locator('div.space-y-1\\.5')
    .filter({ has: page.getByText(label, { exact: true }) })
    .first()

async function pickSelect(fieldLoc: Locator, option: string) {
  await fieldLoc.locator('[data-slot="select-trigger"]').click()
  await fieldLoc
    .page()
    .getByRole('option', { name: option, exact: true })
    .click()
}

// HTML5 drop with the custom MIME the palette sets (flow.tsx onDrop; there is
// no click-to-add). The canvas mounts empty and re-runs fitView once the
// first node measures, so a fixed client offset would land under a moving
// zoom. Invert screenToFlowPosition with the live viewport transform instead,
// making the node land at the exact flow position whatever the zoom is.
async function dropNode(page: Page, type: string, x: number, y: number) {
  await page.evaluate(
    ([nodeType, fx, fy]) => {
      const pane = document.querySelector('.react-flow__pane')
      const viewport = document.querySelector('.react-flow__viewport')
      if (!pane || !viewport) throw new Error('react-flow canvas not mounted')
      const matrix = getComputedStyle(viewport).transform
      const t = new DOMMatrixReadOnly(matrix === 'none' ? undefined : matrix)
      const bounds = pane.getBoundingClientRect()
      const dt = new DataTransfer()
      dt.setData('application/kite-workflow-node', nodeType as string)
      pane.dispatchEvent(
        new DragEvent('drop', {
          bubbles: true,
          cancelable: true,
          dataTransfer: dt,
          clientX: bounds.left + t.e + (fx as number) * t.a,
          clientY: bounds.top + t.f + (fy as number) * t.d,
        })
      )
    },
    [type, x, y]
  )
  await page.waitForTimeout(400)
}

// Edge by real mouse drag between node handles (d3-drag listens on document).
// The drag coordinates are screen positions, so both handles must be on the
// viewport — a boundingBox alone also exists for nodes parked off-screen.
async function connectNodes(page: Page, fromId: string, toId: string) {
  const src = page.locator(`.react-flow__handle[data-nodeid="${fromId}"].source`)
  const dst = page.locator(`.react-flow__handle[data-nodeid="${toId}"].target`)
  const a = await src.boundingBox()
  const b = await dst.boundingBox()
  const onScreen = (box: { x: number; y: number; width: number; height: number } | null, id: string) => {
    expect(box, `handle of ${id} has a layout box`).not.toBeNull()
    const { width, height } = page.viewportSize()!
    const cx = box!.x + box!.width / 2
    const cy = box!.y + box!.height / 2
    expect(cx, `handle of ${id} is inside the viewport`).toBeGreaterThan(0)
    expect(cx).toBeLessThan(width)
    expect(cy, `handle of ${id} is inside the viewport`).toBeGreaterThan(0)
    expect(cy).toBeLessThan(height)
  }
  onScreen(a, fromId)
  onScreen(b, toId)
  await page.mouse.move(a!.x + a!.width / 2, a!.y + a!.height / 2)
  await page.mouse.down()
  await page.mouse.move(b!.x + b!.width / 2, b!.y + b!.height / 2, {
    steps: 12,
  })
  await page.mouse.up()
  await page.waitForTimeout(300)
}

const rowOf = (page: Page, name: string) =>
  page.getByRole('row').filter({ hasText: name })

// Shared across the serial chain: the definition saved by the journey test.
let wfId = 0

test.describe('platform workflow editor', () => {
  // Deterministic English + "all spaces" entry state, per fresh context.
  // Context-scoped: no global preference is ever persisted.
  test.beforeEach(async ({ context }) => {
    await context.addInitScript(() => {
      localStorage.setItem('i18nextLng', 'en')
      localStorage.removeItem('current-space')
    })
  })

  // Scenario 1: the list page renders (rows or the empty state) and Create
  // opens the editor.
  test('list renders and Create Workflow opens the editor', async ({
    page,
  }) => {
    await page.goto('/portal/workflows')
    await expect(page.getByRole('heading', { name: 'Workflows' })).toBeVisible()
    // Let the list query settle: a data row appears or the empty state shows.
    await expect
      .poll(
        async () =>
          (await page.getByRole('row').count()) > 1 ||
          (await page.getByText('No workflows yet').count()) > 0
      )
      .toBe(true)

    await page.getByRole('button', { name: /Create Workflow/ }).click()
    await page.waitForURL(/\/portal\/workflows\/new$/)
    await expect(page.locator('.react-flow__pane')).toBeVisible()
    await expect(page.getByPlaceholder('Name *')).toBeVisible()
    await expect(page.getByText('Drag onto the canvas')).toBeVisible()
  })

  // Scenario 2: drop build → deploy → notify, fill the drawer fields, wire
  // the edges, validate, save (revision toast + editor route), find the row
  // back in the list.
  test('build, deploy, notify journey: drawer fields, edges, save, list row', async ({
    page,
    request,
  }) => {
    const clusters = await deployableClusters(request)
    test.skip(
      clusters.length === 0,
      'the journey fixtures need one deployable cluster for the space quota'
    )
    await seedSpace(request, SPACE, clusters)
    await expectJSON(
      await request.post('/api/v1/applications', {
        data: {
          name: APP,
          namespace: SPACE,
          description: 'workflow editor e2e app fixture',
          spec: appSpec(),
        },
      }),
      201
    )

    // The editor has no space selector: the create draft binds to the
    // ?space= entry context, which also un-hides the deploy-app palette item.
    await page.goto(`/portal/workflows/new?space=${SPACE}`)
    await expect(page.locator('.react-flow__pane')).toBeVisible()
    await expect(
      page.getByText('Deploy App', { exact: true }).first()
    ).toBeVisible()

    await page.getByPlaceholder('Name *').fill(WF_NAME)
    await page.getByPlaceholder('Description').fill(WF_DESCRIPTION)

    // Drop positions in flow units, demo-flow's, with notify pulled left so
    // its handles stay clear of the property drawer even when it is open.
    await dropNode(page, 'build-image', 220, 110)
    await dropNode(page, 'deploy-app', 560, 110)
    await dropNode(page, 'notify', 760, 110)
    await expect(page.locator('.react-flow__node')).toHaveCount(3)
    for (const id of ['buildimage1', 'deployapp1', 'notify1']) {
      await expect(page.locator(`.react-flow__node[data-id="${id}"]`)).toBeVisible()
    }
    // The drop-driven re-fits race the node measurements and can leave the
    // last node parked off-screen; the controls' Fit View refits every node
    // deterministically before any handle drag.
    await page.locator('.react-flow__controls-fitview').click()
    await page.waitForTimeout(300)

    // edges: build -> deploy -> notify. Wired before any drawer opens, so the
    // handles are never clipped or stacked during the drag.
    await connectNodes(page, 'buildimage1', 'deployapp1')
    await connectNodes(page, 'deployapp1', 'notify1')
    await expect(page.locator('.react-flow__edge')).toHaveCount(2)

    // build-image node props. Each drawer is closed again with a pane click
    // (which clears the selection) so the next node is fully visible.
    await page.locator('.react-flow__node[data-id="buildimage1"]').click()
    await expect(
      field(page, 'Repository URL').getByRole('textbox')
    ).toBeVisible()
    await field(page, 'Repository URL').getByRole('textbox').fill(GIT_REPO)
    await field(page, 'Ref (branch / tag / SHA)')
      .getByRole('textbox')
      .fill('main')
    await field(page, 'Dockerfile Path').getByRole('textbox').fill('Dockerfile')
    await field(page, 'Output Image Ref').getByRole('textbox').fill(OUTPUT_REF)
    await page.locator('.react-flow__pane').click({ position: { x: 5, y: 5 } })
    await expect(field(page, 'Repository URL')).toHaveCount(0)

    // deploy-app node props: the space is locked to the workflow's own space
    // and the app combobox lists that space's applications.
    await page.locator('.react-flow__node[data-id="deployapp1"]').click()
    const spaceInput = field(page, 'Space').locator('input')
    await expect(spaceInput).toBeDisabled()
    await expect(spaceInput).toHaveValue(SPACE)
    await field(page, 'Application').getByRole('combobox').click()
    // The combobox is a Popover+Command pair, not a radix select: options are
    // cmdk command items. Only the fixture app is picked — the list is
    // filtered to the fixture space, so pre-existing data is unreachable.
    await page
      .locator('[data-slot="command-item"]')
      .filter({ hasText: APP })
      .click()
    await expect(
      field(page, 'Application').getByRole('combobox')
    ).toContainText(APP)
    await pickSelect(field(page, 'Cluster'), clusters[0])
    await field(page, 'Image').getByRole('textbox').fill(IMAGE_TEMPLATE)
    await page.locator('.react-flow__pane').click({ position: { x: 5, y: 5 } })
    await expect(field(page, 'Application')).toHaveCount(0)

    // notify node props
    await page.locator('.react-flow__node[data-id="notify1"]').click()
    await field(page, 'Webhook URL').getByRole('textbox').fill(NOTIFY_URL)
    await page.locator('.react-flow__pane').click({ position: { x: 5, y: 5 } })
    await expect(field(page, 'Webhook URL')).toHaveCount(0)

    await page.getByRole('button', { name: /Validate/ }).click()
    await expect(
      page.getByText('Validation passed: acyclic, templates resolve')
    ).toBeVisible()

    await page.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(page.getByText('Saved as revision 1')).toBeVisible()
    await page.waitForURL(/\/portal\/workflows\/\d+$/)
    wfId = Number(new URL(page.url()).pathname.split('/').pop())
    expect(wfId, 'save redirected to the numeric workflow id').toBeGreaterThan(0)

    await page.goto(`/portal/workflows?space=${SPACE}`)
    const row = rowOf(page, WF_NAME)
    await expect(row).toBeVisible()
    await expect(row.getByText('rev 1')).toBeVisible()
  })

  // Scenario 3: reopening the editor replays the definition — nodes, edges
  // and the saved drawer values come back.
  test('reopening the editor replays nodes, edges and drawer values', async ({
    page,
  }) => {
    test.skip(wfId === 0, 'depends on the workflow saved by the journey test')
    await page.goto(`/portal/workflows/${wfId}`)
    await expect(page.locator('.react-flow__node')).toHaveCount(3)
    await expect(page.locator('.react-flow__edge')).toHaveCount(2)
    await expect(page.getByPlaceholder('Name *')).toHaveValue(WF_NAME)
    await expect(
      page.locator('.react-flow__node[data-id="buildimage1"]')
    ).toContainText('Build Image')
    await expect(
      page.locator('.react-flow__node[data-id="notify1"]')
    ).toContainText('Notify')

    await page.locator('.react-flow__node[data-id="buildimage1"]').click()
    await expect(
      field(page, 'Repository URL').getByRole('textbox')
    ).toHaveValue(GIT_REPO)
    await expect(
      field(page, 'Output Image Ref').getByRole('textbox')
    ).toHaveValue(OUTPUT_REF)
    await page.locator('.react-flow__node[data-id="deployapp1"]').click()
    await expect(field(page, 'Space').locator('input')).toHaveValue(SPACE)
    await expect(
      field(page, 'Application').getByRole('combobox')
    ).toContainText(APP)
  })

  // Scenario 4: the manual trigger dialog opens and renders its form; the
  // run is never executed (no gitea/registry credentials to rely on).
  test('manual trigger dialog renders the parameter form', async ({ page }) => {
    test.skip(wfId === 0, 'depends on the workflow saved by the journey test')
    await page.goto(`/portal/workflows?space=${SPACE}`)
    const row = rowOf(page, WF_NAME)
    await expect(row).toBeVisible()
    await row.getByRole('button', { name: 'Run', exact: true }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading')).toContainText(`Run ${WF_NAME}`)
    // The fixture definition carries no params: the empty-schema form shows.
    await expect(
      dialog.getByText('This workflow has no parameters.')
    ).toBeVisible()
    await expect(dialog.getByRole('button', { name: /Run$/ })).toBeVisible()
    // Close without triggering.
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(page.getByRole('dialog')).toHaveCount(0)
  })

  // Scenario 5: the webhook token dialog opens; the token is visible and
  // copyable (non-empty, 128-bit hex per workflows_api.go generateToken).
  test('webhook token dialog shows a copyable token', async ({ page }) => {
    test.skip(wfId === 0, 'depends on the workflow saved by the journey test')
    await page.goto(`/portal/workflows?space=${SPACE}`)
    const row = rowOf(page, WF_NAME)
    await expect(row).toBeVisible()
    await row.getByRole('button', { name: 'Actions' }).click()
    await page.getByRole('menuitem', { name: 'Webhook Token' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading')).toContainText('Webhook Token')
    const codes = dialog.locator('code')
    await expect(codes.first()).toContainText('/api/workflow-hooks/')
    await expect(codes.nth(1)).toHaveText(/^[0-9a-f]{32}$/)
    // Both rows expose a Copy button.
    await expect(dialog.getByRole('button', { name: 'Copy' })).toHaveCount(2)
    await page.keyboard.press('Escape')
    await expect(page.getByRole('dialog')).toHaveCount(0)
  })

  // Scenario 6: the definition dialogs open — the params list grows a row
  // from a preset (draft only, cancelled) and the triggers form renders.
  test('params and triggers dialogs open from the editor', async ({ page }) => {
    test.skip(wfId === 0, 'depends on the workflow saved by the journey test')
    await page.goto(`/portal/workflows/${wfId}`)
    await expect(page.locator('.react-flow__node')).toHaveCount(3)

    await page.getByRole('button', { name: 'Params', exact: true }).click()
    const paramsDialog = page.getByRole('dialog')
    await expect(
      paramsDialog.getByRole('heading', { name: 'Workflow Parameters' })
    ).toBeVisible()
    await expect(
      paramsDialog.getByText('No parameters defined.')
    ).toBeVisible()
    // The add form works on the draft: the imageTag preset appends a row.
    await paramsDialog.getByRole('button', { name: /imageTag/ }).click()
    await expect(
      paramsDialog.getByText('No parameters defined.')
    ).toHaveCount(0)
    // Cancel: the draft is discarded, the definition untouched.
    await paramsDialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(page.getByRole('dialog')).toHaveCount(0)

    await page.getByRole('button', { name: 'Triggers', exact: true }).click()
    const triggersDialog = page.getByRole('dialog')
    await expect(
      triggersDialog.getByRole('heading', { name: 'Triggers', exact: true })
    ).toBeVisible()
    for (const rowLabel of ['Manual', 'Webhook (push)', 'Cron']) {
      await expect(triggersDialog.getByText(rowLabel, { exact: true })).toBeVisible()
    }
    await expect(
      triggersDialog.getByText('Max Concurrent Runs')
    ).toBeVisible()
    await triggersDialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(page.getByRole('dialog')).toHaveCount(0)
  })

  // Scenario 7: the runs views render without any run, then the definition
  // is deleted through the API (DELETE /api/v1/workflows/:id).
  test('runs views render empty and the workflow deletes via the API', async ({
    page,
    request,
  }) => {
    test.skip(wfId === 0, 'depends on the workflow saved by the journey test')
    await page.goto(`/portal/workflows/${wfId}/runs`)
    await expect(
      page.getByRole('heading', { name: `Runs · ${WF_NAME}` })
    ).toBeVisible()
    await expect(
      page.getByText('No runs yet for this workflow.')
    ).toBeVisible()

    // A run id that does not exist renders the API's error text instead of a
    // crashed page.
    await page.goto(`/portal/workflows/${wfId}/runs/99999999`)
    await expect(page.getByText('workflow run not found')).toBeVisible()

    const deleted = await request.delete(`/api/v1/workflows/${wfId}`)
    expect(deleted.status(), await deleted.text()).toBe(200)
    await expect
      .poll(async () => (await request.get(`/api/v1/workflows/${wfId}`)).status())
      .toBe(404)
    wfId = 0
  })

  // Best-effort teardown for whatever the serial chain left behind: the
  // definition (addressed by its unique name), the draft app and the space
  // binding.
  test.afterAll(async () => {
    const baseURL =
      process.env.KITE_E2E_BASE_URL ||
      `http://127.0.0.1:${process.env.KITE_E2E_PORT || '38080'}`
    let context: APIRequestContext | undefined
    try {
      context = await playwrightRequest.newContext({
        baseURL,
        storageState: authFile,
      })
      const list = await context.get('/api/v1/workflows')
      if (list.ok()) {
        const body = (await list.json()) as {
          workflows?: Array<{ id: number; name: string }>
        }
        for (const wf of body.workflows ?? []) {
          if (wf.name === WF_NAME) {
            await context
              .delete(`/api/v1/workflows/${wf.id}`)
              .catch(() => undefined)
          }
        }
      }
      const app = await context
        .get(`/api/v1/applications/${encodeURIComponent(SPACE)}/${encodeURIComponent(APP)}`)
        .catch(() => undefined)
      if (app?.ok()) {
        for (const config of ((await app.json()) as {
          configs?: Array<{ cluster: string }>
        }).configs ?? []) {
          await context
            .post(
              `/api/v1/applications/${encodeURIComponent(SPACE)}/${encodeURIComponent(APP)}/configs/${encodeURIComponent(config.cluster)}/offline`
            )
            .catch(() => undefined)
        }
        await context
          .delete(
            `/api/v1/applications/${encodeURIComponent(SPACE)}/${encodeURIComponent(APP)}`
          )
          .catch(() => undefined)
      }
      const clusters = await context.get('/api/v1/clusters')
      if (clusters.ok()) {
        const names = ((await clusters.json()) as Array<{ name: string }>).map(
          (cluster) => cluster.name
        )
        await context
          .delete(`/api/v1/applications/spaces/${encodeURIComponent(SPACE)}`, {
            data: { clusters: names },
          })
          .catch(() => undefined)
      }
    } catch {
      // best effort
    } finally {
      await context?.dispose()
    }
  })
})

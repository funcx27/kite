import {
  expect,
  request as playwrightRequest,
  test,
  type APIRequestContext,
  type APIResponse,
  type Page,
} from '@playwright/test'

import { authFile } from '../../env'

test.use({ storageState: authFile })

// Application pod page five-view regression (application-pod-page.tsx):
// enter through the detail page's pod table, then walk Monitoring / Terminal /
// Logs / Files / YAML. The pod is a real registry.k8s.io/pause pod behind an
// API-published fixture app; the pod identity (space/app/cluster/pod) rides
// the URL, so later views navigate directly. Request shapes follow
// modules/application/internal/api/routes.go (runtime_api.go GetResources).

interface ClusterView {
  name: string
  error?: string
}

interface PodInfo {
  name: string
  phase?: string
  status?: string
  containers?: string[]
}

interface AppResponse {
  id: number
  name: string
  namespace: string
  configs: Array<{ cluster: string }>
  deploy?: { success: boolean }
}

async function expectJSON<T>(response: APIResponse, status: number): Promise<T> {
  const text = await response.text()
  expect(response.status(), text).toBe(status)
  return JSON.parse(text) as T
}

// Minimal renderable AppSpec (render/spec.go Validate: deployment, image,
// cpu+memory limits mandatory; requests default from limits).
function appSpec() {
  return {
    workload: {
      type: 'deployment',
      image: IMAGE,
      replicas: 1,
      limits: { cpu: '100m', memory: '64Mi' },
    },
  }
}

// Clusters that can actually take a publish: the overview probe fails for
// registered-but-unreachable clusters, which a bare GET /clusters cannot
// distinguish (same probe as app-list-interactions.spec.ts).
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

// Best-effort teardown in the first batch's order: offline every config (the
// delete gate refuses deployed apps), delete the application row, then strip
// the space marker. The cluster list comes from state, not from the create
// response — the create-with-publish chain never backfills app.Configs in
// its response (apps_api.go createAndDeployApp), so configs always reads [].
// Nothing pre-existing is touched — the names are unique per run.
async function cleanup(request: APIRequestContext) {
  if (state.cluster) {
    await request
      .post(
        `/api/v1/applications/${encodeURIComponent(SPACE)}/${encodeURIComponent(APP)}/configs/${encodeURIComponent(state.cluster)}/offline`
      )
      .catch(() => undefined)
    await request
      .delete(
        `/api/v1/applications/${encodeURIComponent(SPACE)}/${encodeURIComponent(APP)}`
      )
      .catch(() => undefined)
    await request
      .delete(`/api/v1/applications/spaces/${encodeURIComponent(SPACE)}`, {
        data: { clusters: [state.cluster] },
      })
      .catch(() => undefined)
  }
}

// Fixture state shared by the serial view tests; the pod's live status is
// recorded so non-Running environments degrade to skips with a reason instead
// of false failures — page-render assertions never skip on that.
const STAMP = Date.now().toString(36)
const SPACE = `e2e-${STAMP}-pod`
const APP = `e2e-${STAMP}-pause`
const IMAGE = 'registry.k8s.io/pause:3.10.1'

const state = {
  cluster: '',
  podName: '',
  // status || phase, exactly what the UI chip renders
  observedStatus: '',
  podSeen: false,
  podReady: false,
}

const notReadyReason = () =>
  `pod fixture never reached Running within 120s (last status: ${state.observedStatus || 'none'})`

const detailUrl = () =>
  `/portal/applications/${encodeURIComponent(SPACE)}/${encodeURIComponent(APP)}/clusters/${encodeURIComponent(state.cluster)}`
const podUrl = () => `${detailUrl()}/pods/${encodeURIComponent(state.podName)}`

// The pod page header chip (application-pod-page.tsx): a rounded-full span
// carrying the status text; same matcher style as the wizard journey spec.
const statusChip = (page: Page, label: string) =>
  page.locator('span.rounded-full').filter({ hasText: label }).first()

test.describe.serial('application pod views', () => {
  // Fixture: publish the pause app through the application API, then poll
  // the aggregated runtime view (GET .../configs/:cluster/resources) until
  // the pod exists and reports Running (120s budget). Publish failure fails
  // here; a poll timeout only downgrades the Running-dependent assertions of
  // the following tests to documented skips.
  test('fixture: publish pause app and wait for its pod to run', async ({
    request,
  }) => {
    test.setTimeout(240_000)
    const deployable = await deployableClusters(request)
    test.skip(
      deployable.length === 0,
      'pod fixtures need one deployable cluster to publish an app'
    )
    state.cluster = deployable[0]
    await seedSpace(request, SPACE, [state.cluster])

    const app = await expectJSON<AppResponse>(
      await request.post('/api/v1/applications', {
        data: {
          name: APP,
          namespace: SPACE,
          description: `pod views fixture ${STAMP}`,
          spec: appSpec(),
          cluster: state.cluster,
        },
      }),
      201
    )
    expect(app.deploy?.success, 'publish on create').toBe(true)

    const deadline = Date.now() + 120_000
    while (Date.now() < deadline) {
      const view = await expectJSON<{ pods?: { items?: PodInfo[] } }>(
        await request.get(
          `/api/v1/applications/${encodeURIComponent(SPACE)}/${encodeURIComponent(APP)}/configs/${encodeURIComponent(state.cluster)}/resources`
        ),
        200
      )
      const pod = (view.pods?.items ?? [])[0]
      if (pod) {
        state.podSeen = true
        state.podName = pod.name
        state.observedStatus = pod.status || pod.phase || ''
        if (state.observedStatus === 'Running') {
          state.podReady = true
          break
        }
      }
      await new Promise((resolve) => setTimeout(resolve, 3_000))
    }
    expect(state.podSeen, 'the published app produced a pod').toBe(true)
    expect(state.podName).not.toBe('')
  })

  // Scenario 1: enter from the detail page's pod table by clicking the pod
  // name; the pod page opens on the Monitoring view with name + status.
  test('monitoring view shows the pod name and status after entering from the pod table', async ({
    page,
  }) => {
    test.skip(!state.podSeen, 'no pod was created, the pod table stays empty')
    await page.goto(detailUrl())

    // The pod table row's name is a button (cluster-tab.tsx) navigating to
    // the pod page — the interaction under test.
    await page
      .getByRole('button', { name: state.podName, exact: true })
      .click()
    await page.waitForURL((url) =>
      url.pathname.endsWith(`/pods/${encodeURIComponent(state.podName)}`)
    )

    // Header: the pod name switcher and the live status chip.
    const nameButton = page.getByRole('button', {
      name: state.podName,
      exact: true,
    })
    await expect(nameButton).toBeVisible()
    await expect(
      page.getByRole('tab', { name: 'Monitoring', exact: true })
    ).toHaveAttribute('aria-selected', 'true')
    await expect(
      page.getByRole('combobox').filter({ hasText: 'Last 30 min' })
    ).toBeVisible()
    // The chip shows whatever the runtime API last reported — the
    // status-visibility assertion is live in either environment; only the
    // green Running styling below depends on the fixture actually being
    // Ready.
    const chip = statusChip(page, state.observedStatus)
    await expect(chip).toBeVisible()
    test.skip(!state.podReady, notReadyReason())
    await expect(chip).toHaveClass(/green/)
  })

  // Scenario 2: the Terminal tab mounts the xterm container (DOM only — no
  // interaction output is asserted).
  test('terminal tab mounts the xterm container', async ({ page }) => {
    test.skip(!state.podSeen, 'no pod was created, the pod page has no tabs')
    await page.goto(podUrl())
    await page.getByRole('tab', { name: 'Terminal', exact: true }).click()
    await page.waitForURL((url) => url.searchParams.get('view') === 'terminal')
    await expect(page.locator('.xterm').first()).toBeVisible()
  })

  // Scenario 3: the Logs view renders its panel. The pause container emits
  // no output — that is expected, so the assertion targets the panel itself
  // (Monaco editor mounted, filter input present), never log content.
  test('logs view renders the log panel', async ({ page }) => {
    test.skip(!state.podSeen, 'no pod was created, the pod page has no tabs')
    await page.goto(podUrl())
    await page.getByRole('tab', { name: 'Logs', exact: true }).click()
    await page.waitForURL((url) => url.searchParams.get('view') === 'logs')
    // CardTitle renders a plain div (ui/card.tsx), not a heading element.
    await expect(
      page.locator('[data-slot="card-title"]').filter({ hasText: 'Logs' })
    ).toBeVisible()
    await expect(page.locator('.monaco-editor').first()).toBeVisible()
    await expect(
      page.getByPlaceholder('Filter logs...', { exact: true })
    ).toBeVisible()
  })

  // Scenario 4: the Files view renders the container file browser. The
  // resolved listing state is asserted, never specific entries: the mandated
  // pause image ships no shell `ls`, so the exec-based listing resolves to
  // the view's explicit "not supported" error card (a correct degradation,
  // not a bug); a listing-capable container would show root rows or the
  // 'No files found' empty state. All three are accepted as rendered.
  test('files view renders the container file browser', async ({ page }) => {
    test.skip(!state.podSeen, 'no pod was created, the pod page has no tabs')
    await page.goto(podUrl())
    await page.getByRole('tab', { name: 'Files', exact: true }).click()
    await page.waitForURL((url) => url.searchParams.get('view') === 'files')
    // Browser chrome: path input rooted at / and the file table rendered
    // (pod-file-browser.tsx).
    const pathInput = page.getByLabel('Current path', { exact: true })
    await expect(pathInput).toBeVisible()
    await expect(pathInput).toHaveValue('/')
    await expect(page.getByText('Loading files...')).toHaveCount(0)
    await expect(
      page
        .getByText('No files found', { exact: true })
        .or(page.locator('tbody tr').first())
        .or(page.getByText('Error loading pod files'))
    ).toBeVisible()
  })

  // Scenario 5: the YAML view shows the live pod manifest — kind: Pod and
  // the pod name must appear in the rendered content (SimpleYamlEditor /
  // Monaco .view-lines).
  test('yaml view shows the pod manifest', async ({ page }) => {
    test.skip(!state.podSeen, 'no pod was created, the pod page has no tabs')
    await page.goto(podUrl())
    await page.getByRole('tab', { name: 'YAML', exact: true }).click()
    await page.waitForURL((url) => url.searchParams.get('view') === 'yaml')
    const lines = page.locator('.view-lines')
    await expect(lines).toContainText('kind: Pod')
    await expect(lines).toContainText(state.podName)
  })

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
      await cleanup(context)
    } catch {
      // best-effort
    } finally {
      await context?.dispose()
    }
  })
})

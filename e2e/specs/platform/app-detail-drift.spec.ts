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

// Application detail page drift resolution regression: a save-only edit
// (仅保存) moves the DB manifests off the published anchor without touching
// the cluster, so the config lands in the pending state (status_api.go
// statusOf: DB-side-only move → pending). The tests then walk the whole
// loop on the cluster tab — drift doc row, live-vs-DB YAML diff, cluster-side
// pick → resolve → synced, the revision history — plus the two-step restart
// confirm. Fixtures are API-seeded into a dedicated space and torn down in
// afterAll; request shapes follow modules/application/internal/api/routes.go.

// Unique per-run fixtures; nothing pre-existing is touched.
const STAMP = Date.now().toString(36)
const SPACE = `e2e-${STAMP}-drift`
const APP = `e2e-${STAMP}-driftapp`
const IMAGE = 'registry.k8s.io/pause:3.10.1'

interface ClusterView {
  name: string
  error?: string
}

interface StatusDoc {
  kind: string
  namespace?: string
  name: string
  missingIn?: string
  paths?: string[]
}

interface StatusReport {
  status: string
  docs: StatusDoc[]
  previewToken?: string
  checkedAt: string
  error?: string
}

// Minimal renderable AppSpec (render/spec.go Validate: deployment, image,
// cpu+memory limits mandatory); replicas is the field the save-only edit
// moves to manufacture the pending diff.
function appSpec(replicas: number) {
  return {
    workload: {
      type: 'deployment',
      image: IMAGE,
      replicas,
      limits: { cpu: '100m', memory: '64Mi' },
    },
  }
}

async function expectJSON<T>(response: APIResponse, status: number): Promise<T> {
  const text = await response.text()
  expect(response.status(), text).toBe(status)
  return JSON.parse(text) as T
}

const configPath = (cluster: string, suffix = '') =>
  `/api/v1/applications/${encodeURIComponent(SPACE)}/${encodeURIComponent(APP)}/configs/${encodeURIComponent(cluster)}${suffix}`

const detailUrl = (cluster: string) =>
  `/portal/applications/${encodeURIComponent(SPACE)}/${encodeURIComponent(APP)}/clusters/${encodeURIComponent(cluster)}`

// Clusters that can actually take a publish: the overview probe fails for
// registered-but-unreachable clusters, which a bare GET /clusters cannot
// distinguish.
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

// The status endpoint re-runs the server-side drift check on every call
// (status_api.go GetStatus), so polling it shows the moment the cluster
// state and the stored manifests diverge — or converge again.
async function statusReport(request: APIRequestContext, cluster: string) {
  return expectJSON<StatusReport>(
    await request.get(configPath(cluster, '/status')),
    200
  )
}

// The detail page's status chip (status-badge.tsx ApplicationStatusChip);
// green styling rides the same classes as the label.
const statusChip = (page: Page, label: string) =>
  page.locator('span.rounded-full').filter({ hasText: label }).first()

// The status query only runs on mount and react-query never refetches it on
// its own — each page.reload() remounts the cluster tab and re-runs the
// server-side check (same trick as app-wizard-journey.spec.ts).
async function reloadUntilChip(page: Page, label: string, timeout: number) {
  await expect
    .poll(
      async () => {
        await page.reload()
        try {
          await statusChip(page, label)
            .first()
            .waitFor({ state: 'visible', timeout: 10_000 })
          return label
        } catch {
          return 'not-yet'
        }
      },
      { timeout, intervals: [5_000] }
    )
    .toBe(label)
}

const driftDocRow = (page: Page) =>
  page.getByRole('button').filter({ hasText: `Deployment/${APP}` })

test.describe.serial('platform application detail drift resolution', () => {
  // Resolved in the first test from the deployable-cluster probe; empty when
  // the fixtures never made it up (tests 2–5 skip, afterAll no-ops).
  let driftCluster = ''

  // Scenario 1 — fixture chain plus the pending surface: after the save-only
  // edit the status chip flips to the pending label and the consistency card
  // lists the drifted Deployment document with its changed-path count.
  test('save-only edit turns the cluster tab pending with a drift doc row', async ({
    page,
    request,
  }) => {
    const cluster = (await deployableClusters(request))[0]
    test.skip(
      !cluster,
      'drift fixtures need one deployable cluster to publish the app'
    )
    driftCluster = cluster

    // Space (labeled namespace + managed quota) on the one fixture cluster.
    const upsert = await expectJSON<{
      succeeded: string[]
      failed: Array<{ cluster: string; error: string }>
    }>(
      await request.put(`/api/v1/applications/spaces/${SPACE}/quotas`, {
        data: {
          quotas: [
            { cluster, cpu: '1', memory: '1Gi' },
          ],
        },
      }),
      200
    )
    expect(upsert.failed).toEqual([])

    // Create + first publish in one chain; the publish read-back anchors the
    // config, so a right-after status check must already say synced.
    const app = await expectJSON<{ deploy?: { success: boolean } }>(
      await request.post('/api/v1/applications', {
        data: {
          name: APP,
          namespace: SPACE,
          description: 'drift resolution fixture',
          spec: appSpec(1),
          cluster,
        },
      }),
      201
    )
    expect(app.deploy?.success, 'first publish on create').toBe(true)
    await expect
      .poll(async () => (await statusReport(request, cluster)).status, {
        timeout: 120_000,
        intervals: [3_000],
      })
      .toBe('synced')

    // 仅保存: persist a spec revision (replicas 1→2) without publishing —
    // the cluster keeps running the anchored set, so only the DB side moves.
    const cfg = await expectJSON<{ latestRev: number }>(
      await request.get(configPath(cluster)),
      200
    )
    const saved = await expectJSON<{ success: boolean; saveOnly?: boolean }>(
      await request.post(configPath(cluster, '/deploy'), {
        data: {
          spec: appSpec(2),
          expectedRev: cfg.latestRev,
          saveOnly: true,
        },
      }),
      200
    )
    expect(saved.saveOnly, 'save-only publish response').toBe(true)
    await expect
      .poll(
        async () => {
          const report = await statusReport(request, cluster)
          const docs = (report.docs ?? [])
            .map(
              (doc) =>
                `${doc.kind}/${doc.name}:${(doc.paths ?? []).join('|')}`
            )
            .join(',')
          return `${report.status}|${docs}`
        },
        { timeout: 60_000 }
      )
      .toContain(`pending|Deployment/${APP}:spec.replicas`)

    // The tab renders the pending verdict and the drift document row.
    await page.goto(detailUrl(cluster))
    await reloadUntilChip(page, 'Unpublished changes', 120_000)
    await expect(
      page.getByText('Config Consistency', { exact: true })
    ).toBeVisible()
    const row = driftDocRow(page)
    await expect(row).toBeVisible()
    await expect(row).toContainText('1 changed paths')
  })

  // Scenario 2 — the drift document row opens the YAML diff: pane badges
  // name cluster live vs DB config and the changed replicas lines show.
  test('drift doc opens the YAML diff with cluster-live vs DB panes', async ({
    page,
  }) => {
    test.skip(!driftCluster, 'drift fixture was not created')
    await page.goto(detailUrl(driftCluster))
    const row = driftDocRow(page)
    await expect(row).toBeVisible()
    await row.click()

    const dialog = page.getByRole('dialog')
    await expect(
      dialog.getByRole('heading', { name: `Deployment/${APP}` })
    ).toBeVisible({ timeout: 30_000 })
    await expect(
      dialog.getByText('Cluster (live)', { exact: true })
    ).toBeVisible()
    await expect(dialog.getByText('DB config', { exact: true })).toBeVisible()
    // The one-hunk diff: cluster runs replicas 1, the DB config carries 2.
    await expect(dialog).toContainText('replicas: 1')
    await expect(dialog).toContainText('replicas: 2')
    // Per-path side-pick arrows float over the seam once the diff computed.
    await expect(
      dialog.getByTitle('spec.replicas · Use cluster')
    ).toBeVisible({ timeout: 30_000 })
    await expect(dialog.getByTitle('spec.replicas · Use DB')).toBeVisible()

    await dialog.getByRole('button', { name: 'Close' }).click()
    await expect(page.getByRole('dialog')).toHaveCount(0)
  })

  // Scenario 3 — side-pick the cluster side for the changed path, apply
  // through the confirm dialog and poll the tab back to Synced.
  test('cluster-side pick applies via confirm and returns to synced', async ({
    page,
    request,
  }) => {
    test.skip(!driftCluster, 'drift fixture was not created')
    await page.goto(detailUrl(driftCluster))
    const row = driftDocRow(page)
    await expect(row).toBeVisible()
    await row.click()

    const dialog = page.getByRole('dialog')
    const clusterPick = dialog.getByTitle('spec.replicas · Use cluster')
    await expect(clusterPick).toBeVisible({ timeout: 30_000 })

    // Unpicked paths count toward the DB side and leave Apply disabled;
    // taking the cluster side moves the whole count over.
    const apply = dialog.getByRole('button', { name: /Apply · cluster/ })
    await expect(apply).toHaveText('Apply · cluster 0 / DB 1')
    await expect(apply).toBeDisabled()
    await clusterPick.click()
    await expect(apply).toHaveText('Apply · cluster 1 / DB 0')
    await expect(apply).toBeEnabled()
    await apply.click()

    // The confirm spells out the side split before the resolve commits.
    const confirm = page.getByRole('dialog').filter({
      hasText: 'Apply selection?',
    })
    await expect(
      confirm.getByText('Cluster side (1 paths)', { exact: false })
    ).toBeVisible()
    await expect(
      confirm.getByText('DB side (0 paths)', { exact: false })
    ).toBeVisible()
    await confirm.getByRole('button', { name: 'Apply', exact: true }).click()
    await expect(page.getByRole('dialog')).toHaveCount(0)

    // The resolve adopted the live replicas back into the DB config and
    // re-anchored: fresh checks say synced on both the API and the tab.
    await expect
      .poll(async () => (await statusReport(request, driftCluster)).status, {
        timeout: 120_000,
        intervals: [3_000],
      })
      .toBe('synced')
    await reloadUntilChip(page, 'Synced', 120_000)
    await expect(driftDocRow(page)).toHaveCount(0)
  })

  // Scenario 4 — the history dialog lists the chain the loop just wrote:
  // first render, the wizard save-only revision and the drift resolve that
  // is now live on the cluster.
  test('history dialog lists the publish, save-only and resolve records', async ({
    page,
  }) => {
    test.skip(!driftCluster, 'drift fixture was not created')
    await page.goto(detailUrl(driftCluster))
    await page.getByRole('button', { name: '•••' }).click()
    await page.getByRole('menuitem', { name: 'Revisions' }).click()

    const dialog = page.getByRole('dialog')
    await expect(
      dialog.getByRole('heading', {
        name: `Revisions · ${SPACE} / ${APP} · ${driftCluster}`,
      })
    ).toBeVisible()
    await expect(
      dialog.locator('tr').filter({ hasText: 'First render' })
    ).toBeVisible()
    await expect(
      dialog.locator('tr').filter({ hasText: 'Wizard save' })
    ).toBeVisible()
    const resolveRow = dialog.locator('tr').filter({ hasText: 'Drift resolve' })
    await expect(resolveRow).toBeVisible()
    await expect(resolveRow).toContainText('#3')
    await expect(resolveRow).toContainText('live on cluster')
  })

  // Scenario 5 — the config Restart button arms on the first click and
  // disarms itself when the confirm click never comes; the text flip is the
  // assertion, the restart itself is not required to run.
  test('restart button arms on first click and disarms without a second', async ({
    page,
  }) => {
    test.skip(!driftCluster, 'drift fixture was not created')
    await page.goto(detailUrl(driftCluster))
    // The overview bar's config-level button renders before any pod-row
    // Restart buttons, and the title stays constant while arming renames the
    // accessible name to "Confirm restart" — anchoring by title keeps the
    // locator pinned to this one button across the flip.
    const restart = page.getByTitle('Click again to confirm').first()
    await expect(restart).toBeVisible()
    await expect(restart).toHaveText('Restart', { timeout: 30_000 })

    await restart.click()
    await expect(restart).toHaveText('Confirm restart')
    // Still armed shortly before the three-second deadline…
    await page.waitForTimeout(1_500)
    await expect(restart).toHaveText('Confirm restart')
    // …and back to the original label once it passes.
    await page.waitForTimeout(2_000)
    await expect(restart).toHaveText('Restart')
  })

  // Best-effort teardown when an earlier serial test failed mid-chain: take
  // the deployed config down (offline deletes the cluster objects — the
  // delete gate refuses otherwise), remove the application row, then strip
  // the space marker so the fixture namespace leaves the picker.
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
      const list = await context.get('/api/v1/applications')
      if (!list.ok()) return
      const body = (await list.json()) as {
        applications?: Array<{
          name: string
          namespace: string
          configs: Array<{ cluster: string }>
        }>
      }
      const mine = (body.applications ?? []).find(
        (app) => app.name === APP && app.namespace === SPACE
      )
      if (mine) {
        for (const config of mine.configs ?? []) {
          await context
            .post(
              `/api/v1/applications/${SPACE}/${APP}/configs/${encodeURIComponent(config.cluster)}/offline`
            )
            .catch(() => undefined)
        }
        await context
          .delete(`/api/v1/applications/${SPACE}/${APP}`)
          .catch(() => undefined)
      }
      if (driftCluster) {
        await context
          .delete(`/api/v1/applications/spaces/${encodeURIComponent(SPACE)}`, {
            data: { clusters: [driftCluster] },
          })
          .catch(() => undefined)
      }
    } catch {
      // best-effort
    } finally {
      await context?.dispose()
    }
  })
})

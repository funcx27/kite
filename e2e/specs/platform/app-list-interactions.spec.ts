import { readFileSync } from 'node:fs'

import {
  expect,
  test,
  type APIRequestContext,
  type APIResponse,
  type Page,
} from '@playwright/test'

import { authFile } from '../../env'

test.use({ storageState: authFile })

// Application list page interactions (design §11): the cluster filter
// narrowing the status column, toolbar search, the edit-description dialog
// and the delete gate. Every test seeds its own fixtures through the
// application module API into a dedicated space (namespace) and tears them
// down again, so pre-existing applications are never touched. Request shapes
// follow modules/application/internal/api/routes.go and its handlers.

interface ClusterView {
  name: string
  error?: string
}

interface AppResponse {
  id: number
  name: string
  namespace: string
  description: string
  configs: Array<{ cluster: string; statusSummary: string }>
}

async function expectJSON<T>(
  response: APIResponse,
  status: number
): Promise<T> {
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
      image: 'registry.k8s.io/pause:3.10.1',
      replicas: 1,
      limits: { cpu: '100m', memory: '64Mi' },
    },
  }
}

const rowOf = (page: Page, name: string) =>
  page.getByRole('row').filter({ hasText: name })
const chip = (page: Page, name: string) =>
  page.getByRole('button', { name, exact: true })

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

test.describe('platform application list interactions', () => {
  // Scenario 1: the cluster filter narrows the status column to the selected
  // clusters' config rows; an app only deployed elsewhere disappears.
  test('cluster filter narrows the status column and restores it', async ({
    page,
    request,
  }) => {
    const suffix = Date.now().toString(36)
    const space = `e2e-${suffix}-flt`
    const dualApp = `e2e-${suffix}-both`
    const onlyAApp = `e2e-${suffix}-onlya`
    // The cluster this test imported itself; only that one is ever deleted.
    let importedCluster = ''

    let deployable = await deployableClusters(request)
    if (deployable.length < 2) {
      // CI runs against a fresh instance with a single kind cluster. The
      // kubeconfig (KUBECONFIG is exported to the playwright process by the
      // e2e-run make target) is re-imported under a unique context name via
      // POST /admin/clusters/import {config}: clusters are named per context
      // (cluster_manager.go ImportClustersFromKubeconfig), so this yields a
      // genuinely reachable second client.
      try {
        const kubeconfigPath = process.env.KUBECONFIG
        if (!kubeconfigPath) {
          throw new Error('KUBECONFIG is not set')
        }
        const text = readFileSync(kubeconfigPath, 'utf8')
        const field = (key: string) => {
          const match = text.match(new RegExp(`${key}:\\s*(\\S+)`))
          if (!match) throw new Error(`kubeconfig has no ${key}`)
          return match[1]
        }
        const alias = `${space}-c2`
        const config = {
          apiVersion: 'v1',
          kind: 'Config',
          'current-context': alias,
          clusters: [
            {
              name: alias,
              cluster: {
                server: field('server'),
                'certificate-authority-data': field(
                  'certificate-authority-data'
                ),
              },
            },
          ],
          users: [
            {
              name: alias,
              user: {
                'client-certificate-data': field('client-certificate-data'),
                'client-key-data': field('client-key-data'),
              },
            },
          ],
          contexts: [{ name: alias, context: { cluster: alias, user: alias } }],
        }
        const imported = await expectJSON<{ importedCount: number }>(
          await request.post('/api/v1/admin/clusters/import', {
            data: { config: JSON.stringify(config) },
          }),
          201
        )
        expect(imported.importedCount).toBeGreaterThan(0)
        importedCluster = alias
        // The import triggers a cluster sync; poll until the new client is
        // actually deployable.
        await expect
          .poll(async () => (await deployableClusters(request)).length, {
            timeout: 30_000,
          })
          .toBeGreaterThanOrEqual(2)
      } catch (err) {
        // Fall through to the skip decision below; the reason is reported.
        await request
          .post('/api/v1/admin/clusters/import', { data: { inCluster: true } })
          .catch(() => undefined)
        deployable = await deployableClusters(request)
        if (deployable.length < 2) {
          await removeCluster(request, importedCluster)
          importedCluster = ''
          test.skip(
            true,
            `needs two deployable clusters, found ${deployable.length} (${deployable.join(',')}): ${(err as Error).message}`
          )
        }
      }
    }
    const [clusterA, clusterB] = deployable

    // The space lives on both clusters (labeled namespace + managed quota),
    // so the toolbar renders both cluster chips for ?space= deep links.
    const upsert = await expectJSON<{
      succeeded: string[]
      failed: Array<{ cluster: string; error: string }>
    }>(
      await request.put(`/api/v1/applications/spaces/${space}/quotas`, {
        data: {
          quotas: [clusterA, clusterB].map((cluster) => ({
            cluster,
            cpu: '1',
            memory: '1Gi',
          })),
        },
      }),
      200
    )
    expect(upsert.failed).toEqual([])

    const dual = await expectJSON<
      AppResponse & { deploy?: { success: boolean } }
    >(
      await request.post('/api/v1/applications', {
        data: {
          name: dualApp,
          namespace: space,
          description: 'deployed on two clusters',
          spec: appSpec(),
          cluster: clusterA,
        },
      }),
      201
    )
    expect(dual.deploy?.success, 'first publish on create').toBe(true)
    // Second cluster config: create-mode deploy rendered from the draft spec.
    const deployB = await expectJSON<{ success: boolean }>(
      await request.post(
        `/api/v1/applications/${space}/${dualApp}/configs/${encodeURIComponent(clusterB)}/deploy`,
        { data: {} }
      ),
      200
    )
    expect(deployB.success, 'deploy to the second cluster').toBe(true)

    const onlyA = await expectJSON<
      AppResponse & { deploy?: { success: boolean } }
    >(
      await request.post('/api/v1/applications', {
        data: {
          name: onlyAApp,
          namespace: space,
          description: 'deployed on the first cluster only',
          spec: appSpec(),
          cluster: clusterA,
        },
      }),
      201
    )
    expect(onlyA.deploy?.success, 'publish on create').toBe(true)

    try {
      await page.goto(`/portal/applications?space=${space}`)
      const dualRow = rowOf(page, dualApp)
      const onlyARow = rowOf(page, onlyAApp)
      await expect(dualRow).toBeVisible()
      await expect(onlyARow).toBeVisible()

      // Unfiltered: one status line per cluster config.
      await expect(dualRow.getByText(clusterA, { exact: true })).toBeVisible()
      await expect(dualRow.getByText(clusterB, { exact: true })).toBeVisible()

      // Single selection narrows the status column to that cluster's line.
      await chip(page, clusterA).click()
      await expect(dualRow.getByText(clusterA, { exact: true })).toBeVisible()
      await expect(dualRow.getByText(clusterB, { exact: true })).toHaveCount(0)
      // The app deployed on the selected cluster stays.
      await expect(onlyARow).toBeVisible()

      // Deselect (no filter): both lines come back.
      await chip(page, clusterA).click()
      await expect(dualRow.getByText(clusterA, { exact: true })).toBeVisible()
      await expect(dualRow.getByText(clusterB, { exact: true })).toBeVisible()

      // Multi-select both clusters: still the full set.
      await chip(page, clusterA).click()
      await chip(page, clusterB).click()
      await expect(dualRow.getByText(clusterA, { exact: true })).toBeVisible()
      await expect(dualRow.getByText(clusterB, { exact: true })).toBeVisible()
      await expect(onlyARow).toBeVisible()

      // Only the other cluster: the A-only app's row disappears entirely,
      // and the dual app's column narrows to B.
      await chip(page, clusterA).click()
      await expect(dualRow.getByText(clusterB, { exact: true })).toBeVisible()
      await expect(dualRow.getByText(clusterA, { exact: true })).toHaveCount(0)
      await expect(onlyARow).toHaveCount(0)

      // Deselect B: filter fully cleared, everything restored.
      await chip(page, clusterB).click()
      await expect(onlyARow).toBeVisible()
      await expect(dualRow.getByText(clusterA, { exact: true })).toBeVisible()
      await expect(dualRow.getByText(clusterB, { exact: true })).toBeVisible()
    } finally {
      await cleanupApps(request, space, [
        { name: dualApp, clusters: [clusterA, clusterB] },
        { name: onlyAApp, clusters: [clusterA] },
      ])
      await dissociateSpace(request, space, [clusterA, clusterB])
      await removeCluster(request, importedCluster)
    }
  })

  // Scenario 2: the toolbar search filters by name and by description and
  // clearing it restores every row.
  test('search filters by name and description and clears', async ({
    page,
    request,
  }) => {
    const suffix = Date.now().toString(36)
    const space = `e2e-${suffix}-srch`
    const nameApp = `e2e-${suffix}-alpha`
    const descApp = `e2e-${suffix}-bravo`
    const descToken = `bravodoc${suffix}`

    const clusterA = (await deployableClusters(request))[0]
    test.skip(
      !clusterA,
      'search fixtures need one deployable cluster to publish an app'
    )
    await seedSpace(request, space, [clusterA])
    await createApp(request, {
      name: nameApp,
      namespace: space,
      description: `alpha fixture ${suffix}`,
      spec: appSpec(),
      cluster: clusterA,
    })
    await createApp(request, {
      name: descApp,
      namespace: space,
      description: `${descToken} fixture`,
      spec: appSpec(),
      cluster: clusterA,
    })

    try {
      await page.goto(`/portal/applications?space=${space}`)
      const alphaRow = rowOf(page, nameApp)
      const bravoRow = rowOf(page, descApp)
      const searchBox = page.getByLabel('Search applications...')
      await expect(alphaRow).toBeVisible()
      await expect(bravoRow).toBeVisible()

      // By name: only the matching row stays.
      await searchBox.fill(nameApp)
      await expect(alphaRow).toBeVisible()
      await expect(bravoRow).toHaveCount(0)

      // By description: the token only appears in bravo's description.
      await searchBox.fill(descToken)
      await expect(bravoRow).toBeVisible()
      await expect(alphaRow).toHaveCount(0)

      // Clearing restores every row.
      await searchBox.fill('')
      await expect(alphaRow).toBeVisible()
      await expect(bravoRow).toBeVisible()
    } finally {
      await cleanupApps(request, space, [
        { name: nameApp, clusters: [clusterA] },
        { name: descApp, clusters: [clusterA] },
      ])
      await dissociateSpace(request, space, [clusterA])
    }
  })

  // Scenario 3: the edit-description dialog saves and the table shows the
  // new description.
  test('edit description dialog updates the table', async ({
    page,
    request,
  }) => {
    const suffix = Date.now().toString(36)
    const space = `e2e-${suffix}-desc`
    const app = `e2e-${suffix}-draft`

    const clusterA = (await deployableClusters(request))[0]
    test.skip(
      !clusterA,
      'description fixtures need one deployable cluster to resolve a space view'
    )
    await seedSpace(request, space, [clusterA])
    await createApp(request, {
      name: app,
      namespace: space,
      description: `before edit ${suffix}`,
      spec: appSpec(),
    })

    try {
      await page.goto(`/portal/applications?space=${space}`)
      const appRow = rowOf(page, app)
      await expect(appRow).toBeVisible()

      await appRow.getByRole('button', { name: 'Edit Description' }).click()
      const dialog = page.getByRole('dialog')
      await expect(
        dialog.getByRole('heading', { name: `Edit description of ${app}` })
      ).toBeVisible()
      const updated = `edited description ${suffix}`
      await dialog.locator('input').fill(updated)
      await dialog.getByRole('button', { name: 'Confirm', exact: true }).click()
      await expect(page.getByRole('dialog')).toHaveCount(0)
      await expect(appRow.getByText(updated)).toBeVisible()
    } finally {
      await cleanupApps(request, space, [{ name: app, clusters: [] }])
      await dissociateSpace(request, space, [clusterA])
    }
  })

  // Scenario 4: the delete gate — a draft (no cluster config) deletes end to
  // end, a deployed app's delete button is disabled.
  test('delete gate: draft deletable, deployed locked', async ({
    page,
    request,
  }) => {
    const suffix = Date.now().toString(36)
    const space = `e2e-${suffix}-gate`
    const draftApp = `e2e-${suffix}-draft`
    const deployedApp = `e2e-${suffix}-live`

    const clusterA = (await deployableClusters(request))[0]
    test.skip(
      !clusterA,
      'delete gate needs one deployable cluster for the deployed fixture'
    )
    await seedSpace(request, space, [clusterA])
    await createApp(request, {
      name: deployedApp,
      namespace: space,
      description: `deployed fixture ${suffix}`,
      spec: appSpec(),
      cluster: clusterA,
    })
    const draft = await createApp(request, {
      name: draftApp,
      namespace: space,
      description: `draft fixture ${suffix}`,
      spec: appSpec(),
    })
    expect(draft.configs).toEqual([])

    try {
      await page.goto(`/portal/applications?space=${space}`)
      const draftRow = rowOf(page, draftApp)
      const deployedRow = rowOf(page, deployedApp)
      await expect(draftRow).toBeVisible()
      await expect(deployedRow).toBeVisible()

      // Deployed: the row action is disabled outright.
      await expect(
        deployedRow.getByRole('button', { name: 'Delete', exact: true })
      ).toBeDisabled()

      // Draft: delete runs through the confirmation dialog.
      await draftRow.getByRole('button', { name: 'Delete', exact: true }).click()
      const dialog = page.getByRole('dialog')
      await expect(
        dialog.getByRole('heading', { name: `Delete application ${draftApp}?` })
      ).toBeVisible()
      await dialog.getByRole('button', { name: 'Delete', exact: true }).click()
      await expect(draftRow).toHaveCount(0)
      await expect(deployedRow).toBeVisible()
      await expect
        .poll(async () =>
          (await request.get(`/api/v1/applications/${space}/${draftApp}`)).status()
        )
        .toBe(404)
    } finally {
      await cleanupApps(request, space, [
        { name: deployedApp, clusters: [clusterA] },
        { name: draftApp, clusters: [] },
      ])
      await dissociateSpace(request, space, [clusterA])
    }
  })
})

// ── fixture helpers (shapes from modules/application/internal/api) ──

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

async function createApp(
  request: APIRequestContext,
  body: {
    name: string
    namespace: string
    description: string
    spec: ReturnType<typeof appSpec>
    cluster?: string
  }
) {
  // With a cluster the create chain also first-publishes; per-document
  // failures ride the response instead of the status code.
  const app = await expectJSON<AppResponse & { deploy?: { success: boolean } }>(
    await request.post('/api/v1/applications', { data: body }),
    201
  )
  if (body.cluster) {
    expect(app.deploy?.success, `${body.name} first publish`).toBe(true)
  }
  return app
}

// Offline every config first (the delete gate refuses deployed apps and the
// offline deletes the cluster objects), then delete the application rows.
// Every step is best effort so a mid-test failure still tears down what
// exists.
async function cleanupApps(
  request: APIRequestContext,
  space: string,
  apps: Array<{ name: string; clusters: string[] }>
) {
  for (const app of apps) {
    for (const cluster of app.clusters) {
      await request
        .post(
          `/api/v1/applications/${encodeURIComponent(space)}/${encodeURIComponent(app.name)}/configs/${encodeURIComponent(cluster)}/offline`
        )
        .catch(() => undefined)
    }
    await request
      .delete(
        `/api/v1/applications/${encodeURIComponent(space)}/${encodeURIComponent(app.name)}`
      )
      .catch(() => undefined)
  }
}

// Strip the space marker from the fixture namespaces; the namespaces and any
// remaining objects stay, the space just leaves the picker.
async function dissociateSpace(
  request: APIRequestContext,
  space: string,
  clusters: string[]
) {
  await request
    .delete(`/api/v1/applications/spaces/${encodeURIComponent(space)}`, {
      data: { clusters },
    })
    .catch(() => undefined)
}

// Delete a cluster this spec imported itself, looked up by name; a missing
// name is a no-op.
async function removeCluster(request: APIRequestContext, name: string) {
  if (!name) return
  const response = await request
    .get('/api/v1/admin/clusters/')
    .catch(() => undefined)
  if (!response?.ok()) return
  const rows = (await response.json().catch(() => [])) as Array<{
    id: number
    name: string
  }>
  const row = rows.find((cluster) => cluster.name === name)
  if (row) {
    await request.delete(`/api/v1/admin/clusters/${row.id}`).catch(() => undefined)
  }
}

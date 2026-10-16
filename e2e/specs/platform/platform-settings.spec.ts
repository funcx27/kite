import { expect, test, type APIResponse, type Page } from '@playwright/test'

import { authFile } from '../../env'

test.use({ storageState: authFile })

// Application-mode settings page (/portal/settings, pages/
// application-settings-page.tsx): the global view renders the credentials /
// certificates / build providers / build runtime / kcross cards, a ?space=
// deep link swaps in the space's quota + build settings with a "View global"
// reset, git credentials round-trip through the shared create dialog
// (workflow/components/credentials-management.tsx), and the kcross card
// (kcross/kcross-management.tsx) renders its gateway form and the peers
// table's empty state. Every fixture is self-created and torn down; the
// kcross gateway setting is global persistent state and is restored through
// PUT /admin/kcross/setting (pkg/kcross/handler.go HandleUpdateSetting).

interface ClusterView {
  name: string
  error?: string
}

// GET /admin/kcross/setting (pkg/kcross/handler.go HandleGetSetting); the
// token never round-trips, only adminTokenConfigured does.
interface KcrossSetting {
  enabled: boolean
  gateways: string[] | null
  adminTokenConfigured: boolean
}

async function expectJSON<T>(response: APIResponse, status: number): Promise<T> {
  const text = await response.text()
  expect(response.status(), text).toBe(status)
  return JSON.parse(text) as T
}

// Cards are shadcn Cards (data-slot="card", components/ui/card.tsx); the
// CardTitle carries data-slot="card-title".
const cardByTitle = (page: Page, title: string) =>
  page.locator('[data-slot="card"]').filter({
    has: page.locator('[data-slot="card-title"]', { hasText: title }),
  })

const headerPicker = (page: Page) =>
  page.locator('header').getByRole('button', { name: /Spaces/ })

// A cluster the quota upsert can actually create the labeled namespace on:
// a bare GET /clusters also lists registered-but-unreachable ones, so probe
// the overview like specs/platform/app-list-interactions.spec.ts does.
async function reachableCluster(page: Page): Promise<string | undefined> {
  const clusters = await expectJSON<ClusterView[]>(
    await page.request.get('/api/v1/clusters'),
    200
  )
  for (const name of clusters.map((cluster) => cluster.name).sort()) {
    const overview = await page.request
      .get(`/api/v1/_clusters/${encodeURIComponent(name)}/overview`)
      .catch(() => undefined)
    if (overview?.ok()) return name
  }
  return undefined
}

async function seedSpace(
  page: Page,
  space: string,
  cluster: string
): Promise<void> {
  const upsert = await expectJSON<{
    succeeded: string[]
    failed: Array<{ cluster: string; error: string }>
  }>(
    await page.request.put(`/api/v1/applications/spaces/${space}/quotas`, {
      data: {
        quotas: [{ cluster, cpu: '1', memory: '1Gi' }],
      },
    }),
    200
  )
  expect(upsert.failed).toEqual([])
}

// The space marker leaves the picker; the namespace itself stays.
async function dissociateSpace(page: Page, space: string, cluster: string) {
  await page.request
    .delete(`/api/v1/applications/spaces/${encodeURIComponent(space)}`, {
      data: { clusters: [cluster] },
    })
    .catch(() => undefined)
}

test.describe('platform settings page', () => {
  // Scenario 1: the global view (fresh context: no stored space, no ?space=)
  // renders every management card. The page branch renders five —
  // CredentialsManagement, CertificatesManagement, BuildProvidersManagement,
  // BuildRuntimeSettings and KcrossManagement
  // (application-settings-page.tsx:65-71).
  test('global view renders the management sections', async ({ page }) => {
    await page.goto('/portal/settings')
    await expect(new URL(page.url()).searchParams.get('space') ?? null).toBe(
      null
    )

    for (const title of [
      'Credentials',
      'SSL Certificates',
      'Build Providers',
      'Build Runtime',
      'kcross Gateway',
    ]) {
      await expect(cardByTitle(page, title)).toBeVisible()
    }
    // No space sections bleed into the global view.
    await expect(page.getByText('Resource Quotas ·')).toHaveCount(0)

    // The cards are live, not just titled: the credentials card carries the
    // Git/Registry tabs and create entry (credentials-management.tsx), the
    // kcross card its gateway form fields (kcross-management.tsx
    // GatewaySettingForm).
    const credentials = cardByTitle(page, 'Credentials')
    await expect(credentials.getByRole('tab', { name: 'Git' })).toBeVisible()
    await expect(credentials.getByRole('tab', { name: 'Registry' })).toBeVisible()
    await expect(credentials.getByTitle('Create Credential')).toBeVisible()
    const kcross = cardByTitle(page, 'kcross Gateway')
    await expect(kcross.locator('#kcross-gateways')).toBeVisible()
    await expect(kcross.locator('#kcross-admin-token')).toBeVisible()
    await expect(
      kcross.getByRole('button', { name: 'Test Connection' })
    ).toBeVisible()
  })

  // Scenario 2: a ?space= deep link swaps the five global cards for the
  // space's quota + build settings; "View global settings" clears the active
  // space itself (application-settings-page.tsx setCurrentSpace(null)), so
  // the ?space= param leaves the URL and the header picker returns to
  // "All Spaces" (space-context.tsx / space-picker.tsx).
  test('space view sections and View global reset the header picker', async ({
    page,
  }) => {
    const suffix = Date.now().toString(36)
    const space = `e2e-${suffix}-set`
    const cluster = await reachableCluster(page)
    test.skip(
      !cluster,
      'space fixtures need one reachable cluster to seed the space quota'
    )
    await seedSpace(page, space, cluster!)

    try {
      await page.goto(`/portal/settings?space=${space}`)
      await expect(
        cardByTitle(page, `Resource Quotas · ${space}`)
      ).toBeVisible()
      await expect(
        cardByTitle(page, `Space Settings · ${space}`)
      ).toBeVisible()
      const picker = headerPicker(page)
      await expect(picker).toContainText(space)
      await expect(picker).not.toContainText('All Spaces')

      await page
        .getByRole('button', { name: 'View global settings' })
        .click()
      // URL semantics: the space param is gone, the path is the bare one.
      await expect
        .poll(() => new URL(page.url()).searchParams.get('space'))
        .toBe(null)
      await expect(page).toHaveURL(/\/portal\/settings$/)
      // The header picker follows the cleared space instead of only this page.
      await expect(picker).toContainText('All Spaces')
      // And the global sections are back.
      await expect(cardByTitle(page, 'Credentials')).toBeVisible()
    } finally {
      await dissociateSpace(page, space, cluster!)
    }
  })

  // Scenario 3: a git credential round-trips through the shared create
  // dialog and the type-to-confirm delete gate. The git tab is the default,
  // so the card-header plus button opens the dialog with the git fields
  // (credentials-management.tsx openCreate + payloadOf). The list row is the
  // durable result; the toasts only pin the mutation outcomes.
  test('git credential round-trips through create and delete', async ({
    page,
  }) => {
    const suffix = Date.now().toString(36)
    const name = `e2e-${suffix}-cred`
    const host = `git.e2e-${suffix}.invalid`

    await page.goto('/portal/settings')
    const credentials = cardByTitle(page, 'Credentials')
    await expect(credentials).toBeVisible()

    try {
      await credentials.getByTitle('Create Credential').click()
      const dialog = page.getByRole('dialog')
      await expect(dialog.getByText('Create Credential')).toBeVisible()
      // Field order per credentials-management.tsx: name, description, then
      // the git trio (host, username, password).
      const inputs = dialog.locator('input')
      await inputs.nth(0).fill(name)
      await inputs.nth(1).fill(`e2e git fixture ${suffix}`)
      await inputs.nth(2).fill(host)
      await inputs.nth(3).fill('e2e-bot')
      await dialog.locator('input[type="password"]').fill(`secret-${suffix}`)
      await dialog.getByRole('button', { name: 'Create', exact: true }).click()
      await expect(page.getByText('Credential created')).toBeVisible()

      const row = page.getByRole('row').filter({ hasText: name })
      await expect(row).toBeVisible()
      // The shape column mirrors the stored host back.
      await expect(row.getByText(host)).toBeVisible()

      // Delete runs through the type-to-confirm gate
      // (components/delete-confirmation-dialog.tsx).
      await row.getByTitle('Delete', { exact: true }).click()
      const confirm = page.getByRole('dialog')
      await expect(confirm.getByText('Delete credential')).toBeVisible()
      await confirm.locator('#confirmation').fill(name)
      await confirm
        .getByRole('button', { name: 'Delete', exact: true })
        .click()
      await expect(page.getByText('Credential deleted')).toBeVisible()
      await expect(row).toHaveCount(0)
    } finally {
      // Best-effort: a mid-test failure must not leave the fixture behind
      // (modules/credentials/internal/api/routes.go DELETE /credentials/:name).
      await page.request
        .delete(`/api/v1/credentials/${encodeURIComponent(name)}`)
        .catch(() => undefined)
    }
  })

  // Scenario 4: the kcross card renders the gateway form and the peers
  // section without peers. The terminal button lives inside the peer detail
  // dialog and only for an online peer (kcross-management.tsx:466), and that
  // dialog only opens from a peer row — with no peers it is hidden, never
  // merely disabled. No real peer is fabricated: an unreachable gateway
  // (127.0.0.1:9) guarantees an empty peer list.
  test('kcross gateway form and empty peers table without a terminal entry', async ({
    page,
  }) => {
    const original = await expectJSON<KcrossSetting>(
      await page.request.get('/api/v1/admin/kcross/setting'),
      200
    )
    const terminalButton = page.getByTitle('Open Terminal')
    // Whether this test flipped the global gateway setting; the finally
    // restores the pre-test value even on a mid-test failure.
    let mutated = false
    const restore = () =>
      page.request.put('/api/v1/admin/kcross/setting', {
        data: {
          enabled: original.enabled,
          gateways: (original.gateways ?? []).join(','),
        },
      })

    try {
      await page.goto('/portal/settings')
      const gateway = cardByTitle(page, 'kcross Gateway')
      await expect(gateway).toBeVisible()
      const peers = cardByTitle(page, 'Discovered Peers')
      await expect(peers).toBeVisible()
      await expect(terminalButton).toHaveCount(0)

      // The gateway starts disabled on a fresh instance (model default
      // false). Enable it against a dead gateway to drive the peers table
      // into its empty state, then restore.
      if (!original.enabled) {
        await expect(
          peers.getByText(
            'Configure and enable the gateway first to discover peers.'
          )
        ).toBeVisible()
        await expect(peers.getByRole('table')).toHaveCount(0)

        await gateway.getByRole('switch').click()
        await gateway.locator('#kcross-gateways').fill('http://127.0.0.1:9')
        await gateway
          .locator('#kcross-admin-token')
          .fill(`e2e-token-${Date.now().toString(36)}`)
        await gateway
          .getByRole('button', { name: 'Save', exact: true })
          .click()
        mutated = true
        await expect(page.getByText('Settings saved')).toBeVisible()

        // The peers section keys off the fetched setting, not the form
        // state, so remount to refetch it.
        await page.reload()
        await expect(
          peers.getByPlaceholder('Search peer / capability / cluster')
        ).toBeVisible()
        // Empty state, no crash: the header row only, zero peers.
        await expect(
          peers.getByRole('columnheader', { name: 'Peer' })
        ).toBeVisible()
        await expect(peers.getByRole('row')).toHaveCount(1)
        await expect(terminalButton).toHaveCount(0)

        // Restore the pre-test setting and watch the card fall back.
        await restore()
        mutated = false
        await page.reload()
        await expect(
          peers.getByText(
            'Configure and enable the gateway first to discover peers.'
          )
        ).toBeVisible()
        await expect(terminalButton).toHaveCount(0)
      } else {
        // Already-enabled instance: the peers UI renders as-is; nothing is
        // mutated, so no restore is needed.
        await expect(
          peers.getByRole('columnheader', { name: 'Peer' })
        ).toBeVisible()
        await expect(terminalButton).toHaveCount(0)
      }
    } finally {
      if (mutated) {
        await restore().catch(() => undefined)
      }
    }
  })
})

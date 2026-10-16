import {
  expect,
  request as playwrightRequest,
  test,
  type APIRequestContext,
  type Page,
} from '@playwright/test'

import { authFile } from '../../env'

test.use({ storageState: authFile })

// Full creation journey regression (the first half of demo-flow.mjs, turned
// into a real test): create a uniquely named space through the header picker,
// walk the six-pane creation wizard, Create & Publish against a real cluster
// (dynamic first dropdown item: dev1/dev2 locally, the kind cluster in CI),
// land on the detail page and wait for the green Synced chip. The offline and
// delete tests double as the fixture teardown — the offline call is what
// re-enables deletion, mirroring the backend's delete gate.

// Unique per-run fixtures; nothing pre-existing is touched.
const STAMP = Date.now().toString(36)
const SPACE = `e2e-j-${STAMP}`
const APP = `e2e-app-${STAMP}`
const IMAGE = 'nginx:alpine'
const APP_CPU_LIMIT = '0.2'
const APP_MEMORY_LIMIT_M = '256'
const SPACE_CPU = '1'
const SPACE_MEMORY_G = '2'

const listUrl = (space: string) =>
  `/portal/applications?space=${encodeURIComponent(space)}`
const detailUrl = (space: string, name: string) =>
  `/portal/applications/${encodeURIComponent(space)}/${encodeURIComponent(name)}`

// The detail page's status chip (status-badge.tsx ApplicationStatusChip):
// green styling rides the same classes as the "Synced" label (lib.ts
// applicationStatusStyles.synced.chip), so the text pins the color.
const statusChip = (page: Page, label: string) =>
  page.locator('span.rounded-full').filter({ hasText: label }).first()

// Space rows are plain buttons inside the header picker's popover
// (space-picker.tsx SpacePicker, mounted in the portal <header>).
async function openSpacePicker(page: Page) {
  await page.locator('header').getByRole('button', { name: /Spaces/ }).click()
}

// The status query runs the server-side drift check on every mount
// (status_api.go GetStatus persists StatusSummary), but react-query never
// refetches it on its own — each page.reload() remounts the cluster tab and
// re-runs the check, which is what the list page's delete gate reads too.
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

// Best-effort safety net: when an earlier serial test failed, the UI cleanup
// tests were skipped — take the fixtures down through the API so nothing
// lingers. The offline call is required first: the backend delete gate blocks
// applications that still carry a deployed config.
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
    if (!mine) return
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
  } catch {
    // best-effort
  } finally {
    await context?.dispose()
  }
})

test.describe.serial('application wizard journey', () => {
  // Resolved live from the wizard's target-cluster dropdown (its first
  // option), never hardcoded: local runs have dev1/dev2, CI has one kind
  // cluster under another name.
  let targetCluster = ''

  test('new space, wizard steps, create & publish lands on the detail page with a green chip', async ({
    page,
  }) => {
    test.setTimeout(300_000)
    await page.goto(listUrl(SPACE))

    // ── Space: header picker → New Space → small cpu/memory quota ──
    await openSpacePicker(page)
    await page.getByRole('button', { name: 'New Space' }).click()
    const spaceDialog = page.getByRole('dialog')
    await spaceDialog.getByPlaceholder(/Space name/).fill(SPACE)
    // One cluster is preselected; its quota renders two number inputs
    // (cpu cores / memory GiB) — space-quota-settings.tsx ClusterQuotaFields.
    const quotaInputs = spaceDialog.locator('input[type="number"]')
    await expect(quotaInputs).toHaveCount(2)
    await quotaInputs.nth(0).fill(SPACE_CPU)
    await quotaInputs.nth(1).fill(SPACE_MEMORY_G)
    await spaceDialog.getByRole('button', { name: 'Create', exact: true }).click()
    // Success toast names the space and the cluster it was created on.
    await page.getByText(`Space ${SPACE} created on`).waitFor()

    // ── Switch to the new space (?space= lands in the URL; the wizard binds
    //    creation to this current space) ──
    await openSpacePicker(page)
    const spaceRow = page.getByRole('button', { name: SPACE, exact: true })
    await expect(spaceRow).toBeVisible({ timeout: 15_000 })
    await spaceRow.click()
    await page.waitForURL((url) => url.searchParams.get('space') === SPACE)

    // ── Wizard: basic info → startup → config files → network →
    //    target cluster → preview ──
    await page.getByRole('button', { name: /Create Application/ }).click()
    const wiz = page.getByRole('dialog')
    await expect(wiz).toBeVisible()
    await wiz.getByPlaceholder('my-app').fill(APP)
    await wiz.locator('#container-image').fill(IMAGE)
    await wiz.getByLabel('CPU Limit', { exact: true }).fill(APP_CPU_LIMIT)
    await wiz
      .getByLabel('Memory Limit', { exact: true })
      .fill(APP_MEMORY_LIMIT_M)
    const next = wiz.getByRole('button', { name: 'Next', exact: true })

    await next.click() // → startup
    await expect(wiz.getByText('Environment Variables')).toBeVisible()
    await next.click() // → config files
    await expect(
      wiz.getByText('No config files needed? Just continue to the next step.')
    ).toBeVisible()
    await next.click() // → network
    await expect(wiz.getByText(/Port names are auto-generated/)).toBeVisible()
    await next.click() // → target cluster
    await expect(wiz.getByText('Target cluster', { exact: true })).toBeVisible()

    // Publish target = the dropdown's first item, whatever the cluster is
    // named in this environment. The only select rendered on this step is
    // the target cluster's.
    await wiz.locator('[data-slot="select-trigger"]').click()
    const firstOption = page.getByRole('option').first()
    const optionText = (await firstOption.textContent()) ?? ''
    // Item text is "<name> · <version>" (application-wizard.tsx step 5).
    targetCluster = optionText.split('·')[0].trim()
    expect(targetCluster, 'target cluster dropdown has a first item').not.toBe(
      ''
    )
    await firstOption.click()
    await next.click() // → preview

    const publish = wiz.getByRole('button', { name: 'Create & Publish' })
    await expect(publish).toBeVisible()
    await publish.click()

    // ── Landed on the app detail page; once the app query returns the
    //    config the page replaces the URL with the cluster tab route. Wait
    //    for either shape first — on a fast cluster both navigations can
    //    land before the first wait is evaluated. ──
    const detailPath = `/portal/applications/${SPACE}/${APP}`
    await page.waitForURL(
      (url) =>
        url.pathname === detailPath ||
        url.pathname === `${detailPath}/clusters/${targetCluster}`
    )
    await page.waitForURL(
      (url) => url.pathname === `${detailPath}/clusters/${targetCluster}`
    )

    // ── Status chip turns green (Synced); generous budget for the drift
    //    check on a real cluster ──
    await reloadUntilChip(page, 'Synced', 120_000)
    await expect(statusChip(page, 'Synced')).toHaveClass(/green/)

    // ── The target cluster shows up as the detail page's cluster tab ──
    await expect(
      page.getByRole('tab', { name: targetCluster, exact: true })
    ).toBeVisible()

    // ── List page: the delete button is disabled — the app carries a
    //    deployed config (the backend's other delete-gate half; the draft
    //    half is covered by app-list-interactions.spec.ts) ──
    await page.goto(listUrl(SPACE))
    const appRow = page.getByRole('row').filter({ hasText: APP })
    await appRow.waitFor()
    await expect(
      appRow.getByRole('button', { name: 'Delete', exact: true })
    ).toBeDisabled()
  })

  test('offline from the detail page takes the deployed config down', async ({
    page,
  }) => {
    await page.goto(detailUrl(SPACE, APP))
    await page.waitForURL(/\/clusters\//)
    const cluster = decodeURIComponent(page.url().split('/clusters/')[1])
    expect(cluster).not.toBe('')

    await page.getByRole('button', { name: 'Offline', exact: true }).click()
    const dialog = page.getByRole('dialog')
    await expect(
      dialog.getByRole('button', { name: 'Confirm Offline & Delete Config' })
    ).toBeVisible()
    await dialog
      .getByRole('button', { name: 'Confirm Offline & Delete Config' })
      .click()

    // The only config is gone: the detail page falls back to the undeployed
    // empty state instead of a cluster tab.
    await expect(
      page.getByText('Not bound to any cluster yet')
    ).toBeVisible()
    await expect(statusChip(page, 'Undeployed')).toBeVisible()
    await expect(
      page.getByRole('tab', { name: cluster, exact: true })
    ).toHaveCount(0)
  })

  test('delete from the list is enabled again after offline and removes the app', async ({
    page,
  }) => {
    await page.goto(listUrl(SPACE))
    const appRow = page.getByRole('row').filter({ hasText: APP })
    await appRow.waitFor()
    const deleteButton = appRow.getByRole('button', {
      name: 'Delete',
      exact: true,
    })
    // Flipped relative to the journey test: no deployed config left.
    await expect(deleteButton).toBeEnabled()
    await deleteButton.click()

    const dialog = page.getByRole('dialog')
    await expect(dialog).toContainText(`Delete application ${APP}?`)
    await dialog.getByRole('button', { name: 'Delete', exact: true }).click()

    await expect(appRow).toHaveCount(0)
  })
})

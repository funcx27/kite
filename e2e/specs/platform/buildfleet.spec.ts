import { expect, test, type Page } from '@playwright/test'

import { authFile } from '../../env'

test.use({ storageState: authFile })

// BuildFleet portal page regression (/portal/buildfleet, ui/src/portal/
// buildfleet/page.tsx): the build form, the fleet-status section and one real
// build submission driven through the form.

// No fixtures are created: the build request below uses an RFC 2606 reserved
// `.invalid` hostname unique per run, so there is nothing to tear down. The
// only persisted trace of a submission is one audit ResourceHistory row
// (adapter.go recordHistory) — the same class of audit row every API action
// writes.

// Build cost (scenario 4 of the task): the submitted build intentionally
// cannot reach a clone. The fleet materializes the git context in-process
// (fleet.go prepareContext → materializeGit → gitctx.Fetch) BEFORE the
// runtime acquire loop runs, so the DNS failure of a `.invalid` host ends the
// build with zero cluster cost — no buildkitd pod provisioned, no image
// pulled, no registry push (verified live: the SSE stream answers
// `event: error` in under 100 ms). A build that could actually succeed would
// provision a buildkitd pod and pull the buildkit image on a shared cluster,
// and the fresh CI instance has no buildfleet config section at all (scripts/
// e2e-start-app.sh ships none) — so the spec pins the terminal-error
// rendering, which both environments exercise through the same UI code path.

// Card titles render as div[data-slot="card-title"] inside
// div[data-slot="card"] (ui/src/components/ui/card.tsx).
const card = (page: Page, title: string) =>
  page.locator('[data-slot="card"]').filter({ hasText: title })

test.describe('platform buildfleet page', () => {
  test('build form renders its required controls and gates the submit button', async ({
    page,
  }) => {
    await page.goto('/portal/buildfleet')
    await expect(page).toHaveTitle('Image Builds - Kite')
    await expect(page.getByRole('heading', { name: 'Image Builds' })).toBeVisible()

    const formCard = card(page, 'New Build')
    await expect(formCard).toBeVisible()
    await expect(page.getByLabel('Context Source')).toBeVisible()
    await expect(page.getByLabel('URL', { exact: true })).toBeVisible()
    await expect(page.getByLabel('Git ref')).toBeVisible()
    await expect(page.getByLabel('Subdirectory')).toBeVisible()
    await expect(page.getByLabel('Dockerfile content')).toBeVisible()
    await expect(page.getByLabel('Platforms')).toBeVisible()
    await expect(page.getByLabel('Output tag')).toBeVisible()

    // The submit gate: empty source URL keeps Start Build disabled, filling
    // it enables the button (build-form.tsx canSubmit).
    const start = page.getByRole('button', { name: 'Start Build' })
    await expect(start).toBeDisabled()
    await page
      .getByLabel('URL', { exact: true })
      .fill('https://e2e-bf-form.invalid/repo.git')
    await expect(start).toBeEnabled()

    // Switching the context source re-renders the git-only fields away and
    // back (build-form.tsx sourceType branch).
    await page.getByLabel('Context Source').click()
    await page.getByRole('option', { name: 'HTTP URL' }).click()
    await expect(page.getByLabel('Git ref')).toHaveCount(0)
    await expect(page.getByLabel('Subdirectory')).toHaveCount(0)
    await page.getByLabel('Context Source').click()
    await page.getByRole('option', { name: 'Git repository' }).click()
    await expect(page.getByLabel('Git ref')).toBeVisible()
    await expect(page.getByLabel('Subdirectory')).toBeVisible()
  })

  test('fleet status section renders its empty state and survives a refresh', async ({
    page,
  }) => {
    await page.goto('/portal/buildfleet')
    await expect(page.getByRole('heading', { name: 'Fleet Status' })).toBeVisible()

    // Pool: empty on both environments (local fresh pool; CI's disabled
    // module 404s the status query and still renders the empty text). A warm
    // runtime from a concurrent user renders the capacity table instead —
    // either rendering is the section working.
    const poolCard = card(page, 'Runtime Pool')
    await expect(poolCard).toBeVisible()
    await expect(
      poolCard.getByText('No data').or(poolCard.getByRole('table'))
    ).toBeVisible()

    // Cache: local answers 501 "no cache registry configured"; a disabled
    // module errors the query and falls through to "No data"; a configured
    // registry renders the project table.
    const cacheCard = card(page, 'Cache Registry')
    await expect(cacheCard).toBeVisible()
    await expect(
      cacheCard
        .getByText('No cache registry configured')
        .or(cacheCard.getByText('No data'))
        .or(cacheCard.getByRole('table'))
    ).toBeVisible()

    // Running builds: no build of ours is in flight, the task list is empty.
    const tasksCard = card(page, 'Running Builds')
    await expect(tasksCard).toBeVisible()
    await expect(
      tasksCard.getByText('No running builds').or(tasksCard.getByRole('table'))
    ).toBeVisible()

    // Refresh re-queries both endpoints without breaking the section.
    await page.getByRole('button', { name: 'Refresh' }).click()
    await expect(page.getByRole('heading', { name: 'Fleet Status' })).toBeVisible()
    await expect(tasksCard).toBeVisible()
  })

  test('submitting a build renders its terminal outcome card without crashing', async ({
    page,
  }) => {
    await page.goto('/portal/buildfleet')
    await expect(page.getByRole('heading', { name: 'Image Builds' })).toBeVisible()

    // Minimal parameters: source URL only, everything else left empty.
    await page
      .getByLabel('URL', { exact: true })
      .fill(`https://e2e-bf-${Date.now().toString(36)}.invalid/context.git`)
    const start = page.getByRole('button', { name: 'Start Build' })
    await expect(start).toBeEnabled()

    const buildsPost = page.waitForResponse(
      (resp) =>
        resp.request().method() === 'POST' &&
        resp.url().includes('/api/v1/buildfleet/builds')
    )
    await start.click()
    const response = await buildsPost

    // Either way the terminal state is a visible failure card holding the
    // error text (page.tsx error state → build-result-card.tsx).
    const failedCard = card(page, 'Build Failed')
    await expect(failedCard).toBeVisible()
    if (response.status() === 404) {
      // Buildfleet module disabled (fresh CI instance): startBuild's fetch
      // rejects before any SSE stream and the thrown message —
      // "HTTP error! status: 404" (buildfleet/api.ts startBuild) — must be
      // rendered verbatim in the failure card.
      await expect(failedCard.locator('pre')).toHaveText(
        'HTTP error! status: 404'
      )
    } else {
      // Module enabled: the SSE stream ends with an `error` event whose
      // message (the gitctx DNS failure) must be rendered non-empty.
      const message = await failedCard.locator('pre').textContent()
      expect(
        message?.trim() ?? '',
        'build error text rendered in the failure card'
      ).not.toBe('')
    }

    // The stream ended cleanly: the submit button returned (running=false)
    // and the output panel fell back to its idle message — the failure
    // happened before any buildkit vertex existed, on both environments.
    await expect(
      page.getByRole('button', { name: 'Start Build' })
    ).toBeVisible()
    await expect(start).toBeEnabled()
    await expect(
      page.getByText('Start a build to see its output here')
    ).toBeVisible()

    // No crash: header, form and fleet-status sections all still render.
    await expect(
      page.getByRole('heading', { name: 'Image Builds' })
    ).toBeVisible()
    await expect(card(page, 'New Build')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Fleet Status' })).toBeVisible()
  })
})

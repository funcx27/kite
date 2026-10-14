import { expect, test } from '@playwright/test'

import { authFile } from '../../env'

test.use({ storageState: authFile })

test.describe('platform applications smoke', () => {
  test('shell, application list and settings render without crashing', async ({
    page,
  }) => {
    await page.goto('/applications')

    // Platform shell: the workbench sidebar renders with its Workflows entry.
    await expect(page.getByRole('link', { name: 'Workflows' })).toBeVisible()

    // Application list: toolbar search and the table header render whether
    // the list has rows or is empty.
    await expect(page.getByLabel('Search applications...')).toBeVisible()
    await expect(
      page.getByRole('columnheader', { name: 'Name', exact: true })
    ).toBeVisible()
    await expect(page).toHaveTitle('Applications - Kite')

    await page.goto('/application-settings')

    await expect(
      page.getByRole('heading', { name: 'Settings', exact: true })
    ).toBeVisible()
    await expect(
      page.getByText('Manage settings for application mode')
    ).toBeVisible()

    // Back to the list: the shell and page render again after the round trip.
    await page.goto('/applications')

    await expect(page.getByRole('link', { name: 'Workflows' })).toBeVisible()
    await expect(page.getByLabel('Search applications...')).toBeVisible()
  })
})

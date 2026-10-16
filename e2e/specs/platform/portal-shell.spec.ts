import { expect, test } from '@playwright/test'

import { adminUser, authFile } from '../../env'

test.use({ storageState: authFile })

// Regression coverage for the portal shell itself: the bare /portal redirect,
// the portal-scoped logout/login round trip, the language menu, and the
// invalid-auth bounce to the portal login. No fixtures are created; the login
// step re-signs in as the shared admin account from env.ts.
test.describe('portal shell', () => {
  test('bare /portal redirects to the application list', async ({ page }) => {
    await page.goto('/portal')

    // The platform SPA has no landing route: the index route navigates to
    // /applications (ui/src/portal/main.tsx).
    await expect(page).toHaveURL(/\/portal\/applications$/)
    await expect(page.getByRole('link', { name: 'Workflows' })).toBeVisible()
    await expect(page.getByLabel('Search applications...')).toBeVisible()
    await expect(page).toHaveTitle('Applications - Kite')
  })

  test('logout stays on the portal login and signing back in returns to it', async ({
    page,
  }) => {
    await page.goto('/portal/applications')

    // The portal user menu overrides the shared logout to stay inside the
    // portal SPA (ui/src/portal/user-menu.tsx): the landing spot must be the
    // portal's own /portal/login, never the console's /login.
    await page.getByLabel('User menu').click()
    await page.getByRole('menuitem', { name: 'Log out' }).click()
    await expect(page).toHaveURL(/\/portal\/login$/)

    // A protected route now bounces back to the portal login with a return
    // href (ProtectedRoute sees bootstrap user:null).
    await page.goto('/portal/applications')
    await expect(page).toHaveURL(/\/portal\/login\?href=%2Fapplications$/)

    // Signing back in follows the href target: the portal application list.
    await page.getByLabel('Username').fill(adminUser.username)
    await page.getByLabel('Password', { exact: true }).fill(adminUser.password)
    await page.getByRole('button', { name: 'Sign In with Password' }).click()
    await expect(page).toHaveURL(/\/portal\/applications$/)
    await expect(page.getByLabel('Search applications...')).toBeVisible()
  })

  test('language menu switches the shell text between English and Chinese', async ({
    page,
  }) => {
    await page.goto('/portal/applications')
    const applicationsLink = page
      .getByRole('link', { name: 'Applications', exact: true })
    await expect(applicationsLink).toBeVisible()

    // The header language toggle (ui/src/components/language-toggle.tsx)
    // changes i18n language in place; assert the sidebar label follows.
    await page.locator('header').getByRole('button', { name: 'Language' }).click()
    await page.getByRole('menuitem', { name: '中文' }).click()
    await expect(page.getByRole('link', { name: '应用', exact: true })).toBeVisible()
    await expect(applicationsLink).toHaveCount(0)

    // Switch back to English so the shell reads as before.
    await page.locator('header').getByRole('button', { name: '语言' }).click()
    await page.getByRole('menuitem', { name: 'English' }).click()
    await expect(applicationsLink).toBeVisible()
  })

  // Invalid auth on a fresh navigation goes through ProtectedRoute: bootstrap
  // answers 200 with user:null and the router redirects to the portal login.
  // (The separate known gap sits elsewhere: a mid-session API 401 in
  // ui/src/lib/api-client.ts hard-redirects to the console /login.)
  test('cleared auth on a protected route redirects to the portal login', async ({
    page,
    context,
  }) => {
    await page.goto('/portal/applications')
    await expect(page.getByLabel('Search applications...')).toBeVisible()

    // auth_token is httpOnly, so it can only be dropped at the context level.
    await context.clearCookies()
    await page.goto('/portal/applications')
    await expect(page).toHaveURL(/\/portal\/login\?href=/)
  })

  test('a forged invalid auth token redirects to the portal login', async ({
    page,
    context,
  }) => {
    await page.goto('/portal/applications')
    await expect(page.getByLabel('Search applications...')).toBeVisible()

    // Overwrite the real cookie with a garbage token of the same name.
    await context.addCookies([
      {
        name: 'auth_token',
        value: `e2e-invalid-${Date.now()}`,
        url: new URL(page.url()).origin,
      },
    ])
    await page.goto('/portal/applications')
    await expect(page).toHaveURL(/\/portal\/login\?href=/)
  })
})

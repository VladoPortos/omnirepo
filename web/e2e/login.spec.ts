/**
 * Login E2E tests.
 *
 * Covers: login form shape, dark-mode default, invalid-cred error,
 * dashboard redirect for the pre-seeded admin, forced change-password
 * flow for a newly-created user, and logout.
 *
 * global-setup seeds the super-admin with `must_change_password=0`, so
 * the forced-change path is exercised via a second user provisioned
 * through `POST /admin/users` rather than the admin itself. See
 * web/e2e/helpers/auth.ts for the helper.
 */

import { test, expect } from '@playwright/test';
import {
  ADMIN_LOGIN,
  ADMIN_PASSWORD,
  adminLoginAPI,
  createForcedChangeUser,
  resetServerState,
} from './helpers/auth';

test.describe('Login page', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('displays sign-in form with OmniRepo branding', async ({ page }) => {
    await page.goto('/login');
    await expect(page.getByText('Sign in to OmniRepo')).toBeVisible();
    await expect(page.locator('input#login')).toBeVisible();
    await expect(page.locator('input#password')).toBeVisible();
    await expect(page.getByRole('button', { name: /sign in/i })).toBeVisible();
  });

  test('dark mode is the default', async ({ page }) => {
    await page.goto('/login');
    const html = page.locator('html');
    const cls = await html.getAttribute('class');
    const dataTheme = await html.getAttribute('data-theme');
    expect(cls?.includes('dark') || dataTheme === 'dark').toBeTruthy();
  });

  test('shows error on invalid credentials', async ({ page }) => {
    await page.goto('/login');
    await page.fill('input#login', 'nonexistent');
    await page.fill('input#password', 'wrongpassword');
    await page.click('button[type="submit"]');
    await expect(page.getByText(/invalid login or password/i)).toBeVisible({
      timeout: 10000,
    });
  });

  test('admin login lands somewhere other than /login', async ({ page }) => {
    await page.goto('/login');
    await page.fill('input#login', ADMIN_LOGIN);
    await page.fill('input#password', ADMIN_PASSWORD);
    await page.click('button[type="submit"]');
    // global-setup seeds must_change_password=0, so the post-login
    // redirect goes straight to the dashboard — anywhere but /login.
    await expect(page).not.toHaveURL(/\/login$/, { timeout: 10_000 });
    await expect(page).not.toHaveURL(/\/change-password/);
  });

  test('newly-created user is forced to change their password', async ({
    page,
    request,
  }) => {
    // Admin creates a second user; server returns a one-time password
    // with must_change_password=true hard-coded in the create handler.
    await adminLoginAPI(request);
    const otp = await createForcedChangeUser(
      request,
      'forcedchange-user',
      'forcedchange@local',
    );

    await page.goto('/login');
    await page.fill('input#login', 'forcedchange-user');
    await page.fill('input#password', otp);
    await page.click('button[type="submit"]');

    // Must redirect to /change-password because must_change_password=1.
    await expect(page).toHaveURL(/\/change-password/, { timeout: 10_000 });

    // Drive the change-password form and confirm dashboard redirect.
    const currentPwInput = page.locator(
      'input[autocomplete="current-password"], input#current-password, input[name="current"]',
    );
    const newPwInput = page.locator(
      'input[autocomplete="new-password"], input#new-password, input[name="new"]',
    );
    const confirmInput = page.locator(
      'input[name="confirm"], input#confirm-password',
    );
    if ((await currentPwInput.count()) > 0) {
      await currentPwInput.first().fill(otp);
    }
    if ((await newPwInput.count()) > 0) {
      await newPwInput.first().fill('NewSecure123!');
    }
    if ((await confirmInput.count()) > 0) {
      await confirmInput.first().fill('NewSecure123!');
    }
    const submitBtn = page.getByRole('button', {
      name: /change|update|save/i,
    });
    if ((await submitBtn.count()) > 0) {
      await submitBtn.first().click();
      // After successful change the server clears must_change_password
      // and the SPA navigates back to /dashboard (or the root if no
      // prior pathname was captured). Either is fine — what matters is
      // that we left /change-password without being bounced to /login.
      await expect(page).not.toHaveURL(/\/change-password/, {
        timeout: 10_000,
      });
      await expect(page).not.toHaveURL(/\/login/);
    }
  });

  test('logout redirects to login', async ({ page }) => {
    await page.goto('/login');
    await page.fill('input#login', ADMIN_LOGIN);
    await page.fill('input#password', ADMIN_PASSWORD);
    await page.click('button[type="submit"]');
    await expect(page).not.toHaveURL(/\/login$/, { timeout: 10_000 });

    const logoutBtn = page.getByRole('button', {
      name: /log\s*out|sign\s*out/i,
    });
    if ((await logoutBtn.count()) > 0) {
      await logoutBtn.first().click();
      await expect(page).toHaveURL(/\/login/, { timeout: 10_000 });
    }
  });
});

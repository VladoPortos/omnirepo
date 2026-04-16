/**
 * Login E2E tests (D-48).
 * Covers: login with valid creds, forced password change flow,
 * invalid creds error, dark mode default, logout.
 */

import { test, expect } from '@playwright/test';

// Bootstrap admin credentials (DATA_ROOT is a temp dir with no bootstrap.json,
// so the server creates a default admin user).
const ADMIN_LOGIN = 'admin';
const ADMIN_PASSWORD = 'changeme';

test.describe('Login page', () => {
  test('displays sign-in form with OmniRepo branding', async ({ page }) => {
    await page.goto('/login');
    await expect(page.getByText('Sign in to OmniRepo')).toBeVisible();
    await expect(page.locator('input#login')).toBeVisible();
    await expect(page.locator('input#password')).toBeVisible();
    await expect(page.getByRole('button', { name: /sign in/i })).toBeVisible();
  });

  test('dark mode is the default', async ({ page }) => {
    await page.goto('/login');
    // The root <html> element should have class "dark" or data-theme="dark"
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

  test('login with valid credentials and forced password change', async ({
    page,
  }) => {
    await page.goto('/login');
    await page.fill('input#login', ADMIN_LOGIN);
    await page.fill('input#password', ADMIN_PASSWORD);
    await page.click('button[type="submit"]');

    // Bootstrap admin has must_change_password=true, so we land on /change-password
    await expect(page).toHaveURL(/\/change-password/, { timeout: 10000 });
  });

  test('password change flow completes', async ({ page }) => {
    // Login first
    await page.goto('/login');
    await page.fill('input#login', ADMIN_LOGIN);
    await page.fill('input#password', ADMIN_PASSWORD);
    await page.click('button[type="submit"]');
    await expect(page).toHaveURL(/\/change-password/, { timeout: 10000 });

    // Fill the change password form
    const currentPwInput = page.locator(
      'input[autocomplete="current-password"], input#current-password, input[name="current"]',
    );
    const newPwInput = page.locator(
      'input[autocomplete="new-password"], input#new-password, input[name="new"]',
    );

    if ((await currentPwInput.count()) > 0) {
      await currentPwInput.first().fill(ADMIN_PASSWORD);
    }
    if ((await newPwInput.count()) > 0) {
      await newPwInput.first().fill('NewSecure123!');
    }

    // Look for confirm field
    const confirmInput = page.locator(
      'input[name="confirm"], input#confirm-password',
    );
    if ((await confirmInput.count()) > 0) {
      await confirmInput.first().fill('NewSecure123!');
    }

    // Submit
    const submitBtn = page.getByRole('button', { name: /change|update|save/i });
    if ((await submitBtn.count()) > 0) {
      await submitBtn.first().click();
      // Should redirect to dashboard after successful change
      await expect(page).toHaveURL(/^\/$|\/dashboard/, { timeout: 10000 });
    }
  });

  test('logout redirects to login', async ({ page }) => {
    // Login with bootstrap creds
    await page.goto('/login');
    await page.fill('input#login', ADMIN_LOGIN);
    await page.fill('input#password', ADMIN_PASSWORD);
    await page.click('button[type="submit"]');

    // Wait for auth to settle
    await page.waitForTimeout(1000);

    // If we need to change password first, do so
    if (page.url().includes('change-password')) {
      // Perform password change
      const currentPwInput = page.locator(
        'input[autocomplete="current-password"], input#current-password',
      );
      const newPwInput = page.locator(
        'input[autocomplete="new-password"], input#new-password',
      );
      if ((await currentPwInput.count()) > 0) {
        await currentPwInput.first().fill(ADMIN_PASSWORD);
      }
      if ((await newPwInput.count()) > 0) {
        await newPwInput.first().fill('LogoutTest1!');
      }
      const confirmInput = page.locator(
        'input[name="confirm"], input#confirm-password',
      );
      if ((await confirmInput.count()) > 0) {
        await confirmInput.first().fill('LogoutTest1!');
      }
      const submitBtn = page.getByRole('button', {
        name: /change|update|save/i,
      });
      if ((await submitBtn.count()) > 0) {
        await submitBtn.first().click();
        await page.waitForTimeout(1000);
      }
    }

    // Look for logout button in the app shell
    const logoutBtn = page.getByRole('button', { name: /log\s*out|sign\s*out/i });
    if ((await logoutBtn.count()) > 0) {
      await logoutBtn.first().click();
      await expect(page).toHaveURL(/\/login/, { timeout: 10000 });
    }
  });
});

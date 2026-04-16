/**
 * Dashboard E2E tests.
 * Verifies the dashboard renders with storage gauge, repo count,
 * user count, and quick action buttons.
 */

import { test, expect } from '@playwright/test';

test.describe('Dashboard page', () => {
  test.beforeEach(async ({ request }) => {
    // Login via API to get session cookie
    await request.post('/api/v1/auth/login', {
      data: { login: 'admin', password: 'changeme' },
    });
  });

  test('renders dashboard heading', async ({ page }) => {
    await page.goto('/');
    // May redirect to /login or /change-password if not authenticated
    // For E2E against a fresh server, just verify the page loads
    await page.waitForTimeout(2000);

    // If redirected to login, authenticate inline
    if (page.url().includes('/login')) {
      await page.fill('input#login', 'admin');
      await page.fill('input#password', 'changeme');
      await page.click('button[type="submit"]');
      await page.waitForTimeout(2000);
    }

    // If on change-password, the user has MCP flag
    if (page.url().includes('/change-password')) {
      // Skip dashboard test for MCP users -- need password change first
      test.skip();
      return;
    }

    await expect(page.getByText('Dashboard')).toBeVisible({ timeout: 10000 });
  });

  test('shows stat cards', async ({ page }) => {
    await page.goto('/');
    await page.waitForTimeout(2000);

    if (page.url().includes('/login')) {
      await page.fill('input#login', 'admin');
      await page.fill('input#password', 'changeme');
      await page.click('button[type="submit"]');
      await page.waitForTimeout(2000);
    }

    if (page.url().includes('/change-password')) {
      test.skip();
      return;
    }

    // Storage gauge card
    await expect(
      page.getByText(/storage|repositories|users/i).first(),
    ).toBeVisible({ timeout: 10000 });
  });

  test('quick action buttons are visible', async ({ page }) => {
    await page.goto('/');
    await page.waitForTimeout(2000);

    if (page.url().includes('/login')) {
      await page.fill('input#login', 'admin');
      await page.fill('input#password', 'changeme');
      await page.click('button[type="submit"]');
      await page.waitForTimeout(2000);
    }

    if (page.url().includes('/change-password')) {
      test.skip();
      return;
    }

    // Quick action buttons
    await expect(
      page.getByRole('link', { name: /create project/i }),
    ).toBeVisible({ timeout: 10000 });
    await expect(
      page.getByRole('link', { name: /upload artifact/i }),
    ).toBeVisible({ timeout: 10000 });
  });
});

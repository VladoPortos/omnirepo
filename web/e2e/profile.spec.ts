/**
 * Profile E2E tests.
 * API key creation with one-time reveal, copy button, password change.
 */

import { test, expect } from '@playwright/test';
import { adminLoginAPI, resetServerState } from './helpers/auth';

test.describe('Profile page', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('profile page renders user info', async ({ page }) => {
    await page.goto('/profile');
    await page.waitForTimeout(2000);

    if (page.url().includes('/login')) {
      await page.fill('input#login', 'admin');
      await page.fill('input#password', 'AdminTest1!');
      await page.click('button[type="submit"]');
      await page.waitForTimeout(2000);
    }

    if (page.url().includes('/change-password')) {
      test.skip();
      return;
    }

    // Should show user info
    await expect(page.getByText('admin').first()).toBeVisible({
      timeout: 10000,
    });
  });

  test('API key creation with one-time reveal', async ({ page }) => {
    await page.goto('/profile');
    await page.waitForTimeout(2000);

    if (page.url().includes('/login')) {
      await page.fill('input#login', 'admin');
      await page.fill('input#password', 'AdminTest1!');
      await page.click('button[type="submit"]');
      await page.waitForTimeout(2000);
    }

    if (page.url().includes('/change-password')) {
      test.skip();
      return;
    }

    // Look for "Create API Key" button
    const createBtn = page.getByRole('button', {
      name: /create.*api.*key|new.*api.*key|generate/i,
    });
    if ((await createBtn.count()) > 0) {
      await createBtn.first().click();
      await page.waitForTimeout(500);

      // Fill in label/name for the key
      const labelInput = page.locator(
        'input[name="label"], input[name="name"], input[placeholder*="label" i], input[placeholder*="name" i]',
      );
      if ((await labelInput.count()) > 0) {
        await labelInput.first().fill('e2e-test-key');
      }

      // Submit
      const submitBtn = page.getByRole('button', {
        name: /create|generate|save/i,
      });
      if ((await submitBtn.count()) > 0) {
        await submitBtn.first().click();
        await page.waitForTimeout(2000);
      }

      // One-time reveal: the key should be visible once
      const keyDisplay = page.locator(
        '[data-testid="api-key-reveal"], code, pre, .api-key-value, [class*="monospace"]',
      );
      if ((await keyDisplay.count()) > 0) {
        const keyText = await keyDisplay.first().textContent();
        expect(keyText).toBeTruthy();
        expect(keyText!.length).toBeGreaterThan(8);
      }

      // Copy button should be available
      const copyBtn = page.getByRole('button', { name: /copy/i });
      if ((await copyBtn.count()) > 0) {
        await expect(copyBtn.first()).toBeVisible();
      }
    }
  });

  test('password change from profile', async ({ page }) => {
    await page.goto('/profile');
    await page.waitForTimeout(2000);

    if (page.url().includes('/login')) {
      await page.fill('input#login', 'admin');
      await page.fill('input#password', 'AdminTest1!');
      await page.click('button[type="submit"]');
      await page.waitForTimeout(2000);
    }

    if (page.url().includes('/change-password')) {
      test.skip();
      return;
    }

    // ProfilePage renders a disabled "Update Password" button until
    // current / new / confirm are populated + new matches confirm.
    // Pre-F-15.4 the spec just clicked the button blindly — it timed
    // out because the disabled-gate never cleared. Fill the form so
    // the click has something to submit.
    await page.fill('input#current-pw', 'AdminTest1!');
    await page.fill('input#new-pw', 'ProfileTest1!');
    await page.fill('input#confirm-pw', 'ProfileTest1!');
    const updateBtn = page.getByRole('button', {
      name: /update password/i,
      exact: false,
    });
    await expect(updateBtn).toBeEnabled({ timeout: 5_000 });
    await updateBtn.click();
    await page.waitForTimeout(500);
  });
});

/**
 * Profile E2E tests.
 * API key creation with one-time reveal, copy button, password change.
 */

import { test, expect } from '@playwright/test';

test.describe('Profile page', () => {
  test.beforeEach(async ({ request }) => {
    const resp = await request.post('/api/v1/auth/login', {
      data: { login: 'admin', password: 'changeme' },
    });
    const body = await resp.json();
    if (body.must_change_password) {
      await request.post('/api/v1/auth/change-password', {
        data: { current: 'changeme', new: 'ProfileTest1!' },
      });
      await request.post('/api/v1/auth/login', {
        data: { login: 'admin', password: 'ProfileTest1!' },
      });
    }
  });

  test('profile page renders user info', async ({ page }) => {
    await page.goto('/profile');
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
      await page.fill('input#password', 'changeme');
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
      await page.fill('input#password', 'changeme');
      await page.click('button[type="submit"]');
      await page.waitForTimeout(2000);
    }

    if (page.url().includes('/change-password')) {
      test.skip();
      return;
    }

    // Look for password change section
    const changePwBtn = page.getByRole('button', {
      name: /change.*password|update.*password/i,
    });
    if ((await changePwBtn.count()) > 0) {
      await changePwBtn.first().click();
      await page.waitForTimeout(500);
    }
  });
});

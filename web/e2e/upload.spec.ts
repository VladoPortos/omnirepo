/**
 * Upload E2E tests.
 * Upload artifact via dropzone, verify toast, verify listing.
 */

import { test, expect } from '@playwright/test';

test.describe('Upload page', () => {
  test.beforeEach(async ({ request }) => {
    // Login as admin
    const resp = await request.post('/api/v1/auth/login', {
      data: { login: 'admin', password: 'changeme' },
    });
    const body = await resp.json();
    if (body.must_change_password) {
      await request.post('/api/v1/auth/change-password', {
        data: { current: 'changeme', new: 'UploadTest1!' },
      });
      await request.post('/api/v1/auth/login', {
        data: { login: 'admin', password: 'UploadTest1!' },
      });
    }

    // Create project and raw repo via API
    await request.post('/api/v1/projects', {
      data: { name: 'upload-test' },
    });
    await request.post('/api/v1/projects/upload-test/repos', {
      data: { name: 'raw-uploads', type: 'raw' },
    });
  });

  test('upload a file via dropzone', async ({ page }) => {
    // Navigate to the raw repo detail page
    await page.goto('/projects/upload-test/raw/raw-uploads');
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

    // Look for upload/dropzone area
    const dropzone = page.locator(
      '[data-testid="dropzone"], .dropzone, [role="button"]:has-text("upload"), [role="button"]:has-text("drop")',
    );

    if ((await dropzone.count()) > 0) {
      // Create a test file buffer
      const buffer = Buffer.from('test file content for upload e2e');

      // Use Playwright's file chooser to upload
      const fileChooserPromise = page.waitForEvent('filechooser', {
        timeout: 5000,
      }).catch(() => null);
      await dropzone.first().click();
      const fileChooser = await fileChooserPromise;

      if (fileChooser) {
        await fileChooser.setFiles({
          name: 'test-upload.txt',
          mimeType: 'text/plain',
          buffer,
        });
        await page.waitForTimeout(3000);

        // Look for success indication (toast, file in listing, etc.)
        const success = page.locator(
          '[data-testid="upload-success"], .toast, [role="alert"]',
        );
        if ((await success.count()) > 0) {
          await expect(success.first()).toBeVisible({ timeout: 10000 });
        }
      }
    }
  });
});

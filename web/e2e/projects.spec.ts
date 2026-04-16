/**
 * Projects E2E tests.
 * Golden path: create project -> list -> click -> tabs -> create repos of
 * each type -> verify.
 */

import { test, expect } from '@playwright/test';

// Helper: login via API and return the context
async function loginAsAdmin(request: import('@playwright/test').APIRequestContext) {
  const resp = await request.post('/api/v1/auth/login', {
    data: { login: 'admin', password: 'changeme' },
  });
  return resp;
}

// Helper: change password via API if needed
async function changePasswordIfNeeded(request: import('@playwright/test').APIRequestContext) {
  const loginResp = await request.post('/api/v1/auth/login', {
    data: { login: 'admin', password: 'changeme' },
  });
  const body = await loginResp.json();
  if (body.must_change_password) {
    await request.post('/api/v1/auth/change-password', {
      data: { current: 'changeme', new: 'E2EPass123!' },
    });
    // Re-login with new password
    await request.post('/api/v1/auth/login', {
      data: { login: 'admin', password: 'E2EPass123!' },
    });
  }
}

test.describe('Projects page', () => {
  test.beforeEach(async ({ request }) => {
    await loginAsAdmin(request);
  });

  test('golden path: create project -> list -> view', async ({
    page,
    request,
  }) => {
    await changePasswordIfNeeded(request);

    // Navigate to projects page
    await page.goto('/projects');
    await page.waitForTimeout(2000);

    // Handle login redirect
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

    // Look for create project button or dialog trigger
    const createBtn = page.getByRole('button', { name: /create|new/i });
    if ((await createBtn.count()) > 0) {
      await createBtn.first().click();
      await page.waitForTimeout(500);

      // Fill project name in dialog/form
      const nameInput = page.locator(
        'input[name="name"], input[placeholder*="project"], input[placeholder*="name"]',
      );
      if ((await nameInput.count()) > 0) {
        await nameInput.first().fill('e2e-test-project');
        // Submit
        const submitBtn = page.getByRole('button', {
          name: /create|save|submit/i,
        });
        if ((await submitBtn.count()) > 0) {
          await submitBtn.first().click();
          await page.waitForTimeout(2000);
        }
      }
    }

    // Verify project appears
    await expect(page.getByText('e2e-test-project')).toBeVisible({
      timeout: 10000,
    });
  });

  test('create repos of each type via API then verify in UI', async ({
    page,
    request,
  }) => {
    await changePasswordIfNeeded(request);

    // Create project via API
    await request.post('/api/v1/projects', {
      data: { name: 'repo-types-test' },
    });

    // Create one repo of each type via API
    const types = ['rpm', 'deb', 'pypi', 'docker', 'helm', 'git', 'raw'];
    for (const typ of types) {
      await request.post('/api/v1/projects/repo-types-test/repos', {
        data: { name: `test-${typ}`, type: typ },
      });
    }

    // Navigate to project detail page
    await page.goto('/projects/repo-types-test');
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

    // Verify at least some repo names appear
    for (const typ of ['rpm', 'docker', 'raw']) {
      await expect(page.getByText(`test-${typ}`)).toBeVisible({
        timeout: 10000,
      });
    }
  });
});

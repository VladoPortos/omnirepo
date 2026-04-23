/**
 * Projects E2E tests.
 * Golden path: create project -> list -> click -> tabs -> create repos
 * of each type -> verify.
 *
 * Post-v1.4 (F-15.4): uses adminLoginUI / adminLoginAPI helpers and
 * tightens locators against UI drift:
 *   - "Create Project" is a DialogTrigger-wrapped button, not a link.
 *   - The project list renders the project name as a link; strict mode
 *     complains if we match raw text that also appears in breadcrumbs.
 */

import { test, expect } from '@playwright/test';
import { adminLoginAPI, adminLoginUI } from './helpers/auth';

test.describe('Projects page', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
  });

  test('golden path: create project -> list -> view', async ({ page }) => {
    await adminLoginUI(page);
    await page.goto('/projects');

    // ProjectsPage renders <DialogTrigger render={<Button />}>Create Project</>
    // — role="button". The dialog trigger text is stable; click on it.
    await page.getByRole('button', { name: /create project/i }).first().click();

    // Dialog has input#project-name (see ProjectsPage.tsx).
    await page.fill('input#project-name', 'e2e-test-project');

    // Footer submit button also says "Create Project" — disambiguate
    // by scoping to the dialog's footer.
    const dialog = page.getByRole('dialog');
    await dialog.getByRole('button', { name: /create project/i }).click();

    // Dialog closes and the new project appears in the list.
    await expect(
      page.getByRole('link', { name: /e2e-test-project/i }).first(),
    ).toBeVisible({ timeout: 10_000 });
  });

  test('create repos of each type via API then verify in UI', async ({
    page,
    request,
  }) => {
    await adminLoginUI(page);
    await request.post('/api/v1/projects', {
      data: { name: 'repo-types-test' },
    });
    const types = ['rpm', 'deb', 'pypi', 'docker', 'helm', 'git', 'raw'];
    for (const typ of types) {
      await request.post('/api/v1/projects/repo-types-test/repos', {
        data: { name: `test-${typ}`, type: typ },
      });
    }

    await page.goto('/projects/repo-types-test');

    // Each repo row renders the full slug `{proj}/{type}/{name}` — the
    // raw name substring may match in breadcrumbs + the row, so match
    // on the first occurrence explicitly.
    for (const typ of ['rpm', 'docker', 'raw']) {
      await expect(page.getByText(`test-${typ}`).first()).toBeVisible({
        timeout: 10_000,
      });
    }
  });
});

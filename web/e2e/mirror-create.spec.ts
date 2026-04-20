/**
 * Phase 8 / plan 08-04 — Playwright coverage for creating a mirror repo.
 *
 * Drives the CreateRepoDialog MirrorConfigSection end-to-end:
 *   1. Login as admin
 *   2. Create project (real POST)
 *   3. Open Create Repository dialog
 *   4. Select APT type → assert MirrorConfigSection appears
 *   5. Tick "This repo is a mirror of an upstream"
 *   6. Fill upstream URL + Suites + Components + Arches
 *   7. Mock POST /repos route and assert body.is_mirror === true and
 *      mirror_filter has PascalCase keys (Suites/Components/Arches)
 *
 * Plus gate tests:
 *   - Switching type to 'raw' hides MirrorConfigSection
 *   - Empty URL → "Upstream URL is required"
 *   - Invalid URL (file://) → "URL must use http(s)"
 *
 * Auth bootstrap mirrors docker-clone.spec.ts exactly.
 */

import { expect, test, type APIRequestContext, type Page } from '@playwright/test';

test.use({ viewport: { width: 1440, height: 900 } });

const ADMIN_PW = 'AdminTest1!';

async function bootstrapAdmin(request: APIRequestContext): Promise<void> {
  const first = await request.post('/api/v1/auth/login', {
    data: { login: 'admin', password: 'changeme' },
  });
  if (first.ok()) {
    const body = await first.json();
    if (body.must_change_password) {
      await request.post('/api/v1/auth/change-password', {
        data: { current: 'changeme', new: ADMIN_PW },
      });
    }
  }
  await request.post('/api/v1/auth/login', {
    data: { login: 'admin', password: ADMIN_PW },
  });
}

async function uiLoginAdmin(page: Page): Promise<void> {
  await page.goto('/login');
  await page.fill('input#login', 'admin');
  await page.fill('input#password', ADMIN_PW);
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle');
}

async function seedProject(
  request: APIRequestContext,
  name: string,
): Promise<string> {
  await request.post('/api/v1/projects', { data: { name } });
  return name;
}

test.describe('Mirror repo creation (Phase 8 / plan 08-04)', () => {
  test.beforeEach(async ({ request }) => {
    await bootstrapAdmin(request);
  });

  test('APT mirror happy path: filter widget emits PascalCase JSON', async ({
    page,
    request,
  }) => {
    const project = await seedProject(request, `mirror-create-${Date.now()}`);

    // Mock upstream-creds so the picker loads instantly regardless of AEAD.
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/upstream-creds/`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([]),
        }),
    );

    // Intercept the actual POST and capture the body for assertion.
    let capturedBody: Record<string, unknown> | null = null;
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/repos`,
      (route) => {
        const req = route.request();
        if (req.method() === 'POST') {
          const post = req.postDataJSON() as Record<string, unknown>;
          capturedBody = post;
          return route.fulfill({
            status: 201,
            contentType: 'application/json',
            body: JSON.stringify({
              id: 99,
              project_id: 1,
              type: post.type,
              name: post.name,
              description_md: '',
              auto_scan: false,
              block_on_severity: 'none',
              public_read: false,
              size_bytes: 0,
              item_count: 0,
              created_at: new Date().toISOString(),
              is_mirror: post.is_mirror ?? false,
              mirror_upstream_url: post.mirror_upstream_url ?? '',
              mirror_filter_json: post.mirror_filter
                ? JSON.stringify(post.mirror_filter)
                : '',
              mirror_cred_id: post.mirror_cred_id ?? null,
              scan_on_sync: post.scan_on_sync ?? false,
            }),
          });
        }
        return route.fallback();
      },
    );

    await uiLoginAdmin(page);
    await page.goto(`/projects/${encodeURIComponent(project)}`);

    // Go to APT tab and open create dialog.
    await page.getByRole('tab', { name: /^APT/ }).click();
    await page
      .getByRole('button', { name: /Create Repository/i })
      .first()
      .click();

    const dialog = page.getByRole('dialog', { name: 'Create Repository' });
    await expect(dialog).toBeVisible();

    // MirrorConfigSection is visible for APT (type preselected to 'deb').
    await expect(
      dialog.getByText('This repo is a mirror of an upstream'),
    ).toBeVisible();

    // Tick the checkbox.
    await dialog
      .getByRole('checkbox', { name: 'This repo is a mirror of an upstream' })
      .click();

    // Fill URL + Suites + toggle Components(main) + Arches(amd64).
    await dialog
      .locator('input#mirror-url')
      .fill('https://archive.ubuntu.com/ubuntu');
    await dialog.locator('input#apt-suites').fill('focal');

    await dialog
      .getByRole('checkbox', { name: 'main' })
      .first()
      .click();
    await dialog
      .getByRole('checkbox', { name: 'amd64' })
      .first()
      .click();

    // Fill repo name.
    await dialog.locator('input#repo-name').fill('focal-main-test-mirror');

    // Submit.
    await dialog.getByRole('button', { name: /Create Repository/i }).click();

    // Wait for the dialog to close (=mutation resolved).
    await expect(dialog).not.toBeVisible({ timeout: 5000 });

    // Assert the captured body.
    expect(capturedBody).not.toBeNull();
    const body = capturedBody as Record<string, unknown>;
    expect(body.name).toBe('focal-main-test-mirror');
    expect(body.type).toBe('deb');
    expect(body.is_mirror).toBe(true);
    expect(body.mirror_upstream_url).toBe('https://archive.ubuntu.com/ubuntu');
    const filter = body.mirror_filter as Record<string, string[]>;
    expect(filter).toBeTruthy();
    expect(filter.Suites).toEqual(['focal']);
    expect(filter.Components).toEqual(['main']);
    expect(filter.Arches).toEqual(['amd64']);
  });

  test('type switch toggles visibility of MirrorConfigSection', async ({
    page,
    request,
  }) => {
    const project = await seedProject(request, `mirror-gate-${Date.now()}`);

    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/upstream-creds/`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([]),
        }),
    );

    await uiLoginAdmin(page);
    await page.goto(`/projects/${encodeURIComponent(project)}`);
    await page.getByRole('tab', { name: /^APT/ }).click();
    await page
      .getByRole('button', { name: /Create Repository/i })
      .first()
      .click();

    const dialog = page.getByRole('dialog', { name: 'Create Repository' });
    await expect(dialog).toBeVisible();

    // Initially APT — section visible.
    await expect(
      dialog.getByText('This repo is a mirror of an upstream'),
    ).toBeVisible();

    // Switch to RAW via the select.
    await dialog.locator('button#repo-type').click();
    await page.getByRole('option', { name: 'RAW' }).click();

    // Section hidden.
    await expect(
      dialog.getByText('This repo is a mirror of an upstream'),
    ).not.toBeVisible();

    // Switch back to APT — visible again.
    await dialog.locator('button#repo-type').click();
    await page.getByRole('option', { name: 'APT' }).click();
    await expect(
      dialog.getByText('This repo is a mirror of an upstream'),
    ).toBeVisible();
  });

  test('URL validation: empty + non-http(s) both surface client errors', async ({
    page,
    request,
  }) => {
    const project = await seedProject(request, `mirror-val-${Date.now()}`);

    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/upstream-creds/`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([]),
        }),
    );

    await uiLoginAdmin(page);
    await page.goto(`/projects/${encodeURIComponent(project)}`);
    await page.getByRole('tab', { name: /^APT/ }).click();
    await page
      .getByRole('button', { name: /Create Repository/i })
      .first()
      .click();

    const dialog = page.getByRole('dialog', { name: 'Create Repository' });
    await expect(dialog).toBeVisible();

    // Turn on mirror, fill repo-name, leave URL blank.
    await dialog
      .getByRole('checkbox', { name: 'This repo is a mirror of an upstream' })
      .click();
    await dialog.locator('input#repo-name').fill('empty-url-mirror');
    // The URL input has type="url" + required; browsers short-circuit
    // validation before we ever run JS. Remove the required attribute
    // via evaluate so we can exercise the JS fallback branch.
    await dialog.locator('input#mirror-url').evaluate((el) => {
      (el as HTMLInputElement).removeAttribute('required');
      (el as HTMLInputElement).removeAttribute('type');
    });
    await dialog.getByRole('button', { name: /Create Repository/i }).click();
    await expect(
      dialog.getByText('Upstream URL is required'),
    ).toBeVisible();

    // Now fill file:// — assert http(s) rule.
    await dialog.locator('input#mirror-url').fill('file:///etc/passwd');
    await dialog.getByRole('button', { name: /Create Repository/i }).click();
    await expect(dialog.getByText('URL must use http(s)')).toBeVisible();
  });

});

/**
 * Playwright coverage for the Mirror config
 * card on the new /projects/:name/:type/:repo/settings route.
 *
 * Covers:
 *   1. Edit-and-save — seed APT mirror with Components=["main"], visit
 *      /settings, add "universe" via the widget, Save; intercept the
 *      PATCH request and assert body.mirror_filter.Components equals
 *      ["main", "universe"] (PascalCase keys asserted).
 *   2. Readonly URL — the upstream URL input on /settings is not
 *      editable (rendered via CopyInline — no input[readonly]; we
 *      assert CopyInline is visible and the MirrorConfigSection's URL
 *      input carries readOnly).
 *   3. Non-mirror repo — the Mirror config card is NOT rendered.
 */

import { expect, test, type APIRequestContext, type Page } from '@playwright/test';
import { adminLoginAPI, resetServerState } from './helpers/auth';

test.use({ viewport: { width: 1440, height: 900 } });

const ADMIN_PW = 'AdminTest1!';

async function uiLoginAdmin(page: Page): Promise<void> {
  await page.goto('/login');
  await page.fill('input#login', 'admin');
  await page.fill('input#password', ADMIN_PW);
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle');
}

async function seedAptMirrorRepo(
  request: APIRequestContext,
  project: string,
  repo: string,
): Promise<{ project: string; repo: string }> {
  await request.post('/api/v1/projects', { data: { name: project } });
  await request.post(
    `/api/v1/projects/${encodeURIComponent(project)}/repos`,
    {
      data: {
        name: repo,
        type: 'deb',
        is_mirror: true,
        mirror_upstream_url: 'https://archive.ubuntu.com/ubuntu',
        mirror_filter: {
          Suites: ['focal'],
          Components: ['main'],
          Arches: ['amd64'],
        },
        scan_on_sync: false,
      },
    },
  );
  return { project, repo };
}

async function seedAptRegularRepo(
  request: APIRequestContext,
  project: string,
  repo: string,
): Promise<{ project: string; repo: string }> {
  await request.post('/api/v1/projects', { data: { name: project } });
  await request.post(
    `/api/v1/projects/${encodeURIComponent(project)}/repos`,
    { data: { name: repo, type: 'deb' } },
  );
  return { project, repo };
}

test.describe('Mirror config settings card', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('edit filter and save: PATCH body carries PascalCase mirror_filter', async ({
    page,
    request,
  }) => {
    const { project, repo } = await seedAptMirrorRepo(
      request,
      `mirror-settings-${Date.now()}`,
      'focal',
    );

    // Mock upstream-creds so the picker loads instantly.
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/upstream-creds/`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([]),
        }),
    );

    // Intercept the PATCH and capture the body.
    let capturedBody: Record<string, unknown> | null = null;
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/repos/deb/${encodeURIComponent(repo)}`,
      async (route) => {
        const req = route.request();
        if (req.method() === 'PATCH') {
          capturedBody = req.postDataJSON() as Record<string, unknown>;
          return route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({
              id: 1,
              project_id: 1,
              type: 'deb',
              name: repo,
              description_md: '',
              auto_scan: false,
              block_on_severity: 'none',
              public_read: false,
              size_bytes: 0,
              item_count: 0,
              created_at: new Date().toISOString(),
              is_mirror: true,
              mirror_upstream_url: 'https://archive.ubuntu.com/ubuntu',
              mirror_filter_json: JSON.stringify(
                (capturedBody as Record<string, unknown>).mirror_filter,
              ),
              mirror_cred_id: null,
              scan_on_sync: false,
            }),
          });
        }
        return route.fallback();
      },
    );

    await uiLoginAdmin(page);
    await page.goto(`/projects/${project}/deb/${repo}/settings`);

    // Mirror config card visible.
    await expect(page.getByText('Mirror config')).toBeVisible();

    // Add "universe" to Components by clicking its checkbox.
    await page
      .getByRole('checkbox', { name: 'universe' })
      .first()
      .click();

    // Save.
    await page.getByRole('button', { name: 'Save', exact: true }).click();

    // Give the mutation a tick to serialize.
    await expect(page.getByText(/saved/i)).toBeVisible({ timeout: 5000 });

    expect(capturedBody).not.toBeNull();
    const body = capturedBody as Record<string, unknown>;
    // The request body MUST NOT carry is_mirror or mirror_upstream_url
    // (those are immutable; sending them triggers a 400).
    expect(body.is_mirror).toBeUndefined();
    expect(body.mirror_upstream_url).toBeUndefined();
    const filter = body.mirror_filter as Record<string, unknown>;
    expect(filter).toBeTruthy();
    const components = filter.Components as string[];
    expect(components).toContain('main');
    expect(components).toContain('universe');
  });

  test('URL is readonly on /settings', async ({ page, request }) => {
    const { project, repo } = await seedAptMirrorRepo(
      request,
      `mirror-readonly-${Date.now()}`,
      'focal',
    );

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
    await page.goto(`/projects/${project}/deb/${repo}/settings`);

    await expect(page.getByText('Mirror config')).toBeVisible();

    // /settings no longer renders the Upstream URL
    // as an Input — the readonly guarantee is implemented by showing
    // the value as a CopyInline <code> block with "URL is immutable"
    // helper text. MirrorConfigSection is invoked with hideUrl=true
    // in RepoSettingsTab so the duplicate Input never renders.
    // "Upstream URL" appears in both the Label and downstream sections,
    // so match on the immutability helper text which is unique to
    // RepoSettingsTab's readonly-display path.
    await expect(
      page.getByText('URL is immutable — delete and recreate the repo to change.'),
    ).toBeVisible();
    // And confirm no editable input exists at the mirror-url id.
    await expect(page.locator('input#mirror-url')).toHaveCount(0);
  });

  test('non-mirror repo: Mirror config card is absent', async ({
    page,
    request,
  }) => {
    const { project, repo } = await seedAptRegularRepo(
      request,
      `mirror-absent-${Date.now()}`,
      'plain',
    );

    await uiLoginAdmin(page);
    await page.goto(`/projects/${project}/deb/${repo}/settings`);

    // Wait for the settings tab content to render — the "not a mirror"
    // fallback message is inside a Card that only mounts after the
    // /api/repo fetch resolves.
    await expect(page.getByText('not a mirror')).toBeVisible({
      timeout: 10_000,
    });
    // The CardTitle "Mirror config" only renders on the is_mirror
    // branch — scope to the CardTitle slot rather than any occurrence
    // of the text.
    await expect(
      page
        .locator('[data-slot="card-title"]')
        .filter({ hasText: /^Mirror config$/ }),
    ).toHaveCount(0);
  });
});

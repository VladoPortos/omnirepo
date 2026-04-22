/**
 * Phase 11 / plan 11-09 — Playwright coverage for creating a Git mirror
 * repo via CreateRepoDialog. Drives the widened MirrorConfigSection
 * (D-13: HTTPS+PAT only; D-07: no filter widget; D-14: passive LFS
 * warning) end-to-end.
 *
 * Scenarios:
 *   1. Git-mirror happy path: Git tab → Create Repository → tick mirror
 *      checkbox → assert LFS warning visible + Filters label absent →
 *      fill upstream URL + repo-name → submit. Asserts the captured POST
 *      body has type='git', is_mirror=true, and mirror_upstream_url set.
 *   2. Cred picker filters to kind='basic' for git: seed two upstream
 *      creds (one kind='basic', one kind='helm') and assert only the
 *      basic one appears in the picker for the Git mirror.
 *
 * NOT covered here (covered by backend tests in plan 11-05):
 *   - ssh:// upstream rejection envelope
 *   - LFS objects/batch 501 envelope
 *   - 403 receive-pack on push
 *
 * Auth bootstrap mirrors mirror-create.spec.ts verbatim.
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

test.describe('Create repo: Git mirror (Phase 11 / plan 11-09)', () => {
  test.beforeEach(async ({ request }) => {
    await bootstrapAdmin(request);
  });

  test('Git mirror happy path: LFS warning + no filter widget + POST body shape', async ({
    page,
    request,
  }) => {
    const project = await seedProject(request, `git-mirror-create-${Date.now()}`);

    // Mock the upstream-creds list with a single kind='basic' cred so
    // the picker has at least one option to render. Using mocks keeps
    // the spec independent of AEAD materialisation on the dev server.
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/upstream-creds/`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([
            {
              id: 11,
              host: 'github.com',
              kind: 'basic',
              username: 'mirrorbot',
              created_at: new Date().toISOString(),
              updated_at: new Date().toISOString(),
            },
          ]),
        }),
    );

    // Capture the actual POST and reply with a synthetic created repo.
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

    // Switch to the Git tab and open the create dialog. The Git tab
    // label appears as "Git" per ProjectDetailPage REPO_TYPES.
    await page.getByRole('tab', { name: /^Git/ }).click();
    await page
      .getByRole('button', { name: /Create Repository/i })
      .first()
      .click();

    const dialog = page.getByRole('dialog', { name: 'Create Repository' });
    await expect(dialog).toBeVisible();

    // Phase 11 / D-13: MirrorConfigSection now visible for type='git'
    // (it auto-selects 'git' because the Git tab pre-set the dialog
    // initial type via openCreateDialog).
    await expect(
      dialog.getByText('This repo is a mirror of an upstream'),
    ).toBeVisible();

    // Tick the mirror checkbox to reveal the upstream + cred + warning.
    await dialog
      .getByRole('checkbox', { name: 'This repo is a mirror of an upstream' })
      .click();

    // Phase 11 / D-14: passive LFS warning is visible under the URL
    // input when protocol === 'git'. The data-testid is the stable
    // selector contract for downstream specs.
    await expect(
      dialog.getByTestId('git-mirror-lfs-warning'),
    ).toBeVisible();
    await expect(dialog.getByTestId('git-mirror-lfs-warning')).toContainText(
      'Git LFS objects are not mirrored',
    );

    // Phase 11 / D-07: no Filters widget for git — assert the Filters
    // label is NOT in the dialog. (Other protocols always render it.)
    await expect(dialog.getByText('Filters', { exact: true })).toHaveCount(0);

    // The cred picker shows the seeded basic cred.
    const credPicker = dialog.locator('select#mirror-cred');
    await expect(credPicker).toBeVisible();
    await expect(credPicker.locator('option')).toHaveCount(2); // none + 1 basic
    // Value is the stringified cred id per MirrorConfigSection's option
    // render path.
    await credPicker.selectOption('11');

    // Fill upstream URL + repo-name and submit.
    await dialog
      .locator('input#mirror-url')
      .fill('https://github.com/example/mirror-target.git');
    await dialog.locator('input#repo-name').fill('mirrored-git');
    await dialog.getByRole('button', { name: /Create Repository/i }).click();

    // Dialog closes when the mutation resolves.
    await expect(dialog).not.toBeVisible({ timeout: 5000 });

    // Assert POST body shape per the wire contract in CreateRepoDialog.
    expect(capturedBody).not.toBeNull();
    const body = capturedBody as Record<string, unknown>;
    expect(body.name).toBe('mirrored-git');
    expect(body.type).toBe('git');
    expect(body.is_mirror).toBe(true);
    expect(body.mirror_upstream_url).toBe(
      'https://github.com/example/mirror-target.git',
    );
    expect(body.mirror_cred_id).toBe(11);
    // mirror_filter is sent as the empty AnyFilter object for git
    // (the filter widget is suppressed but the state default still
    // serialises). Backend tolerates {} per plan 11-05.
    expect(body.mirror_filter).toEqual({});
  });

  test('Git mirror cred picker filters to kind=basic only (D-13)', async ({
    page,
    request,
  }) => {
    const project = await seedProject(request, `git-mirror-creds-${Date.now()}`);

    // Two creds: one kind='basic' (should appear), one kind='helm'
    // (should NOT appear in the git picker per protocolCredKinds.git).
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/upstream-creds/`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([
            {
              id: 21,
              host: 'github.com',
              kind: 'basic',
              username: 'gitbot',
              created_at: new Date().toISOString(),
              updated_at: new Date().toISOString(),
            },
            {
              id: 22,
              host: 'charts.bitnami.com',
              kind: 'helm',
              username: 'helmbot',
              created_at: new Date().toISOString(),
              updated_at: new Date().toISOString(),
            },
          ]),
        }),
    );

    await uiLoginAdmin(page);
    await page.goto(`/projects/${encodeURIComponent(project)}`);

    await page.getByRole('tab', { name: /^Git/ }).click();
    await page
      .getByRole('button', { name: /Create Repository/i })
      .first()
      .click();

    const dialog = page.getByRole('dialog', { name: 'Create Repository' });
    await expect(dialog).toBeVisible();

    await dialog
      .getByRole('checkbox', { name: 'This repo is a mirror of an upstream' })
      .click();

    const credPicker = dialog.locator('select#mirror-cred');
    await expect(credPicker).toBeVisible();
    // Two options total: the "(none — anonymous access)" placeholder
    // plus the single kind='basic' cred. The kind='helm' cred MUST be
    // filtered out — protocolCredKinds.git === ['basic'].
    await expect(credPicker.locator('option')).toHaveCount(2);
    await expect(credPicker).toContainText('github.com');
    await expect(credPicker).not.toContainText('charts.bitnami.com');
  });
});

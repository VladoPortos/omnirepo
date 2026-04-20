/**
 * Phase 8 / plan 08-05 — Playwright coverage for the ProjectSettingsPage
 * Upstream credentials tab (MIRROR-22..24, T-08-05-01/03/08).
 *
 * Mocks the /upstream-creds/ endpoints so the spec is deterministic
 * regardless of whether AEAD is materialised on the dev server (the
 * backend routes are skipped when no AEAD key is present). Exercises:
 *
 *   1. Create-cred happy path: empty list -> EmptyState -> Add dialog ->
 *      POST -> table row appears. Assert the secret "secret123" is
 *      NEVER visible anywhere on the page.
 *   2. Edit preserves password when blank: cred exists -> Edit opens in
 *      edit mode with host / kind / username prefilled; password +
 *      token blank; help text "Leave password or token blank to keep
 *      the existing value." visible. Save -> PATCH body MUST NOT
 *      include `password` or `token` keys when both are blank.
 *   3. Delete confirmation + orphan note: Delete button opens
 *      confirm dialog with "mirror repo references this credential,
 *      its next sync will fail" copy. Confirm -> DELETE -> 204 ->
 *      empty state returns.
 *   4. Secrets never disclosed: after create, rescan the full DOM for
 *      the specific secret "secret123" — MUST NOT appear.
 *
 * Auth bootstrap mirrors empty-states.spec.ts + mirror-create.spec.ts
 * verbatim.
 */

import { expect, test, type APIRequestContext, type Page } from '@playwright/test';

test.use({ viewport: { width: 1440, height: 900 } });

const ADMIN_PW = 'AdminTest1!';
const SECRET = 'secret123';

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

interface MockedCred {
  id: number;
  host: string;
  kind: string;
  username: string;
  created_at: string;
  updated_at: string;
}

/**
 * installCredRoutes — installs Playwright route handlers backed by a
 * mutable in-memory list of creds. The list state lives on the
 * returned object so tests can inspect / seed / reset.
 */
async function installCredRoutes(
  page: Page,
  projectName: string,
): Promise<{
  state: { creds: MockedCred[]; lastPatchBody: Record<string, unknown> | null };
}> {
  const state = {
    creds: [] as MockedCred[],
    lastPatchBody: null as Record<string, unknown> | null,
  };
  const nextID = () =>
    state.creds.length === 0
      ? 1
      : Math.max(...state.creds.map((c) => c.id)) + 1;

  const encProj = encodeURIComponent(projectName);

  // LIST + CREATE on the collection URL.
  await page.route(
    `**/api/v1/projects/${encProj}/upstream-creds/`,
    async (route) => {
      const req = route.request();
      if (req.method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(state.creds),
        });
      }
      if (req.method() === 'POST') {
        const body = (req.postDataJSON() ?? {}) as Record<string, unknown>;
        const cred: MockedCred = {
          id: nextID(),
          host: String(body.host ?? ''),
          kind: String(body.kind ?? ''),
          username: String(body.username ?? ''),
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        };
        state.creds.push(cred);
        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify(cred),
        });
      }
      return route.fallback();
    },
  );

  // GET by id / PATCH / DELETE on the item URL.
  await page.route(
    new RegExp(`/api/v1/projects/${encProj}/upstream-creds/\\d+$`),
    async (route) => {
      const req = route.request();
      const url = req.url();
      const idMatch = /upstream-creds\/(\d+)$/.exec(url);
      const id = idMatch ? Number(idMatch[1]) : NaN;
      const idx = state.creds.findIndex((c) => c.id === id);
      if (req.method() === 'DELETE') {
        if (idx >= 0) state.creds.splice(idx, 1);
        return route.fulfill({ status: 204 });
      }
      if (req.method() === 'PATCH') {
        const body = (req.postDataJSON() ?? {}) as Record<string, unknown>;
        state.lastPatchBody = body;
        if (idx < 0) {
          return route.fulfill({
            status: 404,
            contentType: 'application/json',
            body: JSON.stringify({
              code: 'repo.not_found',
              message: 'upstream cred not found',
              class: 'validation',
            }),
          });
        }
        const cur = state.creds[idx];
        const updated: MockedCred = {
          ...cur,
          host: typeof body.host === 'string' ? body.host : cur.host,
          kind: typeof body.kind === 'string' ? body.kind : cur.kind,
          username:
            typeof body.username === 'string' ? body.username : cur.username,
          updated_at: new Date().toISOString(),
        };
        state.creds[idx] = updated;
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(updated),
        });
      }
      if (req.method() === 'GET') {
        if (idx < 0) {
          return route.fulfill({
            status: 404,
            contentType: 'application/json',
            body: JSON.stringify({
              code: 'repo.not_found',
              message: 'upstream cred not found',
              class: 'validation',
            }),
          });
        }
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(state.creds[idx]),
        });
      }
      return route.fallback();
    },
  );

  return { state };
}

test.describe('Upstream credentials tab (Phase 8 / plan 08-05)', () => {
  test.beforeEach(async ({ request }) => {
    await bootstrapAdmin(request);
  });

  test('create cred happy path: empty -> dialog -> row; secret never rendered', async ({
    page,
    request,
  }) => {
    const project = await seedProject(request, `ucreds-create-${Date.now()}`);
    await installCredRoutes(page, project);

    await uiLoginAdmin(page);
    await page.goto(`/projects/${encodeURIComponent(project)}/settings`);

    // EmptyState visible by default.
    await expect(page.getByTestId('empty-state')).toBeVisible();
    await expect(page.getByTestId('empty-state')).toContainText(
      'No upstream credentials',
    );

    // Click "Add credential" primary CTA inside the EmptyState.
    await page
      .getByTestId('empty-state')
      .getByRole('button', { name: /^Add credential$/i })
      .click();

    // Dialog opens in create mode.
    const dialog = page.getByRole('dialog', { name: 'Add upstream credential' });
    await expect(dialog).toBeVisible();

    // Fill: host archive.ubuntu.com, kind apt, username myuser, password secret123.
    await dialog.locator('input#cred-host').fill('archive.ubuntu.com');
    await dialog.locator('select#cred-kind').selectOption('apt');
    await dialog.locator('input#cred-username').fill('myuser');
    await dialog.locator('input#cred-password').fill(SECRET);

    await dialog.getByRole('button', { name: /^Add credential$/i }).click();

    // Dialog closes.
    await expect(dialog).not.toBeVisible({ timeout: 5000 });

    // Table row appears with host / kind / username.
    await expect(page.getByTestId('empty-state')).not.toBeVisible();
    await expect(page.getByRole('cell', { name: 'archive.ubuntu.com' })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'apt' })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'myuser' })).toBeVisible();

    // T-08-05-01: the password we typed is NEVER rendered back to DOM.
    await expect(page.getByText(SECRET)).not.toBeVisible();
    const fullBody = await page.content();
    expect(fullBody).not.toContain(SECRET);
  });

  test('edit preserves password when blank: PATCH body omits password/token keys', async ({
    page,
    request,
  }) => {
    const project = await seedProject(request, `ucreds-edit-${Date.now()}`);
    const { state } = await installCredRoutes(page, project);

    // Seed a cred directly into the mock store.
    state.creds.push({
      id: 1,
      host: 'archive.ubuntu.com',
      kind: 'apt',
      username: 'myuser',
      created_at: '2026-04-19T00:00:00Z',
      updated_at: '2026-04-19T00:00:00Z',
    });

    await uiLoginAdmin(page);
    await page.goto(`/projects/${encodeURIComponent(project)}/settings`);

    await expect(page.getByTestId('cred-row-1')).toBeVisible();
    await page
      .getByTestId('cred-row-1')
      .getByRole('button', { name: 'Edit' })
      .click();

    const dialog = page.getByRole('dialog', { name: 'Edit credential' });
    await expect(dialog).toBeVisible();

    // Host / kind / username prefilled.
    await expect(dialog.locator('input#cred-host')).toHaveValue(
      'archive.ubuntu.com',
    );
    await expect(dialog.locator('select#cred-kind')).toHaveValue('apt');
    await expect(dialog.locator('input#cred-username')).toHaveValue('myuser');

    // Password + token BLANK by contract.
    await expect(dialog.locator('input#cred-password')).toHaveValue('');
    await expect(dialog.locator('input#cred-token')).toHaveValue('');

    // Help text visible.
    await expect(
      dialog.getByText(
        'Leave password or token blank to keep the existing value.',
      ),
    ).toBeVisible();

    // Change username; leave password + token blank.
    await dialog.locator('input#cred-username').fill('myuser2');
    await dialog.getByRole('button', { name: /^Save changes$/i }).click();

    await expect(dialog).not.toBeVisible({ timeout: 5000 });

    // T-08-05-03: captured PATCH body does NOT carry `password` or `token`.
    expect(state.lastPatchBody).not.toBeNull();
    const body = state.lastPatchBody as Record<string, unknown>;
    expect(Object.prototype.hasOwnProperty.call(body, 'password')).toBe(false);
    expect(Object.prototype.hasOwnProperty.call(body, 'token')).toBe(false);
    expect(body.username).toBe('myuser2');

    await expect(page.getByRole('cell', { name: 'myuser2' })).toBeVisible();
  });

  test('delete confirmation + orphan warning + list refetch', async ({
    page,
    request,
  }) => {
    const project = await seedProject(request, `ucreds-delete-${Date.now()}`);
    const { state } = await installCredRoutes(page, project);

    state.creds.push({
      id: 1,
      host: 'archive.ubuntu.com',
      kind: 'apt',
      username: 'myuser',
      created_at: '2026-04-19T00:00:00Z',
      updated_at: '2026-04-19T00:00:00Z',
    });

    await uiLoginAdmin(page);
    await page.goto(`/projects/${encodeURIComponent(project)}/settings`);

    await expect(page.getByTestId('cred-row-1')).toBeVisible();

    await page
      .getByTestId('cred-row-1')
      .getByRole('button', { name: 'Delete' })
      .click();

    // Confirmation dialog with mirror-orphan warning.
    const confirm = page.getByRole('dialog', { name: 'Delete credential' });
    await expect(confirm).toBeVisible();
    await expect(confirm).toContainText('Delete credential for');
    await expect(confirm).toContainText('archive.ubuntu.com');
    await expect(confirm).toContainText('its next sync will fail');

    await confirm.getByRole('button', { name: /^Delete$/ }).click();

    await expect(confirm).not.toBeVisible({ timeout: 5000 });

    // Row gone; EmptyState back.
    await expect(page.getByTestId('cred-row-1')).not.toBeVisible();
    await expect(page.getByTestId('empty-state')).toBeVisible();
    await expect(page.getByTestId('empty-state')).toContainText(
      'No upstream credentials',
    );

    expect(state.creds.length).toBe(0);
  });

  // POLISH-02 regression: the CRED_KINDS dropdown was 6 entries pre-9.
  // After POLISH-02 it must be exactly 5 with no "deb alias" row.
  test('cred-kind dropdown has 5 entries and no "deb alias" row', async ({
    page,
    request,
  }) => {
    const project = await seedProject(request, `ucreds-dropdown-${Date.now()}`);
    await installCredRoutes(page, project);

    await uiLoginAdmin(page);
    await page.goto(`/projects/${encodeURIComponent(project)}/settings`);

    // Open the Add-credential dialog from the EmptyState.
    await expect(page.getByTestId('empty-state')).toBeVisible();
    await page
      .getByTestId('empty-state')
      .getByRole('button', { name: /^Add credential$/i })
      .click();

    const dialog = page.getByRole('dialog', { name: 'Add upstream credential' });
    await expect(dialog).toBeVisible();

    const dropdown = dialog.locator('select#cred-kind');
    await expect(dropdown.locator('option')).toHaveCount(5);

    // Sorted value set must equal the canonical five.
    const values = await dropdown
      .locator('option')
      .evaluateAll((opts) =>
        opts.map((o) => (o as HTMLOptionElement).value),
      );
    expect(values.slice().sort()).toEqual(
      ['apt', 'docker', 'helm', 'pypi', 'rpm'],
    );

    // Negative regression: no "deb alias" label anywhere in the options.
    await expect(
      dropdown.locator('option', { hasText: /deb alias/ }),
    ).toHaveCount(0);
  });

  test('secrets never disclosed: full DOM scan; secret123 never appears', async ({
    page,
    request,
  }) => {
    const project = await seedProject(request, `ucreds-secret-${Date.now()}`);
    const { state } = await installCredRoutes(page, project);

    state.creds.push({
      id: 1,
      host: 'ghcr.io',
      kind: 'docker',
      username: 'octocat',
      created_at: '2026-04-19T00:00:00Z',
      updated_at: '2026-04-19T00:00:00Z',
    });

    await uiLoginAdmin(page);
    await page.goto(`/projects/${encodeURIComponent(project)}/settings`);

    await expect(page.getByTestId('cred-row-1')).toBeVisible();

    await page
      .getByTestId('cred-row-1')
      .getByRole('button', { name: 'Edit' })
      .click();

    const dialog = page.getByRole('dialog', { name: 'Edit credential' });
    await expect(dialog.locator('input#cred-password')).toHaveValue('');
    await expect(dialog.locator('input#cred-token')).toHaveValue('');

    // T-08-05-01 DEFENSIVE: secret123 is never anywhere in the rendered DOM.
    await expect(page.getByText(SECRET)).not.toBeVisible();
    const pageHtml = await page.content();
    expect(pageHtml).not.toContain(SECRET);
  });
});

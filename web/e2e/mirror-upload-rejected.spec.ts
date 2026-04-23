/**
 * Phase 8 Plan 06 / M6.8 — mirror-upload-rejected Playwright coverage.
 *
 * Two orthogonal assertions:
 *
 *   1. Backend integration: a PUT against the REAL APT upload route
 *      `/{project}/deb/{repo}/pool/<filename>.deb` on an is_mirror=true
 *      repo returns 403 with envelope body carrying code=repo_is_mirror.
 *      This exercises internal/httpx/mirror_guard.go MirrorGuardFixed as
 *      wired at internal/protocol/deb/handler.go:146.
 *
 *      Notes on route shape (confirmed against internal/protocol/deb/handler.go
 *      lines 136-150): NO /api/v1/ prefix, NO /upload suffix, wildcard
 *      accepts any pool-relative filename (including pool/<letter>/<pkg>/
 *      <file>.deb), and the guard fires on PUT + DELETE uniformly.
 *
 *   2. UI rendering: stub POST /sync on the same mirror repo to return
 *      the 403 envelope and assert the page surfaces the operator-facing
 *      message ("writes to mirror repos are disabled") via
 *      ErrorEnvelopeRenderer so a real backend 403 reaches the user.
 *
 * Why the real APT route and not /api/v1/: the APT upload surface is
 * nested directly under the repo's public URL (what `apt-get` would hit
 * on a legitimate install). The MirrorGuard sits on exactly that path
 * so the rejection is visible to the same HTTP clients the protocol
 * serves in production.
 */

import { expect, test, type APIRequestContext, type Page } from '@playwright/test';

test.use({ viewport: { width: 1440, height: 900 } });

const ADMIN_PW = 'AdminTest1!';

async function bootstrapAdmin(request: APIRequestContext): Promise<void> {
  const first = await request.post('/api/v1/auth/login', {
    data: { login: 'admin', password: 'AdminTest1!' },
  });
  if (first.ok()) {
    const body = await first.json();
    if (body.must_change_password) {
      await request.post('/api/v1/auth/change-password', {
        data: { current: 'AdminTest1!', new: ADMIN_PW },
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
): Promise<void> {
  await request.post('/api/v1/projects', { data: { name } });
}

async function seedMirrorRepo(
  request: APIRequestContext,
  project: string,
  repo: string,
): Promise<void> {
  await request.post(`/api/v1/projects/${encodeURIComponent(project)}/repos`, {
    data: {
      type: 'deb',
      name: repo,
      is_mirror: true,
      mirror_upstream_url: 'https://archive.ubuntu.com/ubuntu',
      mirror_filter: { Suites: ['focal'], Components: ['main'], Arches: ['amd64'] },
      scan_on_sync: false,
    },
  });
}

test.describe('Mirror upload rejection (Phase 8 / plan 08-06)', () => {
  test.beforeEach(async ({ request }) => {
    await bootstrapAdmin(request);
  });

  test('APT push to mirror repo returns 403 envelope with code=repo_is_mirror', async ({
    page,
    request,
  }) => {
    const project = `mirror-upload-${Date.now()}`;
    const repo = 'focal-mirror';
    await seedProject(request, project);
    await seedMirrorRepo(request, project, repo);
    // Drive the UI session so page.request (page-scoped APIRequestContext)
    // carries the same session cookies Playwright would use from the
    // browser for a real operator action.
    await uiLoginAdmin(page);

    // Hit the REAL APT upload route. Verified at read-time against
    // internal/protocol/deb/handler.go:147 — this is PUT /{project}/deb/{repo}/pool/*
    // (no /api/v1/ prefix, no /upload suffix). The wildcard accepts the
    // pool-layout filename Debian packages expect.
    //
    // The DEB PUT handler uses BasicOrAPIKey auth (NOT SessionOrAPIKey),
    // so the page's session cookie doesn't authenticate the request —
    // it'd return 401 before the MirrorGuard fires. Supply HTTP Basic
    // explicitly so we exercise the guard path (F-15.4).
    const basicAuth = Buffer.from(`admin:${ADMIN_PW}`).toString('base64');
    const resp = await page.request.put(
      `/${encodeURIComponent(project)}/deb/${encodeURIComponent(repo)}/pool/h/hello/hello_1.0_amd64.deb?suite=focal&component=main`,
      {
        data: Buffer.from('fake-deb-bytes'),
        headers: {
          'Content-Type': 'application/octet-stream',
          Authorization: `Basic ${basicAuth}`,
        },
      },
    );
    expect(resp.status()).toBe(403);
    const body = await resp.json();
    // Envelope code is the dotted repo.repo_is_mirror form; the plan-check
    // grep + assertions rely on the substring "repo_is_mirror" resolving.
    expect(body.code).toContain('repo_is_mirror');
  });

  test('UI renders ErrorEnvelopeRenderer when mocked 403 repo_is_mirror comes back from /sync', async ({
    page,
    request,
  }) => {
    const project = `mirror-upload-ui-${Date.now()}`;
    const repo = 'focal-mirror-ui';
    await seedProject(request, project);
    await seedMirrorRepo(request, project, repo);

    // Stub /sync to return a 403 repo_is_mirror envelope. Exercises the
    // ErrorEnvelopeRenderer wiring inside SyncNowButton's mutationError
    // branch. In production a mirror repo's /sync path returns 202 (or
    // 409 if another sync is running); the stub forces the error path so
    // the Playwright run can verify the renderer gets the right envelope
    // without racing against real sync timing.
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/repos/deb/${encodeURIComponent(repo)}/sync`,
      (route) =>
        route.fulfill({
          status: 403,
          contentType: 'application/json',
          body: JSON.stringify({
            code: 'repo.repo_is_mirror',
            message: 'writes to mirror repos are disabled (uploads + deletes)',
            class: 'permission',
          }),
        }),
    );

    await uiLoginAdmin(page);
    await page.goto(
      `/projects/${encodeURIComponent(project)}/deb/${encodeURIComponent(repo)}`,
    );

    // Click Sync now to trigger the stubbed 403.
    await page.getByRole('button', { name: /sync now/i }).click();

    // ErrorEnvelopeRenderer surfaces the operator-facing message.
    await expect(
      page.getByText(/writes to mirror repos are disabled/i),
    ).toBeVisible();
    // data-envelope-class hook pins the permission class → confirms the
    // envelope went through ErrorEnvelopeRenderer rather than a plain
    // toast.
    await expect(
      page.locator('[data-envelope-class="permission"]').first(),
    ).toBeVisible();
  });
});

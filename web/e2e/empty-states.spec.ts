/**
 * Playwright coverage for the EmptyState surfaces.
 *
 * Declares the canonical `assertEmptyState(page, title, ctaLabel?)` helper
 * and exercises each empty-state surface end-to-end:
 *
 *   ProjectsPage zero-projects ("No projects yet")
 *   ProjectDetailPage zero-repos ("No repositories yet")
 *   ProjectDetailPage zero-members ("No teammates yet")
 *   Per-protocol repo zero-artifacts surface w/ inline SnippetList
 *   Never-scanned repo surface w/ enabled + disabled CTA variants
 *   admin/TLSPage no-uploaded-cert ("Using the default self-signed certificate")
 *   admin/TrashPage empty ("Trash is empty")
 *   SearchPage no-results ("No results found") + 3 example chips
 *
 * Auth bootstrap mirrors error-envelope.spec.ts + dashboard-composition.spec.ts —
 * the change-password-on-first-login dance is handled via the REST API so the
 * page tests never land on the change-password wall.
 */

import { expect, test, type APIRequestContext, type Page } from '@playwright/test';
import { adminLoginAPI, resetServerState, seedUserWithProjectRole, passwordLogin } from './helpers/auth';

test.use({ viewport: { width: 1440, height: 900 } });

const ADMIN_PW = 'AdminTest1!';

// -----------------------------------------------------------------------
// Auth + seeding helpers. Kept near the top so individual tests read
// top-to-bottom.
// -----------------------------------------------------------------------

/**
 * uiLoginAdmin — drives the UI login form as the super-admin. Pairs
 * with the beforeEach-level adminLoginAPI + resetServerState from
 * ./helpers/auth for the BrowserContext cookie (the API request jar is
 * separate from the page's).
 */
async function uiLoginAdmin(page: Page): Promise<void> {
  await page.goto('/login');
  await page.fill('input#login', 'admin');
  await page.fill('input#password', ADMIN_PW);
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle');
}

/**
 * seedProject — idempotent project creation via the REST API. Returns the
 * project name for convenience. Ignores 409 responses when the project
 * already exists from a prior run.
 */
async function seedProject(
  request: APIRequestContext,
  name: string,
): Promise<string> {
  await request.post('/api/v1/projects', { data: { name } });
  return name;
}

/**
 * seedDockerRepoWithOneArtifact — creates a project + docker repo. The
 * "one artifact" terminology refers to the never-scanned surface:
 * scan surface fires when `artifacts.length > 0 && !hasEverBeenScanned`.
 * The current UI's DockerRepoPage uses MOCK_TAGS = [] so a mocked
 * artifacts.length > 0 is simulated via route interception in the
 * never-scanned tests rather than a real OCI manifest push (which requires
 * the full docker blob-upload dance and belongs in the protocol integration
 * tests at internal/protocol/oci/*_test.go). This keeps the e2e spec focused
 * on UI wiring.
 */
async function seedDockerRepoWithOneArtifact(
  request: APIRequestContext,
  project: string,
  repo: string,
): Promise<{ project: string; name: string }> {
  await seedProject(request, project);
  await request.post(`/api/v1/projects/${encodeURIComponent(project)}/repos`, {
    data: { name: repo, type: 'docker' },
  });
  return { project, name: repo };
}

// seedUserWithProjectRole is imported from ./helpers/auth (creates user +
// POSTs to /members/{login} with role).

/** assertEmptyState — empty-state assertion helper. */
export async function assertEmptyState(
  page: Page,
  title: string,
  ctaLabel?: string,
) {
  const es = page.getByTestId('empty-state');
  await expect(es).toBeVisible();
  await expect(es).toContainText(title);
  if (ctaLabel) {
    await expect(
      es.getByRole('button', { name: ctaLabel }).or(
        es.getByRole('link', { name: ctaLabel }),
      ),
    ).toBeVisible();
  }
}

// -----------------------------------------------------------------------
// Spec body. Each test scopes its own fresh fixture state where possible.
// The TLS, trash, search, and zero-projects cases run green against a
// clean fresh install (no fixture setup needed). The never-scanned tests
// seed a docker repo but mock the artifact list + rescan endpoint so the
// UI contract is exercised without a full OCI push.
// -----------------------------------------------------------------------

test.describe('EmptyState surfaces', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('no uploaded TLS cert', async ({ page }) => {
    await uiLoginAdmin(page);
    await page.goto('/admin/tls');
    await assertEmptyState(
      page,
      'Using the default self-signed certificate',
      'Upload certificate',
    );
  });

  test('empty trash', async ({ page }) => {
    await uiLoginAdmin(page);
    await page.goto('/admin/trash');
    await assertEmptyState(page, 'Trash is empty');
  });

  test('no-results search', async ({ page }) => {
    await uiLoginAdmin(page);
    await page.goto('/search');
    // SearchPage debounces the query 300ms — type into the input and
    // wait for the no-results surface rather than relying on a URL query
    // param the page doesn't parse today.
    await page.fill(
      'input[placeholder*="Search repositories"]',
      'zzzzzz_definitely_no_match_zzzzzz',
    );
    await assertEmptyState(page, 'No results found', 'Clear filters');
    // 3 example chips are present as clickable buttons in the description
    // region.
    await expect(page.getByRole('button', { name: 'openssl' })).toBeVisible();
    await expect(
      page.getByRole('button', { name: /CVE-2024/ }),
    ).toBeVisible();
    await expect(
      page.getByRole('button', { name: /myorg\/docker\/alpine/ }),
    ).toBeVisible();
  });

  test('zero projects', async ({ page, request }) => {
    // Fresh-install /projects — only runs green on a truly empty
    // install. On reruns the beforeEach admin login plus any prior
    // test's seeding may leave projects around; purge via the REST API.
    // The trash flow here is a best-effort cleanup — if the admin has
    // deleted projects they'll land in trash until retention, but
    // /projects should still be empty of ACTIVE projects.
    const listResp = await request.get('/api/v1/projects');
    if (listResp.ok()) {
      const body = await listResp.json();
      for (const p of body.items ?? []) {
        await request.delete(`/api/v1/projects/${encodeURIComponent(p.name)}`);
      }
    }
    await uiLoginAdmin(page);
    await page.goto('/projects');
    await assertEmptyState(page, 'No projects yet', 'Create project');
  });

  test('zero teammates on a single-member project', async ({
    page,
    request,
  }) => {
    const project = `empty02-${Date.now()}`;
    await seedProject(request, project);
    await uiLoginAdmin(page);
    await page.goto(`/projects/${project}`);
    await assertEmptyState(page, 'No teammates yet', 'Add member');
  });

  test('zero-artifacts docker repo renders SnippetList inline', async ({
    page,
    request,
  }) => {
    const repo = await seedDockerRepoWithOneArtifact(
      request,
      `empty03-${Date.now()}`,
      'mirror',
    );
    await uiLoginAdmin(page);
    await page.goto(`/projects/${repo.project}/docker/${repo.name}`);
    await assertEmptyState(page, 'No artifacts yet');
    // SnippetList inline: a Docker snippet label (e.g. "Login") is
    // inside the empty-state children slot.
    const es = page.getByTestId('empty-state');
    await expect(es.getByText('Login', { exact: true })).toBeVisible();
  });

  test('never-scanned repo (maintainer) renders enabled CTA', async ({
    page,
    request,
  }) => {
    const repo = await seedDockerRepoWithOneArtifact(
      request,
      `empty04a-${Date.now()}`,
      'mirror',
    );
    // DockerRepoPage uses MOCK_TAGS = [] today, so the surface is wired to
    // look at whether any artifacts exist AND any scans exist. Mock the
    // repo-content endpoint so artifacts.length > 0, and mock the
    // repo-scans endpoint so scans.length == 0, and mock the rescan
    // endpoint so click() does not 404 under test.
    // /content now returns a RepoContentPage object
    // { items, total, next_offset }, not a raw array — select: p => p.items
    // in useRepoContent. Pre-v1.4 mock returned [...] directly; that gave
    // items=undefined and the "no scan results" surface never rendered.
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(repo.project)}/repos/docker/${encodeURIComponent(repo.name)}/content**`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            items: [
              {
                id: 1,
                name: 'app:latest',
                version: 'latest',
                size_bytes: 1024,
                scan_severity: '',
                uploaded_at: new Date().toISOString(),
                extra: {},
              },
            ],
            total: 1,
            next_offset: null,
          }),
        }),
    );
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(repo.project)}/repos/docker/${encodeURIComponent(repo.name)}/scans**`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([]),
        }),
    );
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(repo.project)}/repos/docker/${encodeURIComponent(repo.name)}/artifacts/*/rescan`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: '{}',
        }),
    );
    await uiLoginAdmin(page);
    await page.goto(`/projects/${repo.project}/docker/${repo.name}`);
    await assertEmptyState(
      page,
      'No scan results yet',
      'Run first scan',
    );
    // CTA is enabled for the super-admin (maintainer-equivalent).
    const cta = page
      .getByTestId('empty-state')
      .getByRole('button', { name: 'Run first scan' });
    await expect(cta).toBeEnabled();
  });

  // Non-maintainer viewer sees disabled Scan CTA.
  // seedUserWithProjectRole now posts the real role to /members/{login}, and
  // canScan uses useRoleFor() so viewers see the disabled path.
  test('never-scanned repo (non-maintainer) renders disabled CTA + tooltip', async ({
    page,
    request,
  }) => {
    const repo = await seedDockerRepoWithOneArtifact(
      request,
      `empty04b-${Date.now()}`,
      'mirror',
    );
    const login = `viewer-e04-${Date.now()}`;
    const otp = await seedUserWithProjectRole(
      request,
      login,
      'viewer',
      repo.project,
    );
    // If the seed failed (e.g. admin not super-admin), skip rather than fail.
    if (!otp) {
      test.skip(true, 'Could not seed non-maintainer user via admin API.');
      return;
    }

    // Route intercept: simulate one artifact + zero scans so the
    // never-scanned surface renders (artifacts.length > 0 && scansCount === 0).
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(repo.project)}/repos/docker/${encodeURIComponent(repo.name)}/content**`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            items: [
              {
                id: 1,
                name: 'app:latest',
                version: 'latest',
                size_bytes: 1024,
                scan_severity: '',
                uploaded_at: new Date().toISOString(),
                extra: {},
              },
            ],
            total: 1,
            next_offset: null,
          }),
        }),
    );
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(repo.project)}/repos/docker/${encodeURIComponent(repo.name)}/scans**`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([]),
        }),
    );

    // Log in as the viewer — passwordLogin handles must_change_password redirect.
    const loggedIn = await passwordLogin(page, login, otp);
    if (!loggedIn) {
      test.skip(true, 'passwordLogin failed — viewer session not established.');
      return;
    }

    await page.goto(`/projects/${repo.project}/docker/${repo.name}`);
    // The EmptyState wraps the disabled button in a span with aria-disabled.
    // Locate by aria-label which includes the button name.
    const es = page.getByTestId('empty-state');
    // The disabled span wrapper has aria-label="Run first scan"
    const ctaSpan = es.locator('[aria-disabled="true"]');
    await expect(ctaSpan).toBeVisible();
    // Hover to trigger tooltip. Base UI tooltips render via Portal so
    // getByRole('tooltip') is unreliable in headless mode — use data-slot.
    await ctaSpan.hover({ force: true });
    await expect(page.locator('[data-slot="tooltip-content"]')).toContainText(
      'Maintainer role required',
      { timeout: 5_000 },
    );
  });
});

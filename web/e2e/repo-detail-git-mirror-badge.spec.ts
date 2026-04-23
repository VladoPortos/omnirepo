/**
 * Phase 11 / plan 11-10 — Playwright coverage for the Read-only mirror
 * badge + push-instruction hiding on the Git repo detail page
 * (GITMIRROR-03 / D-09).
 *
 * Six scenarios condensed into two test cases:
 *
 *   1. Mirror Git repo detail page:
 *        - "Read-only mirror" StatusBadge visible (warning variant).
 *        - "Sync now" button visible (parallel to Helm/Apt/Rpm/Pypi).
 *        - Clone URL `<code>` remains visible (read path preserved).
 *        - The push-hint block ("git config credential.helper store")
 *          from the non-mirror snippet is NOT rendered.
 *        - The mirror-specific empty-state copy is rendered
 *          ("Mirror is empty").
 *
 *   2. Non-mirror (dev) Git repo detail page:
 *        - "Read-only mirror" text is absent.
 *        - "Sync now" button is absent.
 *        - The push snippet is visible (the non-mirror empty-state
 *          surface keeps the v1.3 behaviour verbatim).
 *
 * The mirror case uses page.route to mock /repos/git/{repo} and /refs
 * because the backend's mirror-create path performs a real go-git clone
 * against the upstream URL which fails instantly in the offline e2e
 * environment. Mocking the detail + refs GETs lets us exercise the UI
 * branches without a working upstream — this is identical to how
 * mirror-sync-now.spec.ts mocks /sync + /sync-jobs.
 *
 * The dev case uses a real POST /repos to confirm the zero-regression
 * invariant: non-mirror git repos render EXACTLY as they did in v1.3.
 *
 * Auth bootstrap mirrors create-repo-git-mirror.spec.ts verbatim.
 */

import {
  expect,
  test,
  type APIRequestContext,
  type Page,
} from '@playwright/test';
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

async function seedProject(
  request: APIRequestContext,
  name: string,
): Promise<string> {
  await request.post('/api/v1/projects', { data: { name } });
  return name;
}

/** Build the Repo JSON the UI receives from GET /repos/git/{repo}. The
 *  field shape mirrors internal/api/repos.go:repoResponse exactly
 *  (same fields the create-repo-git-mirror.spec.ts mock synthesises). */
function mockRepoPayload(opts: {
  name: string;
  isMirror: boolean;
  upstreamUrl?: string;
}): Record<string, unknown> {
  const now = new Date().toISOString();
  return {
    id: 101,
    project_id: 1,
    type: 'git',
    name: opts.name,
    description_md: '',
    auto_scan: false,
    block_on_severity: 'none',
    public_read: false,
    size_bytes: 0,
    item_count: 0,
    created_at: now,
    is_mirror: opts.isMirror,
    mirror_upstream_url: opts.upstreamUrl ?? '',
    mirror_filter_json: opts.isMirror ? '{}' : '',
    mirror_cred_id: null,
    scan_on_sync: false,
  };
}

test.describe('Repo detail: Git mirror badge + push hide (Phase 11 / plan 11-10)', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('Git mirror: badge visible, push snippet hidden, Sync now present', async ({
    page,
    request,
  }) => {
    const project = await seedProject(
      request,
      `git-mirror-badge-${Date.now()}`,
    );
    const repoName = 'mirrored-git';

    // Mock GET /projects/{p}/repos/git/{repo} → is_mirror=true so the
    // UI renders the mirror branch without the backend actually having
    // a mirrored repo stored (see file-header note).
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/repos/git/${encodeURIComponent(repoName)}`,
      (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(
            mockRepoPayload({
              name: repoName,
              isMirror: true,
              upstreamUrl: 'https://github.com/example/mirror-target.git',
            }),
          ),
        });
      },
    );

    // Mock GET /refs → empty so we exercise the empty-state branch
    // (where the mirror vs dev copy divergence is sharpest). GitRepoPage
    // renders the "Mirror is empty" EmptyState and the Sync Now button
    // in this branch; push snippet must NOT render.
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/repos/git/${encodeURIComponent(repoName)}/refs`,
      (route) => {
        if (route.request().method() !== 'GET') return route.fallback();
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ items: [] }),
        });
      },
    );

    await uiLoginAdmin(page);
    await page.goto(
      `/projects/${encodeURIComponent(project)}/git/${encodeURIComponent(repoName)}`,
    );

    // 1. "Read-only mirror" StatusBadge is visible.
    const badge = page.getByText('Read-only mirror', { exact: true });
    await expect(badge).toBeVisible();

    // 2. "Sync now" button is visible (SyncNowButton in the mirror
    //    empty-state branch).
    await expect(
      page.getByRole('button', { name: 'Sync now', exact: true }),
    ).toBeVisible();

    // 3. Clone URL is visible (the `<code>` element inside the clone
    //    URL bar). The exact shape is
    //    `https://{host}/{project}/git/{repo}.git` — assert the tail
    //    because host varies per test environment.
    await expect(
      page.locator('code', {
        hasText: `/${project}/git/${repoName}.git`,
      }),
    ).toBeVisible();

    // 4. Mirror-specific empty-state copy is present.
    await expect(page.getByText('Mirror is empty')).toBeVisible();

    // 5. Push snippet must NOT be visible. The non-mirror empty state
    //    renders the "Authenticate" SnippetList entry which contains
    //    "git config credential.helper store" — asserting that block
    //    is absent is the strongest available signal that the push
    //    path is suppressed.
    await expect(
      page.getByText('credential.helper store', { exact: false }),
    ).toHaveCount(0);

    // 6. The non-mirror EmptyState copy ("No artifacts yet" / "Upload
    //    your first artifact") must also be absent — the mirror branch
    //    replaces it entirely with "Mirror is empty".
    await expect(page.getByText('No artifacts yet')).toHaveCount(0);
  });

  test('Git dev repo: no badge, no Sync now, push snippet visible', async ({
    page,
    request,
  }) => {
    const project = await seedProject(
      request,
      `git-dev-badge-${Date.now()}`,
    );
    const repoName = 'dev-git';

    // Real create — non-mirror git repo with no refs yet. No mocks: we
    // want the exact v1.3 rendering path to confirm the zero-regression
    // invariant (INV-11-10-03).
    const createResp = await request.post(
      `/api/v1/projects/${encodeURIComponent(project)}/repos`,
      { data: { name: repoName, type: 'git' } },
    );
    expect(createResp.ok()).toBeTruthy();

    await uiLoginAdmin(page);
    await page.goto(
      `/projects/${encodeURIComponent(project)}/git/${encodeURIComponent(repoName)}`,
    );

    // 1. "Read-only mirror" text MUST be absent.
    await expect(
      page.getByText('Read-only mirror', { exact: true }),
    ).toHaveCount(0);

    // 2. "Sync now" button MUST be absent (non-mirror repos don't sync).
    await expect(
      page.getByRole('button', { name: 'Sync now', exact: true }),
    ).toHaveCount(0);

    // 3. The non-mirror empty state renders "No artifacts yet".
    await expect(page.getByText('No artifacts yet')).toBeVisible();

    // 4. The push snippet IS visible — "Authenticate" label plus the
    //    credential-helper hint.
    await expect(page.getByText('Authenticate', { exact: true })).toBeVisible();
    await expect(
      page.getByText('credential.helper store', { exact: false }),
    ).toBeVisible();

    // 5. The "Clone" snippet label is also visible (clone path works
    //    on both mirror and dev repos — regression guard).
    await expect(page.getByText('Clone', { exact: true })).toBeVisible();
  });
});

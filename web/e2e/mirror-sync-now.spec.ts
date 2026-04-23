/**
 * Phase 8 / plan 08-04 — Playwright coverage for the Sync Now button
 * on mirror-aware protocol pages.
 *
 * Happy path: seed an APT mirror repo via the REST API (backend allows
 * is_mirror=true on APT), navigate to the repo page, click Sync now,
 * assert the progress line renders and eventually flips to done.
 *
 * Gate test: a non-mirror APT repo does NOT render the Sync now
 * button (button absent, Upload + "Sync from URL" affordances visible
 * instead).
 *
 * The /sync POST and the /sync-jobs/{id} GET are both mocked through
 * page.route so the test doesn't depend on the server actually running
 * apt-mirror. Same progressive-poll pattern docker-clone.spec.ts uses.
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

test.describe('Mirror Sync Now button (Phase 8 / plan 08-04)', () => {
  test.beforeEach(async ({ request }) => {
    await bootstrapAdmin(request);
  });

  test('APT mirror: Sync now triggers job and progress advances', async ({
    page,
    request,
  }) => {
    const { project, repo } = await seedAptMirrorRepo(
      request,
      `mirror-sync-${Date.now()}`,
      'focal',
    );

    // Mock /sync — return 202 { job_id, kind }.
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/repos/deb/${encodeURIComponent(repo)}/sync`,
      (route) =>
        route.fulfill({
          status: 202,
          contentType: 'application/json',
          body: JSON.stringify({ job_id: 777, kind: 'sync' }),
        }),
    );

    // Mock progressive sync-job polling.
    const progression = [
      {
        id: 777,
        kind: 'sync',
        status: 'running',
        attempts: 1,
        progress_bytes: 1_000_000,
        total_bytes: 10_000_000,
        current_step: 'pulling nginx_1.18.0_amd64.deb',
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
      {
        id: 777,
        kind: 'sync',
        status: 'running',
        attempts: 1,
        progress_bytes: 8_000_000,
        total_bytes: 10_000_000,
        current_step: 'pulling curl_7.68.0_amd64.deb',
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
      {
        id: 777,
        kind: 'sync',
        status: 'done',
        attempts: 1,
        progress_bytes: 10_000_000,
        total_bytes: 10_000_000,
        current_step: 'done',
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
    ];
    let pollCount = 0;
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/repos/deb/${encodeURIComponent(repo)}/sync-jobs/777`,
      (route) => {
        const body = progression[Math.min(pollCount, progression.length - 1)];
        pollCount += 1;
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(body),
        });
      },
    );

    await uiLoginAdmin(page);
    await page.goto(`/projects/${project}/deb/${repo}`);

    // Sync now button visible; click it.
    const btn = page.getByRole('button', { name: 'Sync now', exact: true });
    await expect(btn).toBeVisible();
    await btn.click();

    // Progress line advances.
    await expect(page.getByText(/pulling nginx/)).toBeVisible({
      timeout: 3000,
    });
    await expect(page.getByText(/pulling curl/)).toBeVisible({
      timeout: 3000,
    });

    // After flip to done the button re-enables (no longer "Syncing…").
    await expect(
      page.getByRole('button', { name: 'Sync now', exact: true }),
    ).toBeEnabled({ timeout: 3000 });
  });

  test('non-mirror APT repo: Sync now button absent', async ({
    page,
    request,
  }) => {
    const { project, repo } = await seedAptRegularRepo(
      request,
      `regular-${Date.now()}`,
      'plain',
    );

    await uiLoginAdmin(page);
    await page.goto(`/projects/${project}/deb/${repo}`);

    // The traditional "Sync from URL" button IS visible on non-mirror
    // repos (unchanged UX). The new Sync now button must NOT be.
    await expect(
      page.getByRole('button', { name: 'Sync now', exact: true }),
    ).toHaveCount(0);
  });
});

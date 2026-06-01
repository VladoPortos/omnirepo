/**
 * Playwright coverage for the Docker clone modal.
 *
 * Drives the full 3-state flow (form → progress → result) against a
 * mocked backend:
 *
 *   - page.route('**\/pull-external', ...) stubs 202 { job_id: 999 }.
 *   - page.route('**\/sync-jobs/999', ...) stubs a progression of
 *     responses across successive polls: layer 3 of 7 → layer 5 of 7
 *     → done. The hook polls every 500 ms; we assert the progress
 *     text advances then flips to the success surface.
 *
 * A second test asserts the failure path — the job flips to status=
 * "failed" with last_error, the dialog renders ErrorEnvelopeRenderer,
 * the Retry button exists and resets to the form phase.
 *
 * Auth bootstrap mirrors empty-states.spec.ts + dashboard-composition
 * .spec.ts — the change-password-on-first-login dance is handled via
 * the REST API so the page test never lands on the change-password
 * wall.
 *
 * The hook polls
 * `/projects/{name}/repos/{type}/{repo}/sync-jobs/{id}` (per-repo scope)
 * rather than `/api/v1/jobs/{id}` (no such endpoint exists).
 * The page.route glob mirrors the real URL.
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

async function seedDockerRepo(
  request: APIRequestContext,
  project: string,
  repo: string,
): Promise<{ project: string; name: string }> {
  await request.post('/api/v1/projects', { data: { name: project } });
  await request.post(`/api/v1/projects/${encodeURIComponent(project)}/repos`, {
    data: { name: repo, type: 'docker' },
  });
  return { project, name: repo };
}

test.describe('Docker clone modal', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('success flow: form → progress advances → result close', async ({
    page,
    request,
  }) => {
    const repo = await seedDockerRepo(
      request,
      `clone-ok-${Date.now()}`,
      'mirror',
    );

    // Mock the pull-external enqueue: return 202 { job_id: 999 }. Body
    // of the POST isn't asserted — this is a UI wiring spec, not a
    // backend contract spec.
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(repo.project)}/repos/docker/${encodeURIComponent(repo.name)}/pull-external`,
      (route) =>
        route.fulfill({
          status: 202,
          contentType: 'application/json',
          body: JSON.stringify({ job_id: 999 }),
        }),
    );

    // Mock progressive sync-job polling: first two polls show running
    // progress (layer 3 → layer 5), third poll flips to done. Each
    // call advances a local counter.
    const progression = [
      {
        id: 999,
        kind: 'pull_external',
        status: 'running',
        attempts: 1,
        progress_bytes: 42,
        total_bytes: 103,
        current_step: 'layer 3 of 7',
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
      {
        id: 999,
        kind: 'pull_external',
        status: 'running',
        attempts: 1,
        progress_bytes: 80,
        total_bytes: 103,
        current_step: 'layer 5 of 7',
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
      {
        id: 999,
        kind: 'pull_external',
        status: 'done',
        attempts: 1,
        progress_bytes: 103,
        total_bytes: 103,
        current_step: 'done',
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
    ];
    let pollCount = 0;
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(repo.project)}/repos/docker/${encodeURIComponent(repo.name)}/sync-jobs/999`,
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

    // Some fresh-install repos also touch upstream-creds on modal open
    // for the cred picker — mock an empty list so the picker loads
    // instantly regardless of the server's AEAD state.
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(repo.project)}/upstream-creds/`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([]),
        }),
    );

    await uiLoginAdmin(page);
    await page.goto(`/projects/${repo.project}/docker/${repo.name}`);

    // Open the modal.
    await page.getByRole('button', { name: 'Pull External' }).click();
    await expect(
      page.getByRole('dialog', { name: 'Clone external image' }),
    ).toBeVisible();

    // Fill source reference + click Pull.
    await page.fill('input#clone-src', 'docker.io/library/nginx:1.27');
    await page.getByRole('button', { name: 'Pull' }).click();

    // Progress state — first poll shows layer 3 of 7.
    await expect(page.getByText(/layer 3 of 7/)).toBeVisible();

    // After ~500ms the second poll lands — layer 5 of 7.
    await expect(page.getByText(/layer 5 of 7/)).toBeVisible({
      timeout: 3000,
    });

    // Third poll flips to done — success surface appears.
    await expect(page.getByText(/Cloned.*successfully/i)).toBeVisible({
      timeout: 3000,
    });

    // Close dismisses the modal. Two Close buttons exist — the shadcn
    // Dialog primitive's auto-rendered X (sr-only "Close") plus the
    // footer's explicit Close button on the success surface. Scope to
    // the dialog + pick the last match so we hit the visible footer one.
    await page
      .getByRole('dialog', { name: 'Clone external image' })
      .getByRole('button', { name: 'Close', exact: true })
      .last()
      .click();
    await expect(
      page.getByRole('dialog', { name: 'Clone external image' }),
    ).not.toBeVisible();
  });

  test('failure flow: failed job renders error envelope + retry resets to form', async ({
    page,
    request,
  }) => {
    const repo = await seedDockerRepo(
      request,
      `clone-fail-${Date.now()}`,
      'mirror',
    );

    await page.route(
      `**/api/v1/projects/${encodeURIComponent(repo.project)}/repos/docker/${encodeURIComponent(repo.name)}/pull-external`,
      (route) =>
        route.fulfill({
          status: 202,
          contentType: 'application/json',
          body: JSON.stringify({ job_id: 999 }),
        }),
    );

    // First poll returns failed straight away.
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(repo.project)}/repos/docker/${encodeURIComponent(repo.name)}/sync-jobs/999`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            id: 999,
            kind: 'pull_external',
            status: 'failed',
            attempts: 1,
            last_error: 'pull_external: remote.Get: unexpected EOF',
            progress_bytes: 0,
            total_bytes: 0,
            current_step: '',
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
          }),
        }),
    );

    await page.route(
      `**/api/v1/projects/${encodeURIComponent(repo.project)}/upstream-creds/`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([]),
        }),
    );

    await uiLoginAdmin(page);
    await page.goto(`/projects/${repo.project}/docker/${repo.name}`);

    await page.getByRole('button', { name: 'Pull External' }).click();
    await page.fill('input#clone-src', 'docker.io/library/nginx:1.27');
    await page.getByRole('button', { name: 'Pull' }).click();

    // ErrorEnvelopeRenderer renders with data-envelope-class attribute.
    await expect(page.locator('[data-envelope-class]')).toBeVisible({
      timeout: 3000,
    });
    await expect(page.getByText(/unexpected EOF/)).toBeVisible();

    // Retry resets phase to form — Source reference Input returns.
    await page.getByRole('button', { name: 'Retry' }).click();
    await expect(page.locator('input#clone-src')).toBeVisible();
  });
});

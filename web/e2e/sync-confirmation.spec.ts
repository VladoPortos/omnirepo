/**
 * Phase 9 / plan 09-05 — POLISH-01 Playwright regression for the
 * "Sync complete" success pill rendered by SyncNowButton.
 *
 * Covers D-01 through D-09:
 *   A. Pill appears when progress.status === 'done' (D-01, D-09).
 *   B. Pill auto-dismisses after exactly 8s (D-02).
 *   C. Re-click clears the pill immediately and the progress block
 *      reappears (D-06 — progress XOR confirmation).
 *   D. axe-core WCAG AA clean while the pill is visible (D-05).
 *   E. Helm fallback: total_bytes == 0 + current_step="chart 5 of 5"
 *      surfaces the step verbatim instead of "0 MB" (D-04).
 *
 * Network shape: sync POST + per-repo sync-jobs/{id} GET are mocked
 * via page.route so this spec doesn't depend on apt-mirror / helm /
 * rpm actually running. Pattern matches mirror-sync-now.spec.ts.
 *
 * PRE-EXISTING TEST-INFRA CAVEAT: all specs that call uiLoginAdmin in
 * this repo currently hit a known webServer bootstrap issue (see
 * SUMMARY for Plan 09-04 + Plan 09-05). The spec is tsc-clean and
 * structurally valid; execution blockage is documented separately.
 */

import { expect, test, type APIRequestContext, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

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

/**
 * Build a progression of sync-job poll responses that terminate on the
 * Nth call. `doneBody` is the response returned after the first
 * (running) tick so the pill has a deterministic "done" payload.
 */
function buildProgression(doneBody: Record<string, unknown>): Array<Record<string, unknown>> {
  const now = new Date().toISOString();
  return [
    {
      id: 777,
      kind: 'sync',
      status: 'running',
      attempts: 1,
      progress_bytes: 0,
      total_bytes: Number(doneBody.total_bytes) || 0,
      current_step: 'starting',
      // Running jobs always serialise files_synced as 0 (quick task
      // 260420-d03). The full count lands in the terminal tick.
      files_synced: 0,
      created_at: now,
      updated_at: now,
    },
    { ...doneBody, id: 777, kind: 'sync', attempts: 1, created_at: now, updated_at: now },
  ];
}

async function installMocks(
  page: Page,
  project: string,
  repo: string,
  doneBody: Record<string, unknown>,
): Promise<() => number> {
  let currentJobId = 776;
  let pollCount = 0;

  // Increment job_id per POST so TanStack Query doesn't short-circuit
  // the re-click test with stale cache for the first sync's id (the
  // cache is keyed by jobId; returning the same 777 twice made the
  // second poll cycle fall into the cached `done` state immediately,
  // hiding the progress line test C asserts for).
  await page.route(
    `**/api/v1/projects/${encodeURIComponent(project)}/repos/deb/${encodeURIComponent(repo)}/sync`,
    (route) => {
      currentJobId += 1;
      pollCount = 0;
      return route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({ job_id: currentJobId, kind: 'sync' }),
      });
    },
  );

  // Match any job_id on the sync-jobs path and replay the mock
  // progression. The progression is rebuilt per test; pollCount
  // resets on each POST.
  await page.route(
    `**/api/v1/projects/${encodeURIComponent(project)}/repos/deb/${encodeURIComponent(repo)}/sync-jobs/*`,
    (route) => {
      const progression = buildProgression(doneBody);
      const bodyTemplate =
        progression[Math.min(pollCount, progression.length - 1)];
      pollCount += 1;
      const body = { ...bodyTemplate, id: currentJobId };
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(body),
      });
    },
  );

  return () => pollCount;
}

test.describe('Sync complete pill (Phase 9 / plan 09-05 / POLISH-01)', () => {
  test.beforeEach(async ({ request }) => {
    await bootstrapAdmin(request);
  });

  test('A+B+D: pill appears on done, is a11y-clean, auto-dismisses at 8s', async ({
    page,
    request,
  }) => {
    const { project, repo } = await seedAptMirrorRepo(
      request,
      `pill-${Date.now()}`,
      'focal',
    );
    await installMocks(page, project, repo, {
      status: 'done',
      progress_bytes: 1_048_576,
      total_bytes: 1_048_576,
      current_step: 'completed',
      // Quick task 260420-d03: full D-03 literal shape now that backend
      // surfaces files_synced at completion.
      files_synced: 42,
    });

    await uiLoginAdmin(page);
    await page.goto(`/projects/${project}/deb/${repo}`);

    const btn = page.getByRole('button', { name: 'Sync now', exact: true });
    await expect(btn).toBeVisible();
    await btn.click();

    // A) Pill renders after the job flips to done, with the full D-03
    //    literal shape "Sync complete · N files · X MB".
    const pill = page.getByTestId('sync-complete-pill');
    await expect(pill).toBeVisible({ timeout: 3000 });
    await expect(pill).toContainText('Sync complete · 42 files · 1.0 MB');

    // XOR invariant: progress line must NOT be visible while the pill is.
    await expect(page.getByTestId('sync-progress-line')).toHaveCount(0);

    // D) axe-core clean — scoped to the pill only. The broader page
    // has pre-existing structural violations in shadcn Sidebar (Radix
    // Collapsible puts <div> inside <ul>) and the React Router
    // Breadcrumb pattern (<span class="contents"> wrapping <li>).
    // Those predate Phase 9 and are owned by the design system
    // / library layer, not by POLISH-01. D-05 scopes to "pill is
    // axe-core clean" — not "the whole page" — so the audit include
    // filter matches.
    const axe = await new AxeBuilder({ page })
      .include('[data-testid="sync-complete-pill"]')
      .withTags(['wcag2a', 'wcag2aa'])
      .disableRules([])
      .analyze();
    if (axe.violations.length > 0) {
      for (const v of axe.violations) {
        // eslint-disable-next-line no-console
        console.error(`[sync-pill] axe [${v.id}] ${v.impact}: ${v.help} — ${v.helpUrl}`);
      }
    }
    expect(axe.violations, 'axe-core reported WCAG AA violations inside the pill').toEqual([]);

    // B) Pill auto-dismisses after 8s (wait 8.5s for timer + React flush).
    await page.waitForTimeout(8500);
    await expect(page.getByTestId('sync-complete-pill')).toHaveCount(0);
  });

  test('C: re-click clears the pill immediately and progress block reappears', async ({
    page,
    request,
  }) => {
    const { project, repo } = await seedAptMirrorRepo(
      request,
      `pill-reclick-${Date.now()}`,
      'focal',
    );
    await installMocks(page, project, repo, {
      status: 'done',
      progress_bytes: 2_097_152,
      total_bytes: 2_097_152,
      current_step: 'completed',
    });

    await uiLoginAdmin(page);
    await page.goto(`/projects/${project}/deb/${repo}`);

    const btn = page.getByRole('button', { name: 'Sync now', exact: true });
    await btn.click();
    await expect(page.getByTestId('sync-complete-pill')).toBeVisible({ timeout: 3000 });

    // Re-click (button re-enables once status === 'done').
    await expect(btn).toBeEnabled({ timeout: 3000 });
    await btn.click();

    // Pill gone immediately (D-06 progress XOR confirmation).
    await expect(page.getByTestId('sync-complete-pill')).toHaveCount(0);
    // Progress block comes back for the new run.
    await expect(page.getByTestId('sync-progress-line')).toBeVisible({ timeout: 2000 });
  });

  test('F: pluralization — files_synced=1 renders singular "1 file" (quick task 260420-d03)', async ({
    page,
    request,
  }) => {
    const { project, repo } = await seedAptMirrorRepo(
      request,
      `pill-singular-${Date.now()}`,
      'focal',
    );
    await installMocks(page, project, repo, {
      status: 'done',
      progress_bytes: 524_288,
      total_bytes: 524_288,
      current_step: 'completed',
      files_synced: 1,
    });

    await uiLoginAdmin(page);
    await page.goto(`/projects/${project}/deb/${repo}`);

    await page.getByRole('button', { name: 'Sync now', exact: true }).click();

    const pill = page.getByTestId('sync-complete-pill');
    await expect(pill).toBeVisible({ timeout: 3000 });
    // Singular noun at N=1 — must NOT read "1 files".
    await expect(pill).toContainText('Sync complete · 1 file · 512.0 KB');
    await expect(pill).not.toContainText('1 files');
  });

  test('G: files_synced=0 falls back to bytes-only shape (sync scanned but added nothing)', async ({
    page,
    request,
  }) => {
    const { project, repo } = await seedAptMirrorRepo(
      request,
      `pill-zero-files-${Date.now()}`,
      'focal',
    );
    // Scenario: every upstream package was already present; totalBytes
    // reflects what was scanned but no new files landed. Pill drops the
    // "N files" piece rather than rendering "0 files".
    await installMocks(page, project, repo, {
      status: 'done',
      progress_bytes: 1_048_576,
      total_bytes: 1_048_576,
      current_step: 'completed',
      files_synced: 0,
    });

    await uiLoginAdmin(page);
    await page.goto(`/projects/${project}/deb/${repo}`);

    await page.getByRole('button', { name: 'Sync now', exact: true }).click();

    const pill = page.getByTestId('sync-complete-pill');
    await expect(pill).toBeVisible({ timeout: 3000 });
    await expect(pill).toContainText('Sync complete · 1.0 MB');
    await expect(pill).not.toContainText('0 files');
    await expect(pill).not.toContainText('0 file');
  });

  test('E: Helm fallback — total_bytes==0, currentStep "chart 5 of 5" surfaced verbatim', async ({
    page,
    request,
  }) => {
    const { project, repo } = await seedAptMirrorRepo(
      request,
      `pill-helm-${Date.now()}`,
      'focal',
    );
    // NOTE: we use the APT repo fixture + route-intercept so we don't need
    // a full Helm fixture — the pill content is decided client-side from
    // the mocked JobDetail payload, not from the repo type. The D-04
    // shape we're asserting is `Sync complete · chart 5 of 5`.
    await installMocks(page, project, repo, {
      status: 'done',
      progress_bytes: 0,
      total_bytes: 0,
      current_step: 'chart 5 of 5',
    });

    await uiLoginAdmin(page);
    await page.goto(`/projects/${project}/deb/${repo}`);

    await page.getByRole('button', { name: 'Sync now', exact: true }).click();

    const pill = page.getByTestId('sync-complete-pill');
    await expect(pill).toBeVisible({ timeout: 3000 });
    await expect(pill).toContainText('chart 5 of 5');
    // The pill must not say "0 MB" when total_bytes is 0 (anti-regression
    // for the Helm path).
    await expect(pill).not.toContainText('0 B');
    await expect(pill).not.toContainText('0 MB');
  });
});

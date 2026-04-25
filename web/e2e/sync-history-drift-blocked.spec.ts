/**
 * v1.7 Phase 4 / UIBACK-03 — SyncHistoryDialog percent-threshold
 * override flow.
 *
 * The dialog renders a "Drift purge blocked: N rows pending
 * confirmation" banner when the LATEST sync job's summary blob
 * carries `drift_blocked > 0`. The banner exposes an "Override and
 * purge" button that opens a confirm dialog; confirming POSTs
 *   /api/v1/projects/{name}/repos/{type}/{repo}/sync
 * with body `{"force_drift_threshold": true}` so the next sync
 * bypasses the v1.7 percent-threshold guard.
 *
 * Spec mocks both the sync-jobs response (to inject the blocked
 * summary) and the sync POST (to assert the body shape) so the
 * assertion is decoupled from a live drift-purge pipeline.
 */

import { expect, test, type APIRequestContext } from '@playwright/test';
import { adminLoginAPI, adminLoginUI, resetServerState } from './helpers/auth';

test.use({ viewport: { width: 1440, height: 900 } });

async function seedAptMirrorRepo(
  request: APIRequestContext,
  project: string,
  repo: string,
): Promise<void> {
  const projResp = await request.post('/api/v1/projects', {
    data: { name: project },
  });
  if (!projResp.ok()) {
    throw new Error(
      `seedAptMirrorRepo: project create ${project} failed (status=${projResp.status()})`,
    );
  }
  const repoResp = await request.post(
    `/api/v1/projects/${encodeURIComponent(project)}/repos`,
    {
      data: {
        name: repo,
        type: 'deb',
        is_mirror: true,
        mirror_upstream_url: 'https://archive.ubuntu.com/ubuntu',
        mirror_filter: { Suites: ['focal'], Components: ['main'], Arches: ['amd64'] },
        scan_on_sync: false,
      },
    },
  );
  if (!repoResp.ok()) {
    throw new Error(
      `seedAptMirrorRepo: repo create ${project}/${repo} failed (status=${repoResp.status()})`,
    );
  }
}

test.describe('SyncHistoryDialog drift-blocked override flow (UIBACK-03)', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('renders blocked banner; confirm flow POSTs /sync with force_drift_threshold=true', async ({
    page,
    request,
  }) => {
    const ts = Date.now();
    const project = `dblk-${ts}`;
    const repo = 'mirror-deb';
    await seedAptMirrorRepo(request, project, repo);

    const now = new Date().toISOString();
    // Latest job (id=210) carries summary.drift_blocked=42 → banner.
    // Older job (id=209) carries summary.drift_purged=3 (informational
    // sub-line) — proves the banner only surfaces the LATEST blocked
    // count, not history.
    const mockJobs = {
      items: [
        {
          id: 210,
          kind: 'sync',
          status: 'done',
          attempts: 1,
          progress_bytes: 0,
          total_bytes: 0,
          current_step: 'completed',
          files_synced: 0,
          summary: '{"drift_blocked":42}',
          created_at: now,
          updated_at: now,
        },
        {
          id: 209,
          kind: 'sync',
          status: 'done',
          attempts: 1,
          progress_bytes: 0,
          total_bytes: 0,
          current_step: 'completed',
          files_synced: 5,
          summary: '{"drift_purged":3}',
          created_at: now,
          updated_at: now,
        },
      ],
    };

    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/repos/deb/${encodeURIComponent(repo)}/sync-jobs**`,
      (route) => {
        if (route.request().method() !== 'GET') {
          return route.fallback();
        }
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(mockJobs),
        });
      },
    );

    // Capture the /sync POST so we can assert the body shape.
    let syncPostBody: string | null = null;
    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/repos/deb/${encodeURIComponent(repo)}/sync`,
      (route) => {
        if (route.request().method() !== 'POST') {
          return route.fallback();
        }
        syncPostBody = route.request().postData();
        return route.fulfill({
          status: 202,
          contentType: 'application/json',
          body: JSON.stringify({ job_id: 999, kind: 'apt_sync' }),
        });
      },
    );

    await adminLoginUI(page);
    await page.goto(`/projects/${project}/deb/${repo}`);

    // Open the history dialog.
    await page.getByRole('button', { name: /History/i }).click();
    await expect(
      page.getByRole('dialog', { name: /Sync history/i }),
    ).toBeVisible();

    // Banner appears with the blocked count + override button.
    const banner = page.getByTestId('drift-blocked-banner');
    await expect(banner).toBeVisible();
    await expect(banner).toContainText(/Drift purge blocked: 42 rows pending confirmation/i);

    // Click Override and purge → confirm dialog opens.
    await page.getByTestId('drift-blocked-override-button').click();
    const confirmDlg = page.getByRole('dialog', {
      name: /Override drift purge guard/i,
    });
    await expect(confirmDlg).toBeVisible();
    await expect(confirmDlg).toContainText(/will purge\s*42/);

    // Confirm — POST fires with the force flag.
    await page.getByTestId('drift-blocked-confirm-button').click();

    // Wait for the POST to land (route handler captured the body).
    await expect.poll(() => syncPostBody, { timeout: 5000 }).toBeTruthy();
    expect(syncPostBody).toBe('{"force_drift_threshold":true}');

    // Confirm dialog closes; parent dialog also closes (intentional —
    // signals action landed; SyncNowButton's progress block surfaces).
    await expect(confirmDlg).not.toBeVisible();
    await expect(
      page.getByRole('dialog', { name: /Sync history/i }),
    ).not.toBeVisible();
  });

  test('absent banner when no job carries drift_blocked', async ({
    page,
    request,
  }) => {
    const ts = Date.now();
    const project = `dblk-no-${ts}`;
    const repo = 'mirror-deb';
    await seedAptMirrorRepo(request, project, repo);

    const now = new Date().toISOString();
    const mockJobs = {
      items: [
        {
          id: 1,
          kind: 'sync',
          status: 'done',
          attempts: 1,
          progress_bytes: 0,
          total_bytes: 0,
          current_step: 'completed',
          files_synced: 5,
          summary: '{"drift_purged":2}',
          created_at: now,
          updated_at: now,
        },
      ],
    };

    await page.route(
      `**/api/v1/projects/${encodeURIComponent(project)}/repos/deb/${encodeURIComponent(repo)}/sync-jobs**`,
      (route) => {
        if (route.request().method() !== 'GET') {
          return route.fallback();
        }
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(mockJobs),
        });
      },
    );

    await adminLoginUI(page);
    await page.goto(`/projects/${project}/deb/${repo}`);
    await page.getByRole('button', { name: /History/i }).click();
    await expect(
      page.getByRole('dialog', { name: /Sync history/i }),
    ).toBeVisible();

    // No banner.
    await expect(page.getByTestId('drift-blocked-banner')).toHaveCount(0);
    // The drift_purged sub-line should still render (UIBACK-01).
    await expect(page.getByTestId('sync-history-drift-purged')).toHaveCount(1);
  });
});

/**
 * v1.7 Phase 4 / UIBACK-01 — SyncHistoryDialog "Drift purged: N" sub-line.
 *
 * The dialog calls
 *   GET /api/v1/projects/{name}/repos/{type}/{repo}/sync-jobs
 * lazily (only after the user clicks History). Each row carries a
 * `summary` JSON string sourced from sync_jobs.summary; when that blob
 * decodes to `{ drift_purged: N }` with N>0 the dialog renders a
 * dedicated "Drift purged: N" line under the row's "Last step" cell.
 * N==0 (run-evidence per D-21) is suppressed so non-drift mirror jobs
 * stay visually clean.
 *
 * The spec mocks the sync-jobs response so the assertion is decoupled
 * from the actual mirror-sync pipeline (which would require a live
 * upstream + drift adapter to populate summary.drift_purged > 0).
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

test.describe('SyncHistoryDialog drift_purged line (UIBACK-01)', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('renders "Drift purged: N" for jobs with summary.drift_purged > 0; suppresses for 0/absent', async ({
    page,
    request,
  }) => {
    const ts = Date.now();
    const project = `drft-${ts}`;
    const repo = 'mirror-deb';
    await seedAptMirrorRepo(request, project, repo);

    const now = new Date().toISOString();
    // Three rows: drift_purged=12 (line shows), drift_purged=0 (line
    // suppressed — the absence side of D-21), and absent summary key
    // (also suppressed — drift never ran for this job).
    const mockBody = {
      items: [
        {
          id: 103,
          kind: 'sync',
          status: 'done',
          attempts: 1,
          progress_bytes: 0,
          total_bytes: 0,
          current_step: 'completed',
          files_synced: 5,
          summary: '{"drift_purged":12}',
          created_at: now,
          updated_at: now,
        },
        {
          id: 102,
          kind: 'sync',
          status: 'done',
          attempts: 1,
          progress_bytes: 0,
          total_bytes: 0,
          current_step: 'completed',
          files_synced: 0,
          summary: '{"drift_purged":0}',
          created_at: now,
          updated_at: now,
        },
        {
          id: 101,
          kind: 'sync',
          status: 'done',
          attempts: 1,
          progress_bytes: 0,
          total_bytes: 0,
          current_step: 'completed',
          files_synced: 3,
          summary: '{}',
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
          body: JSON.stringify(mockBody),
        });
      },
    );

    await adminLoginUI(page);
    await page.goto(`/projects/${project}/deb/${repo}`);

    const historyBtn = page.getByRole('button', { name: /History/i });
    await expect(historyBtn).toBeVisible();
    await historyBtn.click();

    // Dialog opens — wait for one row to render before asserting on
    // the drift sub-line testid (waits past the loading spinner).
    await expect(page.getByRole('dialog', { name: /Sync history/i })).toBeVisible();
    await expect(page.getByText('completed').first()).toBeVisible();

    // Exactly one row carries the drift sub-line — the row with
    // drift_purged=12. Other two rows must NOT render it.
    const driftLines = page.getByTestId('sync-history-drift-purged');
    await expect(driftLines).toHaveCount(1);
    await expect(driftLines).toHaveText(/Drift purged:\s*12/);
  });
});

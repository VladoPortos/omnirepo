/**
 * Playwright smoke for the drift_purge checkbox in MirrorConfigSection.
 *
 * Covers:
 *   1. Maintainer toggles drift_purge on a mirror repo, saves, reloads,
 *      and the checkbox state persists.
 *   2. Viewer sees the checkbox visible but DISABLED (maintainer gate
 *      via useRoleFor).
 */

import { expect, test, type APIRequestContext } from '@playwright/test';
import {
  adminLoginAPI,
  resetServerState,
  seedUserWithProjectRole,
  passwordLogin,
} from './helpers/auth';

test.use({ viewport: { width: 1440, height: 900 } });

async function seedPypiMirrorRepo(
  request: APIRequestContext,
  project: string,
  repo: string,
): Promise<{ project: string; repo: string }> {
  const projResp = await request.post('/api/v1/projects', {
    data: { name: project },
  });
  if (!projResp.ok()) {
    throw new Error(
      `seedPypiMirrorRepo: project create ${project} failed (status=${projResp.status()})`,
    );
  }
  const repoResp = await request.post(
    `/api/v1/projects/${encodeURIComponent(project)}/repos`,
    {
      data: {
        name: repo,
        type: 'pypi',
        is_mirror: true,
        mirror_upstream_url: 'https://pypi.org/simple/',
        mirror_filter: {},
        scan_on_sync: false,
      },
    },
  );
  if (!repoResp.ok()) {
    throw new Error(
      `seedPypiMirrorRepo: repo create ${project}/${repo} failed (status=${repoResp.status()})`,
    );
  }
  return { project, repo };
}

test.describe('drift_purge toggle', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('maintainer can toggle drift_purge on mirror repo', async ({
    page,
    request,
  }) => {
    const ts = Date.now();
    const { project, repo } = await seedPypiMirrorRepo(
      request,
      `drift-${ts}`,
      'mirror-py',
    );

    const otp = await seedUserWithProjectRole(
      request,
      `dpm-${ts}`,
      'maintainer',
      project,
    );

    // End the admin session before logging in as the maintainer.
    await page.context().clearCookies();
    const ok = await passwordLogin(page, `dpm-${ts}`, otp);
    expect(ok, 'maintainer password login should succeed').toBe(true);

    await page.goto(`/projects/${project}/pypi/${repo}/settings`);

    const driftCb = page.getByRole('checkbox', {
      name: /Auto-remove mirror rows whose upstream entry vanished/i,
    });
    await expect(driftCb).toBeVisible();
    await expect(driftCb).not.toBeChecked();
    await expect(driftCb).toBeEnabled();

    // Toggle on and save.
    await driftCb.click();
    await expect(driftCb).toBeChecked();
    const saveBtn = page.getByRole('button', { name: 'Save', exact: true });
    await saveBtn.click();

    // Wait for mutation to complete — Save button re-enables (or stays
    // disabled because no longer dirty). Either way, the page is stable.
    await page.waitForLoadState('networkidle');

    // Reload and re-assert: state persisted.
    await page.reload();
    await expect(driftCb).toBeChecked();

    // Toggle off and verify round-trip.
    await driftCb.click();
    await expect(driftCb).not.toBeChecked();
    await saveBtn.click();
    await page.waitForLoadState('networkidle');
    await page.reload();
    await expect(driftCb).not.toBeChecked();
  });

  test('viewer sees drift_purge disabled', async ({ page, request }) => {
    const ts = Date.now();
    const { project, repo } = await seedPypiMirrorRepo(
      request,
      `drift-v-${ts}`,
      'mirror-py',
    );

    const otp = await seedUserWithProjectRole(
      request,
      `dpv-${ts}`,
      'viewer',
      project,
    );

    await page.context().clearCookies();
    const ok = await passwordLogin(page, `dpv-${ts}`, otp);
    expect(ok, 'viewer password login should succeed').toBe(true);

    await page.goto(`/projects/${project}/pypi/${repo}/settings`);

    const driftCb = page.getByRole('checkbox', {
      name: /Auto-remove mirror rows whose upstream entry vanished/i,
    });
    await expect(driftCb).toBeVisible();
    await expect(driftCb).toBeDisabled();
  });
});

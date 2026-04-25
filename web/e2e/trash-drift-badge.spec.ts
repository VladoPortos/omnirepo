/**
 * v1.7 Phase 4 / UIBACK-02 — TrashPage colored badge for <proto>_drift kinds.
 *
 * The Type column on /admin/trash renders a distinct amber outline
 * badge (text "Drift · <Proto>") for the four drift-purge trash kinds
 * emitted by driftpurge.Run + storage.Trash.MoveWithSnapshot:
 *   pypi_file_drift, rpm_package_drift, deb_package_drift, helm_chart_drift.
 *
 * The badge MUST:
 *   - render with text "Drift · <ProtoLabel>" (UIBACK-02 acceptance)
 *   - carry an aria-label that spells out the kind for screen readers
 *   - be keyboard-focusable (tabIndex >= 0)
 *
 * Non-drift kinds (e.g. "repo", "project") keep the existing
 * default/secondary badge.
 *
 * Spec mocks /admin/trash so the assertion is decoupled from the
 * driftpurge sync pipeline (which would require a live mirror upstream
 * to populate trash_drift entries).
 */

import { expect, test } from '@playwright/test';
import { adminLoginAPI, adminLoginUI, resetServerState } from './helpers/auth';

test.use({ viewport: { width: 1440, height: 900 } });

test.describe('TrashPage drift-kind badge (UIBACK-02)', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('renders amber drift badge for the four <proto>_drift kinds; default badge for others', async ({
    page,
  }) => {
    const now = new Date().toISOString();
    const mockBody = {
      items: [
        {
          id: 'pypi-1',
          type: 'pypi_file_drift',
          name: 'requests-2.31.0.tar.gz',
          original_location: '/repos/projA/pypi/main/packages/requests-2.31.0.tar.gz',
          deleted_by: 'system',
          deleted_at: now,
          retention_countdown: '6d',
        },
        {
          id: 'rpm-1',
          type: 'rpm_package_drift',
          name: 'curl-8.4.0-1.x86_64.rpm',
          original_location: '/repos/projA/rpm/main/Packages/curl-8.4.0-1.x86_64.rpm',
          deleted_by: 'system',
          deleted_at: now,
          retention_countdown: '6d',
        },
        {
          id: 'deb-1',
          type: 'deb_package_drift',
          name: 'curl_8.4.0-1_amd64.deb',
          original_location: '/repos/projA/deb/main/pool/c/curl_8.4.0-1_amd64.deb',
          deleted_by: 'system',
          deleted_at: now,
          retention_countdown: '6d',
        },
        {
          id: 'helm-1',
          type: 'helm_chart_drift',
          name: 'redis-17.0.0.tgz',
          original_location: '/repos/projA/helm/main/redis-17.0.0.tgz',
          deleted_by: 'system',
          deleted_at: now,
          retention_countdown: '6d',
        },
        {
          id: 'plain-1',
          type: 'repo',
          name: 'old-repo',
          original_location: '/repos/projB/raw/old-repo',
          deleted_by: 'admin',
          deleted_at: now,
          retention_countdown: '5d',
        },
      ],
      next_cursor: null,
    };

    await page.route('**/api/v1/admin/trash**', (route) => {
      if (route.request().method() !== 'GET') {
        return route.fallback();
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(mockBody),
      });
    });

    await adminLoginUI(page);
    await page.goto('/admin/trash');

    // Four drift badges, one per proto kind.
    const driftBadges = page.getByTestId('trash-drift-badge');
    await expect(driftBadges).toHaveCount(4);

    // Each carries the expected human label.
    await expect(driftBadges.filter({ hasText: /Drift · PyPI/ })).toHaveCount(1);
    await expect(driftBadges.filter({ hasText: /Drift · RPM/ })).toHaveCount(1);
    await expect(driftBadges.filter({ hasText: /Drift · APT/ })).toHaveCount(1);
    await expect(driftBadges.filter({ hasText: /Drift · Helm/ })).toHaveCount(1);

    // aria-label spells out the kind.
    await expect(
      driftBadges.filter({ hasText: /Drift · PyPI/ }),
    ).toHaveAttribute('aria-label', 'Drift purge: PyPI');

    // Keyboard-focusable: tabIndex attribute present (Tab to it focuses it).
    await expect(
      driftBadges.filter({ hasText: /Drift · PyPI/ }),
    ).toHaveAttribute('tabindex', '0');

    // Plain "repo" row keeps the default badge — no drift markup.
    const repoRow = page.getByRole('row', { name: /old-repo/ });
    await expect(repoRow).toBeVisible();
    await expect(
      repoRow.locator('[data-testid="trash-drift-badge"]'),
    ).toHaveCount(0);
  });
});

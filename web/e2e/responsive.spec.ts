/**
 * Phase 6 / plan 06-08 — Responsive hard gate (VISUAL-06).
 *
 * Asserts no horizontal PAGE scroll at 1366x768 on every canonical admin
 * route. Table body horizontal scroll inside an `overflow-x-auto`
 * wrapper is allowed (and expected on ProjectsPage + admin tables with
 * 6+ columns, per plan 06-07's sticky-first-column pattern). What we
 * forbid is the whole `document.documentElement` scrolling horizontally,
 * which would push the sidebar + main content off-screen on a typical
 * 1366x768 admin laptop.
 *
 * Auth bootstrap mirrors admin.spec.ts so the routes are reachable.
 * must_change_password flow is handled via the API so the UI tests
 * don't land on the change-password page.
 */

import { test, expect } from '@playwright/test';

test.use({ viewport: { width: 1366, height: 768 } });

test.describe('1366x768 horizontal-scroll gate', () => {
  test.beforeEach(async ({ request }) => {
    const resp = await request.post('/api/v1/auth/login', {
      data: { login: 'admin', password: 'AdminTest1!' },
    });
    if (resp.ok()) {
      const body = await resp.json();
      if (body.must_change_password) {
        await request.post('/api/v1/auth/change-password', {
          data: { current: 'AdminTest1!', new: 'AdminTest1!' },
        });
        await request.post('/api/v1/auth/login', {
          data: { login: 'admin', password: 'AdminTest1!' },
        });
      }
    }
  });

  // Six admin-surface routes. ProjectsPage + admin/* pages are where
  // horizontal-scroll risk is highest because of 6+ column tables.
  // Plan 06-07 enabled stickyFirstColumn on Users/Audit/Trash and added
  // the overflow-x-auto wrapper on ProjectsPage, which should make this
  // gate green. If any route fails, the SUMMARY documents which and
  // files a bug.
  const adminRoutes = [
    '/dashboard',
    '/projects',
    '/admin/users',
    '/admin/audit',
    '/admin/trash',
    '/admin/trivy',
  ];

  for (const route of adminRoutes) {
    test(`${route} has no horizontal page scroll at 1366x768`, async ({
      page,
    }) => {
      await page.goto(route);
      await page.waitForLoadState('networkidle');

      // Extra settle time so any lazy-loaded tables can finish laying
      // out. Without this, some pages evaluate scrollWidth before the
      // table renders and the value races the real measurement.
      await page.waitForTimeout(500);

      const { scrollWidth, clientWidth } = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
      }));

      expect(
        scrollWidth,
        `Page ${route} has horizontal scroll: scrollWidth=${scrollWidth} clientWidth=${clientWidth} at 1366x768. ` +
          `Plan 06-07 should have wrapped wide tables in overflow-x-auto.`,
      ).toBeLessThanOrEqual(clientWidth);
    });
  }
});

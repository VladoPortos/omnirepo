/**
 * Phase 7 / plan 07-07 — DashboardPage Composition row (D-01..D-06).
 *
 * Asserts the row of 6 cards (3 user-visible + 3 admin-only) renders at
 * 1366×768 for a super-admin session, and that non-admin sessions see
 * ONLY the 3 user-visible cards (admin-gated cards are conditionally
 * rendered based on useMe().is_super_admin).
 *
 * Also hard-gates against horizontal PAGE scroll at 1366×768 — the
 * existing responsive.spec.ts covers this for /dashboard, but repeating
 * it here co-locates the regression guard with the cards that introduced
 * the risk (6-card grid at 2 col md breakpoint = tight layout budget).
 *
 * Auth bootstrap mirrors admin.spec.ts + error-envelope.spec.ts — the
 * change-password-on-first-login dance is handled via the REST API so
 * the page tests don't land on the change-password wall.
 *
 * Non-admin flow seeds a fresh user via POST /api/v1/admin/users (uses
 * the super-admin session), captures its one_time_password, and drives
 * the must_change_password wall to establish a working non-admin
 * session.
 */

import { test, expect, type Page, type APIRequestContext } from '@playwright/test';

test.use({ viewport: { width: 1366, height: 768 } });

const USER_VISIBLE_TITLES = ['Storage', 'Recent Failures', 'Scan Findings Trend'];
const ADMIN_ONLY_TITLES = ['Background Jobs', 'TLS Certificate', 'Trivy Database'];

const ADMIN_PW = 'AdminTest1!';
const NON_ADMIN_LOGIN = `compuser-${Date.now()}`;
const NON_ADMIN_EMAIL = `${NON_ADMIN_LOGIN}@example.invalid`;
const NON_ADMIN_PW = 'NonAdmin1!';

/**
 * bootstrapAdmin — drive the change-password-on-first-login dance via
 * the REST API. Idempotent: if the admin already changed their password
 * to ADMIN_PW on a previous run, the second login call succeeds and the
 * must_change_password branch is skipped.
 */
async function bootstrapAdmin(request: APIRequestContext): Promise<void> {
  const first = await request.post('/api/v1/auth/login', {
    data: { login: 'admin', password: 'changeme' },
  });
  if (first.ok()) {
    const body = await first.json();
    if (body.must_change_password) {
      await request.post('/api/v1/auth/change-password', {
        data: { current: 'changeme', new: ADMIN_PW },
      });
    }
  }
  // Final ensured session is under ADMIN_PW.
  await request.post('/api/v1/auth/login', {
    data: { login: 'admin', password: ADMIN_PW },
  });
}

/**
 * uiLogin — walk the UI login form. Used after we already changed the
 * password via bootstrapAdmin so we land on /dashboard.
 */
async function uiLogin(page: Page, login: string, password: string): Promise<void> {
  await page.goto('/login');
  await page.fill('input#login', login);
  await page.fill('input#password', password);
  await page.click('button[type="submit"]');
  // If a must_change_password wall is hit (fresh non-admin user), drive it.
  await page.waitForLoadState('networkidle');
  if (page.url().includes('/change-password')) {
    // fill the change-password form — selectors follow the existing
    // ChangePasswordPage conventions used across other specs.
    await page.fill('input#current', password);
    await page.fill('input#new_password', password + 'x');
    await page.fill('input#new_password_confirm', password + 'x');
    await page.click('button[type="submit"]');
    await page.waitForLoadState('networkidle');
    // Log back in with the new password so cookies settle.
    await page.goto('/login');
    await page.fill('input#login', login);
    await page.fill('input#password', password + 'x');
    await page.click('button[type="submit"]');
    await page.waitForLoadState('networkidle');
  }
}

test.describe('DashboardPage Composition row (Phase 7 D-01..D-06)', () => {
  test('super-admin sees all 6 composition cards at 1366×768', async ({
    page,
    request,
  }) => {
    await bootstrapAdmin(request);

    // Drive the UI login so the browser has a cookie-authenticated
    // session (APIRequestContext cookies aren't automatically shared
    // with the BrowserContext).
    await page.goto('/login');
    await page.fill('input#login', 'admin');
    await page.fill('input#password', ADMIN_PW);
    await page.click('button[type="submit"]');
    await page.waitForLoadState('networkidle');

    await page.goto('/dashboard');
    // Wait for the composition row heading (sr-only) — proves the row
    // mounted after useMe() resolved.
    await expect(
      page.getByRole('region', { name: 'Status summary' }),
    ).toBeVisible({ timeout: 10_000 });

    // Wait for at least one admin-only card to confirm is_super_admin
    // gating evaluated true and the Composition row hydrated.
    await expect(
      page.getByRole('heading', { name: 'Background Jobs', exact: true }),
    ).toBeVisible({ timeout: 10_000 });

    for (const title of [...USER_VISIBLE_TITLES, ...ADMIN_ONLY_TITLES]) {
      await expect(
        page
          .locator('[data-slot="card-title"]')
          .filter({ hasText: new RegExp(`^${title}$`) }),
      ).toBeVisible();
    }

    // No horizontal page scroll at 1366×768 (VISUAL-06 regression gate
    // co-located with the cards that introduced the risk).
    await page.waitForTimeout(500); // let lazy-hydrating cards settle
    const scroll = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(
      scroll.scrollWidth,
      `Dashboard has horizontal page scroll at 1366×768: ${scroll.scrollWidth} > ${scroll.clientWidth}`,
    ).toBeLessThanOrEqual(scroll.clientWidth);
  });

  test('non-admin sees 3 user-visible cards, no admin-only cards', async ({
    page,
    request,
  }) => {
    // Seed a fresh non-super-admin via the admin API (uses the
    // super-admin session). If the user already exists from a prior
    // run, POST returns 409 — we ignore that and proceed.
    await bootstrapAdmin(request);
    const createResp = await request.post('/api/v1/admin/users', {
      data: { login: NON_ADMIN_LOGIN, email: NON_ADMIN_EMAIL },
    });
    let oneTimePw: string | null = null;
    if (createResp.ok()) {
      const body = await createResp.json();
      oneTimePw = body.one_time_password ?? null;
    }

    // Can't proceed if we don't have a password — the API rotates
    // one_time_password on create and there's no server-side way to
    // reset it short of super-admin PATCH. Skip rather than fail.
    if (!oneTimePw) {
      test.skip(true, 'Non-admin seed user already exists from prior run; cannot recover OTP.');
      return;
    }

    // Log out the super-admin session in the Browser context by
    // clearing cookies, then log in as the non-admin via the UI form
    // (which handles the must_change_password wall inline).
    await page.context().clearCookies();
    await uiLogin(page, NON_ADMIN_LOGIN, oneTimePw);

    await page.goto('/dashboard');
    await expect(
      page.getByRole('region', { name: 'Status summary' }),
    ).toBeVisible({ timeout: 10_000 });

    // User-visible cards present.
    for (const title of USER_VISIBLE_TITLES) {
      await expect(
        page
          .locator('[data-slot="card-title"]')
          .filter({ hasText: new RegExp(`^${title}$`) }),
      ).toBeVisible();
    }

    // Admin-only cards MUST NOT render — gating is `{isSuperAdmin && <>...</>}`
    // in DashboardPage, so the titles simply don't exist in the DOM.
    for (const title of ADMIN_ONLY_TITLES) {
      await expect(
        page
          .locator('[data-slot="card-title"]')
          .filter({ hasText: new RegExp(`^${title}$`) }),
      ).toHaveCount(0);
    }

    // No horizontal page scroll — with only 3 cards the grid is
    // smaller, but the regression gate still applies.
    await page.waitForTimeout(500);
    const scroll = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(scroll.scrollWidth).toBeLessThanOrEqual(scroll.clientWidth);
  });
});

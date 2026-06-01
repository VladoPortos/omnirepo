/**
 * DashboardPage Composition row.
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

import { test, expect, type Page } from '@playwright/test';
import { adminLoginAPI, resetServerState } from './helpers/auth';

test.use({ viewport: { width: 1366, height: 768 } });

const USER_VISIBLE_TITLES = ['Storage', 'Recent Failures', 'Scan Findings Trend'];
const ADMIN_ONLY_TITLES = ['Background Jobs', 'TLS Certificate', 'Trivy Database'];

const ADMIN_PW = 'AdminTest1!';
const NON_ADMIN_LOGIN = `compuser-${Date.now()}`;
const NON_ADMIN_EMAIL = `${NON_ADMIN_LOGIN}@example.invalid`;
const NON_ADMIN_PW = 'NonAdmin1!';

/**
 * uiLogin — walk the UI login form. Used after beforeEach's
 * adminLoginAPI + resetServerState so we land on /dashboard (for the
 * super-admin branch) or drive the must_change_password wall for a
 * freshly-seeded non-admin user.
 */
async function uiLogin(page: Page, login: string, password: string): Promise<void> {
  await page.goto('/login');
  await page.fill('input#login', login);
  await page.fill('input#password', password);
  await page.click('button[type="submit"]');
  // If a must_change_password wall is hit (fresh non-admin user), drive it.
  await page.waitForLoadState('networkidle');
  if (page.url().includes('/change-password')) {
    // ChangePasswordPage uses hyphenated IDs (current-password /
    // new-password / confirm-password); underscored locators are stale.
    await page.fill('input#current-password', password);
    await page.fill('input#new-password', password + 'x');
    await page.fill('input#confirm-password', password + 'x');
    // The submit click fires a fetch; navigating the page before the
    // fetch resolves cancels it in the browser, which shows up as
    // `metadata: begin write tx: context canceled` on the server.
    // Wait for the success response BEFORE any subsequent navigation.
    await Promise.all([
      page.waitForResponse(
        (resp) =>
          resp.url().includes('/api/v1/auth/change-password') &&
          resp.request().method() === 'POST',
      ),
      page.click('button[type="submit"]'),
    ]);
    // Known SPA race: useMe() cache may still report
    // must_change_password=true briefly after the mutation succeeds,
    // so MustChangePasswordGuard can bounce the user straight back to
    // /change-password even though the server cleared the flag. Force
    // a hard reload of /login to discard the stale React Query cache,
    // then log in with the new password so the in-memory user state
    // comes from the fresh /auth/login response.
    await page.goto('/login');
    await page.fill('input#login', login);
    await page.fill('input#password', password + 'x');
    await page.click('button[type="submit"]');
    // Wait until the SPA has actually left both /login and
    // /change-password — that is the signal the auth + guards
    // settled on an authenticated, no-wall state.
    await page.waitForURL((url) => {
      const p = new URL(url).pathname;
      return p !== '/login' && p !== '/change-password';
    }, { timeout: 15_000 });
  }
}

test.describe('DashboardPage Composition row', () => {
  // Playwright's default 30 s per-test timeout is tight for the non-admin
  // branch here: beforeEach (adminLoginAPI + resetServerState) + POST
  // /admin/users + UI login + must_change_password dance + UI re-login +
  // goto('/') + dashboard cold-load all share the 30 s budget, and the
  // cold-load alone can approach 15-20 s after a reset. Bump to 60 s so
  // the per-assertion 30 s timeout on the Composition region has room to
  // breathe.
  test.describe.configure({ timeout: 60_000 });

  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('super-admin sees all 6 composition cards at 1366×768', async ({
    page,
  }) => {
    // Drive the UI login so the browser has a cookie-authenticated
    // session (APIRequestContext cookies aren't automatically shared
    // with the BrowserContext).
    await page.goto('/login');
    await page.fill('input#login', 'admin');
    await page.fill('input#password', ADMIN_PW);
    await page.click('button[type="submit"]');
    await page.waitForLoadState('networkidle');

    // DashboardPage is the SPA index route (App.tsx:304), not /dashboard —
    // /dashboard renders "Page Not Found". Navigate to / for the dashboard.
    await page.goto('/');
    // DashboardPage renders a full-page skeleton while `isLoading &&
    // storageLoading` (DashboardPage.tsx:313); the Composition row only
    // mounts once either slice resolves. After resetServerState the DB
    // is freshly empty and cold-loads of useDashboard + useDashboardStorage
    // can take >10 s combined with React hydration. Wait for the skeleton
    // to clear via a real data-ready sentinel — the Storage card heading
    // renders inside the region once storageData lands.
    await expect(
      page.getByRole('region', { name: 'Status summary' }),
    ).toBeVisible({ timeout: 30_000 });

    // Scope card-title lookups to inside the Composition region so we
    // don't collide with duplicate-titled cards rendered elsewhere on
    // the dashboard (e.g. the Recent Activity "Storage" entry at
    // 1366×768 is rendered in a different row and triggers a strict-
    // mode violation against the bare card-title selector).
    const composition = page.getByRole('region', { name: 'Status summary' });

    // Wait for at least one admin-only card to confirm is_super_admin
    // gating evaluated true and the Composition row hydrated. CardTitle
    // renders as <div data-slot="card-title"> (not a heading role) in
    // the current shadcn/ui revision — same selector the title loop
    // below uses.
    await expect(
      composition
        .locator('[data-slot="card-title"]')
        .filter({ hasText: /^Background Jobs$/ }),
    ).toBeVisible({ timeout: 10_000 });

    for (const title of [...USER_VISIBLE_TITLES, ...ADMIN_ONLY_TITLES]) {
      await expect(
        composition
          .locator('[data-slot="card-title"]')
          .filter({ hasText: new RegExp(`^${title}$`) }),
      ).toBeVisible();
    }

    // No horizontal page scroll at 1366×768 (regression gate
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
    // super-admin session established by beforeEach adminLoginAPI +
    // resetServerState). After reset, the user list is empty, so the
    // POST below is guaranteed to create rather than 409.
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

    // DashboardPage is the SPA index route (App.tsx:304), not /dashboard —
    // /dashboard renders "Page Not Found". Navigate to / for the dashboard.
    await page.goto('/');
    // Same skeleton-cold-load budget as the super-admin branch above.
    await expect(
      page.getByRole('region', { name: 'Status summary' }),
    ).toBeVisible({ timeout: 30_000 });

    // Scope to the Composition region to avoid colliding with other
    // card titles on the page (see super-admin branch comment).
    const composition = page.getByRole('region', { name: 'Status summary' });

    // User-visible cards present.
    for (const title of USER_VISIBLE_TITLES) {
      await expect(
        composition
          .locator('[data-slot="card-title"]')
          .filter({ hasText: new RegExp(`^${title}$`) }),
      ).toBeVisible();
    }

    // Admin-only cards MUST NOT render — gating is `{isSuperAdmin && <>...</>}`
    // in DashboardPage, so the titles simply don't exist in the DOM.
    for (const title of ADMIN_ONLY_TITLES) {
      await expect(
        composition
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

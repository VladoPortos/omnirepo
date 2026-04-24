/**
 * Shared admin-auth + seed helpers for Playwright specs.
 *
 * Prior to F-15.4 (post-v1.4), every spec carried its own inline
 * `loginAsAdmin` / `changePasswordIfNeeded` helpers hardcoding
 * `password: 'changeme'`. The actual seeded password — set by
 * `global-setup.ts` via the first-run `/api/v1/setup/superadmin`
 * endpoint — is `AdminTest1!` with `must_change_password=0`. Every spec
 * failed on the first login attempt before ever reaching the
 * now-defunct change-password dance.
 *
 * Single source of truth here. Spec files must not hardcode credentials.
 */

import { expect, type APIRequestContext, type Page } from '@playwright/test';

export const ADMIN_LOGIN = 'admin';

// Matches global-setup.ts seed. See that file's comment for the reasoning
// behind seeding with must_change_password=0 (short-circuits the legacy
// bootstrap dance for every spec that doesn't specifically test it).
export const ADMIN_PASSWORD = 'AdminTest1!';

/**
 * UI sign-in flow. After this returns successfully, `page` carries a
 * valid session cookie and is not on `/login`.
 *
 * Intentionally stricter than the pre-v1.4 inline helpers:
 * if the server redirects to /change-password (unexpected under the
 * must_change_password=0 seed), the caller's `await expect(page).not
 * .toHaveURL(/\/login/)` still passes — specs that need to differentiate
 * should check page.url() themselves.
 */
export async function adminLoginUI(page: Page): Promise<void> {
  await page.goto('/login');
  await page.fill('input#login', ADMIN_LOGIN);
  await page.fill('input#password', ADMIN_PASSWORD);
  await page.click('button[type="submit"]');
  await expect(page).not.toHaveURL(/\/login$/, { timeout: 10_000 });
}

/**
 * API sign-in flow. Attaches a session cookie to `request`'s jar so
 * subsequent calls inherit the admin actor. No return value — caller
 * just needs the side effect on the context.
 */
export async function adminLoginAPI(
  request: APIRequestContext,
): Promise<void> {
  const resp = await request.post('/api/v1/auth/login', {
    data: { login: ADMIN_LOGIN, password: ADMIN_PASSWORD },
  });
  expect(
    resp.ok(),
    `admin API login failed: ${resp.status()} ${await resp.text()}`,
  ).toBeTruthy();
}

/**
 * Create a secondary user via `POST /admin/users` and return the server
 * -generated one-time password. The endpoint hard-codes
 * must_change_password=true on every creation path, so the returned
 * credentials are exactly what a spec needs to exercise the forced-
 * change-password flow that the pre-seeded admin bypasses.
 *
 * Caller must ensure `request` already has the admin session cookie
 * (call `adminLoginAPI` first).
 */
export async function createForcedChangeUser(
  request: APIRequestContext,
  login: string,
  email: string,
): Promise<string> {
  const resp = await request.post('/api/v1/admin/users', {
    data: { login, email },
  });
  expect(
    resp.ok(),
    `create user failed: ${resp.status()} ${await resp.text()}`,
  ).toBeTruthy();
  const body = (await resp.json()) as { one_time_password: string };
  expect(body.one_time_password, 'server returned no one_time_password').toBeTruthy();
  return body.one_time_password;
}

/**
 * Wipe per-test DB state via the DEV-gated
 * POST /api/v1/admin/_reset endpoint and re-authenticate the caller.
 *
 * Preconditions: `request` must already carry a super-admin session
 * cookie (call `adminLoginAPI` first). The reset wipes the super-admin's
 * own session, so this helper re-runs `adminLoginAPI(request)` before
 * returning — the caller's next REST/UI action receives a live cookie.
 *
 * The endpoint only exists when the backend was started with
 * OMNIREPO_DEV=1. playwright.config.ts sets that env var for the
 * managed webServer. A 404 here indicates a prod-flavoured server;
 * fail loud.
 *
 * Canonical beforeEach pattern:
 *
 *   test.beforeEach(async ({ request }) => {
 *     await adminLoginAPI(request);
 *     await resetServerState(request);
 *   });
 *
 * Specs that need a non-admin actor (Phase 2 viewer tests, etc.) do:
 *   adminLoginAPI → resetServerState → createForcedChangeUser → loginAs(user)
 */
export async function resetServerState(
  request: APIRequestContext,
): Promise<void> {
  const resp = await request.post('/api/v1/admin/_reset');
  expect(
    resp.ok(),
    `resetServerState failed: ${resp.status()} ${await resp.text()}`,
  ).toBeTruthy();
  // Super-admin's session was wiped; re-authenticate so the next call
  // inherits a live cookie.
  await adminLoginAPI(request);
}

/**
 * seedUserWithProjectRole — creates a user via the admin API and adds them
 * to the given project with the specified role. Returns the one-time
 * password (OTP) from the admin user-creation endpoint, or null if either
 * step fails.
 *
 * Preconditions: `request` must carry a super-admin session cookie
 * (call `adminLoginAPI` first). The project must already exist.
 *
 * The returned OTP is the user's initial password. New users land in the
 * must_change_password=true state — callers that need a live session for
 * the created user should use `passwordLogin` which handles the
 * change-password redirect automatically.
 */
export async function seedUserWithProjectRole(
  request: APIRequestContext,
  login: string,
  role: 'viewer' | 'maintainer',
  projectName: string,
): Promise<string> {
  // Step 1: create the user. Fail loudly on any non-2xx — a silent null
  // return would leave the test with a stale login that's not actually a
  // project member, masking real failures (Codex Q8).
  const createResp = await request.post('/api/v1/admin/users', {
    data: { login, email: `${login}@e2e.test` },
  });
  if (!createResp.ok()) {
    const body = await createResp.text().catch(() => '');
    throw new Error(
      `seedUserWithProjectRole: admin user create for ${login} failed ` +
      `(status=${createResp.status()}): ${body}`,
    );
  }
  const body = await createResp.json() as { one_time_password?: string };
  const otp = body.one_time_password;
  if (!otp) {
    throw new Error(
      `seedUserWithProjectRole: admin user create for ${login} did not return ` +
      `one_time_password — cannot complete seeding flow`,
    );
  }

  // Step 2: add user to project with the requested role. Fail loudly on
  // non-2xx so the test surfaces the real cause instead of a silent null.
  const addResp = await request.post(
    `/api/v1/projects/${encodeURIComponent(projectName)}/members/${encodeURIComponent(login)}`,
    { data: { role } },
  );
  if (!addResp.ok()) {
    const body = await addResp.text().catch(() => '');
    throw new Error(
      `seedUserWithProjectRole: add ${login} to project ${projectName} ` +
      `with role=${role} failed (status=${addResp.status()}): ${body}`,
    );
  }

  return otp;
}

/**
 * passwordLogin — establish a non-super-admin session on the given page.
 *
 * Newly-seeded users land in must_change_password=true state. This helper
 * detects the /change-password redirect and completes the flow by setting
 * a new password derived from the OTP (appends "x" to avoid reuse checks).
 * After this call, `page` carries a valid session cookie for the given user
 * and is NOT on /login or /change-password.
 *
 * Returns true on success, false if the login or change-password step failed.
 *
 * Note: credentials are not logged; the OTP is only held in test scope and
 * discarded after the session cookie is set (T-V2-02 mitigated).
 */
export async function passwordLogin(
  page: Page,
  login: string,
  password: string,
): Promise<boolean> {
  await page.context().clearCookies();
  await page.goto('/login');
  await page.fill('input#login', login);
  await page.fill('input#password', password);
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle');

  // Handle must_change_password redirect.
  if (page.url().includes('/change-password')) {
    const newPw = `${password}x`;
    await page.fill('input#current-password', password);
    await page.fill('input#new-password', newPw);
    await page.fill('input#confirm-password', newPw);
    await page.click('button[type="submit"]');
    // Wait for navigation away from /change-password. networkidle alone is
    // insufficient — the button enters "Updating..." disabled state before the
    // redirect fires, so the idle window can close while we're still on the page.
    await page.waitForURL((url) => !url.pathname.includes('/change-password'), {
      timeout: 10_000,
    }).catch(() => {/* fall through to URL check below */});
    // After change-password succeeds, the session holds the user's identity.
    // Return whether we actually left the change-password page.
    return !page.url().includes('/change-password') && !page.url().includes('/login');
  }

  return !page.url().includes('/login');
}

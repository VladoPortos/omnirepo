/**
 * Playwright globalSetup — seeds the first super-admin via the
 * first-run `/api/v1/setup/superadmin` endpoint so the per-spec
 * `uiLoginAdmin` fixture can authenticate.
 *
 * The webServer in playwright.config.ts boots OmniRepo against a fresh
 * `DATA_ROOT=$(mktemp -d)` each run. Without this seed, the server has
 * zero users and every login attempt returns "auth.unauthenticated",
 * which silently stalls every spec on the /login page.
 *
 * Credentials (login=admin, password=changeme) match the exact shape
 * every e2e spec in `web/e2e/` expects (search the tree for
 * `'changeme'` — 15 spec files wire the same two-step bootstrap).
 */

import { request as playwrightRequest } from '@playwright/test';

const BASE_URL = process.env.OMNI_E2E_BASE_URL ?? 'https://localhost:8443';
const ADMIN_LOGIN = 'admin';
const ADMIN_EMAIL = 'admin@local';
// Every spec's `bootstrapAdmin` expects the admin password to end up as
// `AdminTest1!` (the constant `ADMIN_PW` defined in the specs). Seeding
// with that value directly short-circuits the changeme→AdminTest1!
// migration dance the specs perform — the `/setup/superadmin` endpoint
// creates the user with `must_change_password=0`, so the spec's
// intermediate change-password call would be skipped anyway.
const ADMIN_PASSWORD = 'AdminTest1!';

async function waitForHealthy(timeoutMs = 60_000): Promise<void> {
  const ctx = await playwrightRequest.newContext({
    baseURL: BASE_URL,
    ignoreHTTPSErrors: true,
  });
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const r = await ctx.get('/healthz');
      if (r.ok()) {
        await ctx.dispose();
        return;
      }
    } catch {
      // server not up yet; retry after tick
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  await ctx.dispose();
  throw new Error(`globalSetup: /healthz not ready within ${timeoutMs}ms`);
}

async function seedSuperAdmin(): Promise<void> {
  const ctx = await playwrightRequest.newContext({
    baseURL: BASE_URL,
    ignoreHTTPSErrors: true,
  });
  try {
    // If setup already ran (reuseExistingServer hit), status.needs_setup
    // is false and we skip — idempotent.
    const statusResp = await ctx.get('/api/v1/setup/status');
    if (!statusResp.ok()) {
      throw new Error(
        `globalSetup: GET /api/v1/setup/status -> ${statusResp.status()}`,
      );
    }
    const status = (await statusResp.json()) as { needs_setup: boolean };
    if (!status.needs_setup) {
      // eslint-disable-next-line no-console
      console.log(
        '[globalSetup] setup already completed (reusing existing server); skipping superadmin seed',
      );
      return;
    }
    const seedResp = await ctx.post('/api/v1/setup/superadmin', {
      data: {
        login: ADMIN_LOGIN,
        email: ADMIN_EMAIL,
        password: ADMIN_PASSWORD,
      },
    });
    if (!seedResp.ok()) {
      throw new Error(
        `globalSetup: POST /setup/superadmin -> ${seedResp.status()} ${await seedResp.text()}`,
      );
    }
    // eslint-disable-next-line no-console
    console.log(`[globalSetup] seeded super-admin ${ADMIN_LOGIN}`);
  } finally {
    await ctx.dispose();
  }
}

async function globalSetup(): Promise<void> {
  await waitForHealthy();
  await seedSuperAdmin();
}

export default globalSetup;

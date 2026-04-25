/**
 * v1.7 Phase 5 / BUNDLE-01..03 — vite manualChunks regression smoke.
 *
 * Loads three views that exercise the most chunk-sensitive code paths
 * after the Phase 5 split (react-vendor / tanstack / ui-base / radix /
 * lucide / dicebear / sanitize / vendor + shiki per-language dynamic
 * imports) and asserts the browser console reports no chunk-load
 * errors. Catches the canonical regression mode for misconfigured
 * manualChunks: "Failed to fetch dynamically imported module" thrown
 * when a chunk that's expected to be dynamically imported instead
 * gets statically referenced from the main entry but lives at a
 * stale hash on disk.
 */

import { expect, test } from '@playwright/test';
import { adminLoginAPI, adminLoginUI, resetServerState } from './helpers/auth';

test.use({ viewport: { width: 1440, height: 900 } });

test.describe('Bundle cold-reload smoke (BUNDLE-01..03)', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('dashboard + profile + swagger UI load with no chunk-load errors', async ({
    page,
  }) => {
    const failures: string[] = [];
    page.on('pageerror', (err) => failures.push(`pageerror: ${err.message}`));
    page.on('console', (msg) => {
      if (msg.type() !== 'error') return;
      const text = msg.text();
      // Only flag the regression-relevant errors. Fonts that 404 for
      // missing weights, third-party noise from extensions, etc. are
      // out of scope for this smoke.
      if (
        /Failed to fetch dynamically imported module/i.test(text) ||
        /ChunkLoadError/i.test(text) ||
        /Loading chunk \d+ failed/i.test(text)
      ) {
        failures.push(`console.error: ${text}`);
      }
    });

    await adminLoginUI(page);

    // 1. Dashboard — the cold-paint critical path. Pulls react-vendor,
    //    ui-base, tanstack, lucide, the dashboard page chunk, and the
    //    long-tail `vendor` catch-all all in parallel.
    await page.goto('/dashboard');
    await expect(page).toHaveURL(/\/dashboard/);

    // 2. Profile page — exercises @dicebear (avatar SVG generation).
    //    Confirms the dicebear chunk loads when the page lands.
    await page.goto('/profile');
    await expect(page).toHaveURL(/\/profile/);

    // 3. Swagger UI — served as static files from public/swagger via
    //    Go embed, NOT through the Vite bundle. Confirms the embed
    //    bundle still ships those assets after the Vite split (the
    //    prebuild copy-swagger step is unaffected by manualChunks
    //    but worth pinning since the handoff called it out).
    const swaggerResp = await page.goto('/api/docs');
    expect(swaggerResp?.status()).toBe(200);

    expect(
      failures,
      `unexpected console/page errors:\n${failures.join('\n')}`,
    ).toEqual([]);
  });
});

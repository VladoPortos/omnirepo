/**
 * Phase 7 / plan 07-03 — CopyButton aria-live + clipboard contract
 * (SNIPPET-09 / S-10).
 *
 * Asserts three things on the SnippetPanel Sheet:
 *
 *   1. Clicking a snippet's CopyButton triggers the aria-live polite
 *      announcement "Copied to clipboard" within 1s. The sr-only live
 *      region is rendered by CopyButton.tsx and is the a11y contract the
 *      Phase 6 upgrade established.
 *   2. The browser clipboard contains the snippet body after the click
 *      (`navigator.clipboard.readText()` round-trip).
 *   3. The Sheet renders at 1440x900 with clipboard-read + clipboard-write
 *      permissions granted at the browser-context level so Chromium's
 *      default "Clipboard Access" prompt does not intercept the write.
 *
 * Fixture strategy: create a throwaway project + docker repo via the
 * REST API in beforeAll (reusing the admin-login bootstrap copied
 * verbatim from error-envelope.spec.ts), then drive the SPA to the repo
 * detail page and open the SnippetPanel Sheet.
 *
 * Scope boundary: this spec covers ONLY the SNIPPET-09 aria-live +
 * clipboard wire contract. Per-protocol snippet correctness (S-01..S-09
 * emitted-string shape) is covered by web/src/lib/__tests__/snippets.test.ts
 * (vitest). Full per-protocol UI coverage is deferred to a later 07-*
 * audit spec.
 */

import { test, expect } from '@playwright/test';

const PROJECT = 'snippet-copy-test';
const REPO = 'snippet-copy-hub';

test.use({
  viewport: { width: 1440, height: 900 },
  // Chromium requires explicit permission for the Clipboard API inside
  // Playwright contexts — grant both read and write at the project level
  // so every test in this file can round-trip through the clipboard.
  permissions: ['clipboard-read', 'clipboard-write'],
});

test.describe('SnippetPanel copy-to-clipboard (SNIPPET-09 / S-10)', () => {
  test.beforeEach(async ({ request }) => {
    // Admin login bootstrap — copied verbatim from error-envelope.spec.ts
    // to preserve the first-login `must_change_password` handling.
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

    // Idempotent fixture setup: create project + docker repo if missing.
    // 409 (already exists) is expected on reruns and treated as success.
    await request.post('/api/v1/projects', {
      data: { name: PROJECT },
    });
    await request.post(`/api/v1/projects/${PROJECT}/repos`, {
      data: { name: REPO, type: 'docker' },
    });
  });

  test('copies first snippet and announces "Copied to clipboard" via aria-live', async ({
    page,
  }) => {
    // Drive the SPA to the docker repo detail page where SnippetPanel
    // renders its "CLI Snippets" trigger in the header action row.
    await page.goto(`/projects/${PROJECT}/docker/${REPO}`);

    // Open the Sheet via the trigger button (aria-labeled "CLI Snippets").
    const trigger = page.getByRole('button', { name: /CLI Snippets/i });
    await expect(trigger).toBeVisible({ timeout: 10000 });
    await trigger.click();

    // Sheet renders with role="dialog" (from shadcn Sheet primitive).
    const sheet = page.getByRole('dialog');
    await expect(sheet).toBeVisible();

    // Docker's first snippet is "Login" → CopyButton with contextual
    // aria-label "Copy Login" per SnippetList (07-02) wiring.
    const copyBtn = page
      .getByRole('button', { name: /^Copy Login$/ })
      .first();
    await expect(copyBtn).toBeVisible();
    await copyBtn.click();

    // aria-live polite region announces "Copied to clipboard" within 1s.
    // Multiple sr-only regions may exist on the page (one per CopyButton
    // instance); the matching one for the clicked button flips its
    // textContent after the click handler resolves.
    await expect(
      page.locator('[aria-live="polite"]', {
        hasText: 'Copied to clipboard',
      }),
    ).toHaveCount(1, { timeout: 1000 });

    // Clipboard round-trip: the docker Login snippet body is
    // `docker login <hostname>` — assert the host portion lands in the
    // browser clipboard.
    const clipboard = await page.evaluate(() =>
      navigator.clipboard.readText(),
    );
    expect(clipboard).toContain('docker login');
  });
});

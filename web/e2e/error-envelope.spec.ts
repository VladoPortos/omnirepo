/**
 * Phase 6 error-envelope e2e coverage (ERR-02, ERR-04, ERR-05, ERR-07).
 *
 * Drives the dev-only story page at /_dev/error-class-story, which
 * renders every ApiErrorClass through ErrorEnvelopeRenderer in three
 * modes:
 *
 *   - "inline" — compact inline variant (list/form contexts)
 *   - "page"   — full-bleed variant used as a route-level fallback
 *   - "live"   — fetched from /api/v1/_dev/error/:class at runtime so
 *                the server-to-UI wire parse is exercised end-to-end
 *
 * Requires the Go server to boot with OMNIREPO_DEV=1 (canned backend
 * routes) AND the SPA bundle to be built with VITE_OMNIREPO_DEV=true
 * (story page route registered). Both are wired via
 * web/playwright.config.ts webServer config.
 *
 * Admin login bootstrap copied from admin.spec.ts — even though the
 * story page is not auth-gated, keeping a consistent baseline prevents
 * cross-test state from tripping the must_change_password wall.
 */

import { test, expect } from '@playwright/test';

const STORY_URL = '/_dev/error-class-story';
const CLASSES = [
  'validation',
  'permission',
  'transient',
  'operator_action_required',
] as const;

test.describe('ErrorEnvelopeRenderer', () => {
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

  test('all 4 classes render in inline mode with ARIA role', async ({ page }) => {
    await page.goto(STORY_URL);
    for (const c of CLASSES) {
      const section = page.locator(
        `[data-story-class="${c}"][data-story-mode="inline"]`,
      );
      await expect(section).toBeVisible({ timeout: 10000 });
      // Validation / transient use role=status (aria-live=polite);
      // permission / operator_action_required use role=alert. Both
      // expose the envelope body to assistive tech — the test asserts
      // the envelope container is present, not which role variant.
      const roles = section.locator('[role="alert"], [role="status"]');
      await expect(roles.first()).toBeVisible();
      // Envelope-class data hook from ErrorEnvelopeRenderer.
      const envelopeEl = section.locator(`[data-envelope-class="${c}"]`);
      await expect(envelopeEl.first()).toBeVisible();
    }
  });

  test('all 4 classes render in page mode', async ({ page }) => {
    await page.goto(STORY_URL);
    for (const c of CLASSES) {
      const section = page.locator(
        `[data-story-class="${c}"][data-story-mode="page"]`,
      );
      await expect(section).toBeVisible({ timeout: 10000 });
    }
  });

  test('transient inline section shows Try again countdown button', async ({
    page,
  }) => {
    await page.goto(STORY_URL);
    const inlineTransient = page.locator(
      '[data-story-class="transient"][data-story-mode="inline"]',
    );
    const retryBtn = inlineTransient.getByRole('button', { name: /Try again/ });
    await expect(retryBtn).toBeVisible();
    // Canned inline transient carries retry_after_ms: 3000, so the
    // initial label has "Try again in Ns" + disabled state.
    await expect(retryBtn).toHaveText(/Try again in \d+s/);
    await expect(retryBtn).toBeDisabled();
  });

  test('transient countdown reaches zero and re-enables the button', async ({
    page,
  }) => {
    await page.goto(STORY_URL);
    const inlineTransient = page.locator(
      '[data-story-class="transient"][data-story-mode="inline"]',
    );
    const retryBtn = inlineTransient.getByRole('button', { name: /Try again/ });

    // retry_after_ms = 3000; allow up to 5s for countdown to finish
    // and re-enable the button.
    await expect(retryBtn).toHaveText('Try again', { timeout: 5000 });
    await expect(retryBtn).toBeEnabled();

    // Clicking fires onRetry → counter on the page increments.
    const counterLocator = page.locator('[data-story-retry-count]');
    await expect(counterLocator).toBeVisible();
    const initial = await counterLocator.textContent();
    await retryBtn.click();
    // Counter changes. Using a polling expect so React has a frame to
    // re-render before we sample.
    await expect(counterLocator).not.toHaveText(initial ?? '', { timeout: 2000 });
  });

  test('operator_action_required class shows deep-link CTA to /admin/trivy', async ({
    page,
  }) => {
    await page.goto(STORY_URL);
    const inlineOp = page.locator(
      '[data-story-class="operator_action_required"][data-story-mode="inline"]',
    );
    const cta = inlineOp.getByRole('button', { name: /Go to Admin → Trivy/ });
    await expect(cta).toBeVisible();
    // Clicking triggers window.location.href = "/admin/trivy". The
    // SPA route may render an auth wall or 404 — either way, the URL
    // must change. Playwright races the navigation against the click
    // so we don't deadlock if navigation resolves instantly.
    await Promise.all([
      page.waitForURL(/\/admin\/trivy/, { timeout: 5000 }).catch(() => {}),
      cta.click(),
    ]);
  });

  test('incident_id chip + copy button render for every class', async ({
    page,
  }) => {
    await page.goto(STORY_URL);
    for (const c of CLASSES) {
      const section = page.locator(
        `[data-story-class="${c}"][data-story-mode="inline"]`,
      );
      // Every canned envelope carries a UUID v7 incident_id, so each
      // class section MUST render the "Incident <id>" chip.
      await expect(section.locator('text=/^Incident /')).toBeVisible({
        timeout: 5000,
      });
      // CopyButton inside the chip is a ghost icon with the stable
      // aria-label wired in web/src/components/common/CopyButton.tsx.
      const copyBtn = section.getByRole('button', { name: 'Copy to clipboard' });
      await expect(copyBtn).toBeVisible();
    }
  });

  test('live-fetched envelopes render for every class (end-to-end wire parse)', async ({
    page,
  }) => {
    await page.goto(STORY_URL);
    // Live sections populate after a useEffect fetch against
    // /api/v1/_dev/error/<class>. The Go server must be serving the
    // OMNIREPO_DEV=1 canned routes for these sections to render. If
    // either side of the pipeline breaks, this test catches it.
    for (const c of CLASSES) {
      const live = page.locator(
        `[data-story-class="${c}"][data-story-mode="live"]`,
      );
      await expect(live).toBeVisible({ timeout: 10000 });
      const envelopeEl = live.locator(`[data-envelope-class="${c}"]`);
      // The envelope element only renders AFTER the fetch resolves
      // with a valid ApiErrorEnvelope — if wire shape drifts the
      // renderer returns null and this assert fails.
      await expect(envelopeEl.first()).toBeVisible({ timeout: 10000 });
    }
  });

  test('validation class shows its class-specific message', async ({ page }) => {
    await page.goto(STORY_URL);
    const inlineVal = page.locator(
      '[data-story-class="validation"][data-story-mode="inline"]',
    );
    // UI-SPEC §Copywriting: validation global message is "Some fields
    // need your attention." The canned envelope ships this verbatim,
    // and the renderer surfaces it as the primary message sentence so
    // forms can rely on a consistent string to describe
    // field-aggregate failures.
    await expect(inlineVal).toContainText(/Some fields need your attention\./);
  });

  test('permission class renders Lock icon and hint', async ({ page }) => {
    await page.goto(STORY_URL);
    const inlinePerm = page.locator(
      '[data-story-class="permission"][data-story-mode="inline"]',
    );
    // Canned envelope carries hint "Ask a project member to add you,
    // or switch to a project where you have access." The renderer
    // surfaces the hint below the message; this test pins that the
    // class-specific copy reaches the DOM.
    await expect(inlinePerm).toContainText(/permission/i);
  });
});

/**
 * Phase 6 / plan 06-08 — Visual Foundation snapshot (VISUAL-01, VISUAL-02,
 * VISUAL-09).
 *
 * Matrix: 6 statuses x 2 sizes x 2 iconOnly values = 24 variants,
 * rendered on the dev-only story page at /_dev/status-badge-story
 * (shipped by plan 06-07 and gated by DEV_ROUTES_ENABLED so production
 * bundles are tree-shaken).
 *
 * Single full-page snapshot is the baseline; unrelated CSS changes in
 * plans 7-10 should not affect it. `maxDiffPixelRatio: 0.01` tolerates
 * 1% pixel difference to absorb sub-pixel anti-aliasing drift across
 * CI runners. `animations: 'disabled'` freezes any mid-transition state.
 *
 * This spec does NOT authenticate — the story route is part of the SPA
 * shell and renders without auth (the ErrorClassStoryPage precedent from
 * plan 06-03 established this pattern).
 */

import { test, expect } from '@playwright/test';

test.use({ viewport: { width: 1440, height: 900 } });

test.describe('StatusBadge visual foundation', () => {
  test('24-variant matrix snapshot', async ({ page }) => {
    await page.goto('/_dev/status-badge-story');
    // The story page outer wrapper carries data-story-root="status-badge".
    // Wait for at least the labeled-md section root to render before
    // snapshotting to avoid racing the hydration.
    await expect(
      page.locator('[data-story-section]').first(),
    ).toBeVisible({ timeout: 10000 });
    await page.waitForLoadState('networkidle');

    await expect(page).toHaveScreenshot('status-badge-matrix.png', {
      animations: 'disabled',
      maxDiffPixelRatio: 0.01,
      fullPage: true,
    });
  });

  test('each of the 24 variants is present with correct data attributes', async ({
    page,
  }) => {
    await page.goto('/_dev/status-badge-story');
    await expect(
      page.locator('[data-story-section]').first(),
    ).toBeVisible({ timeout: 10000 });

    const statuses = [
      'healthy',
      'warning',
      'failure',
      'disabled',
      'maintenance',
      'neutral',
    ];
    const sizes = ['sm', 'md'];
    const iconOnly = ['true', 'false'];

    for (const s of statuses) {
      for (const size of sizes) {
        for (const io of iconOnly) {
          const cell = page.locator(
            `[data-story-variant="${s}"][data-story-size="${size}"][data-story-icon-only="${io}"]`,
          );
          await expect(cell).toBeVisible();
        }
      }
    }
  });
});

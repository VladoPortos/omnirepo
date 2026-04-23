/**
 * Phase 6 / plan 06-08 — Broad WCAG AA audit (VISUAL-08 breadth check).
 *
 * Runs axe-core against 5 key pages via @axe-core/playwright, tagged
 * `wcag2a` + `wcag2aa`. This is the sister breadth check to the fast
 * hard gate in scripts/check-contrast.mjs — that one validates the
 * 6 status-token pairs offline; this one crawls live pages to catch
 * missing aria-label, bad heading order, buttons-without-discernible
 * -text, form-label-association, color-contrast on non-status text,
 * etc.
 *
 * @axe-core/playwright is MPL-2.0 and stays in devDependencies ONLY.
 * Makefile target `lint-axe-devdep` enforces this at every `make test`.
 *
 * If axe reports violations, they are logged to stderr with help URLs
 * so the failure is actionable. `disableRules([])` is empty by design —
 * any real false-positives observed in future should be added with an
 * inline comment explaining the rationale, preferring to fix the
 * underlying markup whenever possible.
 */

import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { adminLoginAPI, resetServerState } from './helpers/auth';

test.describe('WCAG AA audit (broad)', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  const routes = [
    '/login',
    '/dashboard',
    '/projects',
    '/admin/users',
    '/admin/audit',
  ];

  for (const route of routes) {
    test(`${route} has no WCAG AA violations`, async ({ page }) => {
      await page.goto(route);
      await page.waitForLoadState('networkidle');
      // Tiny settle so lazy-loaded panels (TanStack Query refetches)
      // finish and axe gets a steady-state DOM to audit.
      await page.waitForTimeout(500);

      const results = await new AxeBuilder({ page })
        .withTags(['wcag2a', 'wcag2aa'])
        // No rule exclusions by default. Only add here if a real
        // false-positive is observed against shadcn's Radix primitives,
        // with an inline comment explaining why and linking to the
        // upstream axe-core / Radix discussion. Never whitelist your
        // way to green.
        .disableRules([])
        .analyze();

      if (results.violations.length > 0) {
        console.error(`[${route}] axe reported ${results.violations.length} violation(s):`);
        for (const v of results.violations) {
          console.error(`  - [${v.id}] ${v.impact ?? 'unknown'}: ${v.help}`);
          console.error(`      ${v.helpUrl}`);
          for (const n of v.nodes.slice(0, 5)) {
            console.error(`      target: ${n.target.join(', ')}`);
            console.error(`      html:   ${n.html.slice(0, 120)}`);
          }
          if (v.nodes.length > 5) {
            console.error(`      ... +${v.nodes.length - 5} more nodes`);
          }
        }
      }

      expect(
        results.violations,
        `${route}: axe-core reported ${results.violations.length} WCAG AA violation(s). ` +
          `See stderr above for rule IDs + help URLs.`,
      ).toEqual([]);
    });
  }
});

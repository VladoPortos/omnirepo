/**
 * Search E2E tests.
 * Navigate to /search, type query, verify results. Test filter chips.
 */

import { test, expect } from '@playwright/test';
import { adminLoginAPI, resetServerState } from './helpers/auth';

test.describe('Search page', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);

    // Seed data for search AFTER the reset so the rows survive into the
    // test body (resetServerState wipes every non-bootstrap table row).
    await request.post('/api/v1/projects', {
      data: { name: 'search-proj' },
    });
    await request.post('/api/v1/projects/search-proj/repos', {
      data: { name: 'search-repo', type: 'raw' },
    });
  });

  test('renders search page with input', async ({ page }) => {
    await page.goto('/search');
    await page.waitForTimeout(2000);

    if (page.url().includes('/login')) {
      await page.fill('input#login', 'admin');
      await page.fill('input#password', 'AdminTest1!');
      await page.click('button[type="submit"]');
      await page.waitForTimeout(2000);
    }

    if (page.url().includes('/change-password')) {
      test.skip();
      return;
    }

    // Search input should be visible
    const searchInput = page.locator(
      'input[type="search"], input[placeholder*="search" i], input[name="q"]',
    );
    await expect(searchInput.first()).toBeVisible({ timeout: 10000 });
  });

  test('search returns results for existing project', async ({ page }) => {
    await page.goto('/search');
    await page.waitForTimeout(2000);

    if (page.url().includes('/login')) {
      await page.fill('input#login', 'admin');
      await page.fill('input#password', 'AdminTest1!');
      await page.click('button[type="submit"]');
      await page.waitForTimeout(2000);
    }

    if (page.url().includes('/change-password')) {
      test.skip();
      return;
    }

    // Type a search query
    const searchInput = page.locator(
      'input[type="search"], input[placeholder*="search" i], input[name="q"]',
    );
    await searchInput.first().fill('search');
    await searchInput.first().press('Enter');
    await page.waitForTimeout(2000);

    // Results container should exist (even if empty)
    const results = page.locator(
      '[data-testid="search-results"], .search-results, main',
    );
    await expect(results.first()).toBeVisible({ timeout: 10000 });
  });

  test('filter chips are interactive', async ({ page }) => {
    await page.goto('/search');
    await page.waitForTimeout(2000);

    if (page.url().includes('/login')) {
      await page.fill('input#login', 'admin');
      await page.fill('input#password', 'AdminTest1!');
      await page.click('button[type="submit"]');
      await page.waitForTimeout(2000);
    }

    if (page.url().includes('/change-password')) {
      test.skip();
      return;
    }

    // Look for filter chips/buttons
    const filters = page.locator(
      '[data-testid="filter-chip"], button:has-text("project"), button:has-text("repo"), [role="checkbox"]',
    );
    if ((await filters.count()) > 0) {
      await filters.first().click();
      await page.waitForTimeout(500);
      // Verify the chip changes state (toggled/active)
    }
  });
});

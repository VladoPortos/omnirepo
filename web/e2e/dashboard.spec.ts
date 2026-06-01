/**
 * Dashboard E2E tests.
 *
 * Uses the shared adminLoginUI helper and tightens the heading locator
 * to `getByRole('heading')` so it doesn't collide with the sidebar's
 * "Dashboard" nav link.
 */

import { test, expect } from '@playwright/test';
import { adminLoginAPI, adminLoginUI, resetServerState } from './helpers/auth';

test.describe('Dashboard page', () => {
  test.beforeEach(async ({ page, request }) => {
    // Reset server state BEFORE the UI login — reset wipes every session
    // row so the page cookie would be invalidated anyway. adminLoginAPI
    // seeds the request context so resetServerState has a super-admin
    // cookie to present.
    await adminLoginAPI(request);
    await resetServerState(request);
    await adminLoginUI(page);
    await expect(page).not.toHaveURL(/\/change-password/, { timeout: 10_000 });
  });

  test('renders dashboard heading', async ({ page }) => {
    await page.goto('/');
    await expect(
      page.getByRole('heading', { name: 'Dashboard' }),
    ).toBeVisible({ timeout: 10_000 });
  });

  test('shows stat cards', async ({ page }) => {
    await page.goto('/');
    await expect(
      page.getByText(/storage|repositories|users/i).first(),
    ).toBeVisible({ timeout: 10_000 });
  });

  test('quick action buttons are visible', async ({ page }) => {
    await page.goto('/');
    // Wait for the dashboard heading to render so we know the SPA mounted.
    await expect(
      page.getByRole('heading', { name: 'Dashboard' }),
    ).toBeVisible({ timeout: 10_000 });
    // DashboardPage renders Create Project via a base-ui Button with
    // `render={<Link />}` — the resulting DOM shape has historically
    // flipped between `role="link"` and `role="button"` depending on
    // base-ui defaults, so the spec matches on visible text instead.
    await expect(page.getByText('Create Project')).toBeVisible({
      timeout: 10_000,
    });
  });
});

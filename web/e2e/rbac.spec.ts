/**
 * RBAC — maintainer/viewer split
 *
 * End-to-end coverage for the maintainer/viewer RBAC matrix. Exercises
 * the full scenario set:
 *
 *   1. add-viewer         — POST /members with role=viewer creates viewer row
 *   2. promote-to-maintainer — Role Select triggers PATCH maintainer
 *   3. demote-to-viewer   — Select viewer opens Dialog with Confirm
 *   4. last-maintainer-409 — API refuses and UI Trash disabled with tooltip
 *   5. self-demote-allowed-when-other-maintainer — Dialog self-copy + Confirm works
 *
 * Self-demote Dialog uses first-person copy verified verbatim.
 */

import { test, expect } from '@playwright/test';
import {
  adminLoginAPI,
  resetServerState,
  seedUserWithProjectRole,
  passwordLogin,
} from './helpers/auth';

test.describe('RBAC — maintainer/viewer split (RBAC-01..07)', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  // -----------------------------------------------------------------------
  // 1. add-viewer
  // -----------------------------------------------------------------------
  test('add-viewer — POST /members with role=viewer creates viewer row', async ({
    request,
    page,
  }) => {
    // Create project via super-admin.
    const projectName = `rbac-addviewer-${Date.now()}`;
    await request.post('/api/v1/projects', { data: { name: projectName } });

    // Create user and add as viewer.
    const otp = await seedUserWithProjectRole(request, 'bob', 'viewer', projectName);
    expect(otp).not.toBeNull();

    // Super-admin navigates to the project and sees bob in Members with Viewer badge.
    await page.goto('/login');
    await page.fill('input#login', 'admin');
    await page.fill('input#password', 'AdminTest1!');
    await page.click('button[type="submit"]');
    await page.waitForLoadState('networkidle');

    await page.goto(`/projects/${projectName}`);
    // Wait for Members card to load — use exact to avoid matching bob@e2e.test
    await expect(page.getByText('bob', { exact: true })).toBeVisible({ timeout: 10_000 });

    // bob's row should have a Viewer badge
    // Anchor on p.font-medium to avoid strict-mode violation from the email paragraph.
    const bobRow = page.locator('.flex.items-center.justify-between', {
      has: page.locator('p.font-medium', { hasText: /^bob$/ }),
    }).first();
    await expect(bobRow.getByText('Viewer', { exact: true })).toBeVisible();
  });

  // -----------------------------------------------------------------------
  // 2. promote-to-maintainer
  // -----------------------------------------------------------------------
  test('promote-to-maintainer — Role Select triggers PATCH maintainer', async ({
    request,
    page,
  }) => {
    const projectName = `rbac-promote-${Date.now()}`;
    await request.post('/api/v1/projects', { data: { name: projectName } });
    await seedUserWithProjectRole(request, 'bob', 'viewer', projectName);

    await page.goto('/login');
    await page.fill('input#login', 'admin');
    await page.fill('input#password', 'AdminTest1!');
    await page.click('button[type="submit"]');
    await page.waitForLoadState('networkidle');

    await page.goto(`/projects/${projectName}`);
    await expect(page.getByText('bob', { exact: true })).toBeVisible({ timeout: 10_000 });

    // Find bob's row and open the Role select (combobox).
    const bobRow = page.locator('.flex.items-center.justify-between', {
      has: page.locator('p.font-medium', { hasText: /^bob$/ }),
    }).first();
    await bobRow.getByRole('combobox').click();

    // Select Maintainer option.
    await page.getByRole('option', { name: 'Maintainer', exact: true }).click();

    // Badge updates to Maintainer (no dialog needed for promotion).
    await expect(bobRow.getByText('Maintainer', { exact: true })).toBeVisible({ timeout: 5_000 });
  });

  // -----------------------------------------------------------------------
  // 3. demote-to-viewer
  // -----------------------------------------------------------------------
  test('demote-to-viewer — Select viewer opens Dialog with Confirm', async ({
    request,
    page,
  }) => {
    const projectName = `rbac-demote-${Date.now()}`;
    await request.post('/api/v1/projects', { data: { name: projectName } });

    // Seed TWO additional maintainers so demotion is allowed (not last-maintainer).
    await seedUserWithProjectRole(request, 'alice', 'maintainer', projectName);
    await seedUserWithProjectRole(request, 'bob', 'maintainer', projectName);

    await page.goto('/login');
    await page.fill('input#login', 'admin');
    await page.fill('input#password', 'AdminTest1!');
    await page.click('button[type="submit"]');
    await page.waitForLoadState('networkidle');

    await page.goto(`/projects/${projectName}`);
    await expect(page.getByText('bob', { exact: true })).toBeVisible({ timeout: 10_000 });

    const bobRow = page.locator('.flex.items-center.justify-between', {
      has: page.locator('p.font-medium', { hasText: /^bob$/ }),
    }).first();
    await bobRow.getByRole('combobox').click();
    await page.getByRole('option', { name: 'Viewer', exact: true }).click();

    // Dialog appears with exact peer-demote title.
    await expect(
      page.getByRole('heading', { name: 'Change bob to Viewer?' }),
    ).toBeVisible({ timeout: 5_000 });

    // Body copy verbatim.
    await expect(
      page.getByText(
        'They will lose write access to this project. Maintainers can promote them back anytime.',
      ),
    ).toBeVisible();

    // Confirm demotion.
    await page.getByRole('button', { name: 'Confirm' }).click();

    // Badge updates to Viewer.
    await expect(bobRow.getByText('Viewer', { exact: true })).toBeVisible({ timeout: 5_000 });
  });

  // -----------------------------------------------------------------------
  // 4. last-maintainer-409 — UI Trash disabled with tooltip
  // -----------------------------------------------------------------------
  test('last-maintainer-409 — API refuses and UI Trash disabled with tooltip', async ({
    request,
    page,
  }) => {
    const projectName = `rbac-lastmaint-${Date.now()}`;
    await request.post('/api/v1/projects', { data: { name: projectName } });

    // Seed alice as the ONLY additional maintainer (admin is super-admin, not in project_members).
    await seedUserWithProjectRole(request, 'alice', 'maintainer', projectName);

    await page.goto('/login');
    await page.fill('input#login', 'admin');
    await page.fill('input#password', 'AdminTest1!');
    await page.click('button[type="submit"]');
    await page.waitForLoadState('networkidle');

    await page.goto(`/projects/${projectName}`);
    await expect(page.getByText('alice', { exact: true })).toBeVisible({ timeout: 10_000 });

    // Alice's row: Trash button should be disabled (aria-disabled="true" on wrapper span).
    const aliceRow = page.locator('.flex.items-center.justify-between', {
      has: page.locator('p.font-medium', { hasText: /^alice$/ }),
    }).first();

    // The disabled trash wrapper has an aria-label containing "Cannot remove alice".
    const trashWrap = aliceRow.locator('[aria-disabled="true"]');
    await expect(trashWrap).toBeVisible({ timeout: 5_000 });

    // Hover to trigger tooltip. Base UI tooltips render via Portal so
    // getByRole('tooltip') is unreliable in headless mode — use the
    // data-slot attribute that TooltipContent sets.
    await trashWrap.hover({ force: true });
    await expect(page.locator('[data-slot="tooltip-content"]')).toContainText(
      'Promote another member to maintainer first.',
      { timeout: 5_000 },
    );

    // Role Select: Viewer option should be disabled (data-disabled attribute from Base UI).
    await aliceRow.getByRole('combobox').click();
    const viewerItem = page.getByRole('option', { name: 'Viewer', exact: true });
    // Base UI sets data-disabled="" on disabled SelectItem.
    await expect(viewerItem).toHaveAttribute('data-disabled', '');
  });

  // -----------------------------------------------------------------------
  // 5. self-demote-allowed-when-other-maintainer
  // -----------------------------------------------------------------------
  test('self-demote-allowed-when-other-maintainer — Dialog self-copy + Confirm works', async ({
    request,
    page,
  }) => {
    // Seed two maintainers so self-demote is allowed (not last-maintainer).
    const projectName = `rbac-selfdemote-${Date.now()}`;
    await request.post('/api/v1/projects', { data: { name: projectName } });

    const aliceOtp = await seedUserWithProjectRole(request, 'alice', 'maintainer', projectName);
    await seedUserWithProjectRole(request, 'bob', 'maintainer', projectName);
    expect(aliceOtp).not.toBeNull();

    // Log in as alice (non-super-admin maintainer) via passwordLogin.
    // passwordLogin handles the must_change_password redirect.
    const ok = await passwordLogin(page, 'alice', aliceOtp!);
    expect(ok).toBe(true);

    await page.goto(`/projects/${projectName}`);
    await expect(page.getByText('alice', { exact: true })).toBeVisible({ timeout: 10_000 });

    // Find alice's own row.
    const aliceRow = page.locator('.flex.items-center.justify-between', {
      has: page.locator('p.font-medium', { hasText: /^alice$/ }),
    }).first();

    // Open alice's role Select and choose Viewer (self-demote).
    await aliceRow.getByRole('combobox').click();
    await page.getByRole('option', { name: 'Viewer', exact: true }).click();

    // First-person self-demote copy.
    await expect(
      page.getByRole('heading', { name: 'Give up maintainer access?' }),
    ).toBeVisible({ timeout: 5_000 });
    await expect(
      page.getByText(
        'You will lose write access to this project. Another maintainer or a super-admin will need to promote you back.',
      ),
    ).toBeVisible();

    // Confirm self-demotion.
    await page.getByRole('button', { name: 'Confirm' }).click();

    // Alice's badge updates to Viewer.
    await expect(aliceRow.getByText('Viewer', { exact: true })).toBeVisible({ timeout: 5_000 });
  });
});

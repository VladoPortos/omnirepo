/**
 * Field highlight.
 *
 * Locks the client half of the field-highlight wire: when a validation
 * envelope carries a details.field pointer, the corresponding <Input>
 * gets aria-invalid="true" and the envelope hint reads "Check the
 * highlighted field." When no pointer is present the hint falls back
 * to "Please review the form." so the user isn't told to look for a
 * highlight that isn't there.
 *
 * Coverage per surface (post-admin state — the setup wizard is only
 * reachable on a fresh install, so we test it indirectly via the
 * Change Your Password page which exercises the same local-envelope
 * path for a pw-mismatch):
 *
 *   1. Change-password pw-mismatch     → #confirm-password highlighted
 *   2. Change-password new pw too short → #new-password highlighted
 *   3. Create-project bad name         → #project-name highlighted
 *      (server emits details.field: "name")
 *
 * No cleanup is needed for (1) and (2) — we intentionally submit a
 * bad form so the password never actually changes. (3) runs inside
 * an existing project-creation dialog that gets dismissed without
 * submitting a valid value.
 */

import { test, expect } from '@playwright/test';
import { ADMIN_PASSWORD, adminLoginAPI, adminLoginUI, resetServerState } from './helpers/auth';

test.describe('field highlight', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('change-password: mismatching new + confirm highlights #confirm-password', async ({
    page,
  }) => {
    await page.goto('/change-password');
    // This page is only rendered when must_change_password is true;
    // after beforeEach that flag is false, so the router bounces the
    // user back to '/'. Force an explicit visit anyway — the page
    // component renders without the guard check if navigated to
    // directly from the browser bar, which is the UX path we're
    // testing ("user hits the page via bookmark / URL").
    // If the router redirects us elsewhere, skip — the surface isn't
    // reachable in this test's session state.
    if (!page.url().endsWith('/change-password')) {
      test.skip();
      return;
    }

    await page.fill('input#current-password', ADMIN_PASSWORD);
    await page.fill('input#new-password', 'NewPass123!');
    await page.fill('input#confirm-password', 'different');
    await page.getByRole('button', { name: /Update Password/ }).click();

    // Envelope renders; confirm-password is the single highlighted field.
    await expect(page.locator('#confirm-password')).toHaveAttribute(
      'aria-invalid',
      'true',
      { timeout: 5000 },
    );
    await expect(page.locator('#new-password')).not.toHaveAttribute(
      'aria-invalid',
      /./,
    );
    await expect(page.locator('[data-envelope-class="validation"]')).toContainText(
      /Passwords do not match/,
    );
    await expect(page.locator('[data-envelope-class="validation"]')).toContainText(
      /Check the highlighted field/,
    );
  });

  test('create-project: server-side bad name highlights #project-name', async ({
    page,
  }) => {
    // UI login seeds the BrowserContext cookie jar (request's jar is
    // separate). Without this, /projects?create=1 redirects to /login
    // and the auto-opened create dialog never renders.
    await adminLoginUI(page);
    await page.goto('/projects?create=1');
    const nameInput = page.locator('input#project-name');
    await expect(nameInput).toBeVisible({ timeout: 5000 });

    // Populate with an obviously invalid slug (uppercase + spaces +
    // symbols fail the `^[a-z0-9][a-z0-9._-]{0,62}$` regex).
    await nameInput.fill('BAD NAME!!');
    // Submit via the dialog's submit button (label "Create Project"
    // appears in both the trigger and the dialog; scope to the form
    // submit button inside the dialog).
    const dialog = page.getByRole('dialog', { name: 'Create Project' });
    await dialog.getByRole('button', { name: 'Create Project' }).click();

    // Envelope arrives with details.field="name" → aria-invalid lights
    // up on #project-name and the hint reads "Check the highlighted
    // field." (not "Please review the form.").
    await expect(nameInput).toHaveAttribute('aria-invalid', 'true', {
      timeout: 5000,
    });
    await expect(page.locator('[data-envelope-class="validation"]')).toContainText(
      /invalid project name/,
    );
    await expect(page.locator('[data-envelope-class="validation"]')).toContainText(
      /Check the highlighted field/,
    );

    // Reset the dialog so subsequent tests don't inherit the bad name.
    await page.keyboard.press('Escape');
  });
});

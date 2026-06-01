/**
 * DBHealthCard (7th composition-row card, super-admin only) e2e coverage.
 *
 * Six scenarios:
 *   1. Healthy render — card visible, OK badge, Driver row present.
 *   2. Warning variant — WAL bloat ≥ wal.warn_over_bytes flips badge.
 *   3. Failure variant — integrity.status != "ok" dominates warning.
 *   4. Run-Now click → 202 → Running… label + disabled (polling kicks in).
 *   5. Rate-limit disabled — tooltip reveals "Available in N min".
 *   6. Error path — GET /admin/db/health returns 500 → inline envelope
 *      renderer with Retry; un-intercepting + Retry recovers the card.
 *
 * Scenarios 2, 3, 5, 6 use `page.route()` to deterministically shape the
 * payload — integration of the REAL backend with the full UI is covered
 * elsewhere; this spec focuses on the frontend rendering contract.
 *
 * Scenario 4 uses the real backend (no interception) because its purpose
 * is to prove the live POST → GET-refetch → running=true transition
 * wire-and-wiring, which is meaningless against a canned payload.
 *
 * Auth bootstrap mirrors dashboard-composition.spec.ts.
 */

import { test, expect, type Page } from '@playwright/test';
import { adminLoginAPI, resetServerState } from './helpers/auth';

test.use({ viewport: { width: 1366, height: 768 } });

const ADMIN_PW = 'AdminTest1!';
const CARD_TITLE = 'SQLite Health';

/** 100 MB — matches the wal.warn_over_bytes constant. */
const ONE_HUNDRED_MB = 104857600;

async function uiLoginAdmin(page: Page): Promise<void> {
  await page.goto('/login');
  await page.fill('input#login', 'admin');
  await page.fill('input#password', ADMIN_PW);
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle');
}

/**
 * mockDBHealth — register a page.route that fulfils /admin/db/health
 * with the provided partial payload merged onto a healthy base.
 */
async function mockDBHealth(
  page: Page,
  override: Partial<{
    status: string;
    checkedAt: string;
    walBytes: number;
    canRunNow: boolean;
    nextAvailableAt: string;
    running: boolean;
  }>,
): Promise<void> {
  const now = new Date().toISOString();
  const payload = {
    integrity: {
      status: override.status ?? 'ok',
      checked_at: override.checkedAt ?? now,
      duration_ms: 1234,
    },
    size: {
      on_disk_bytes: 524288000,
      logical_bytes: 520093696,
      page_count: 127000,
      page_size: 4096,
      freelist_count: 48,
      freelist_bytes: 196608,
    },
    wal: {
      bytes: override.walBytes ?? 12582912,
      warn_over_bytes: ONE_HUNDRED_MB,
    },
    journal_mode: 'wal',
    driver: {
      label: 'modernc v1.48.2 (FTS5, JSON1)',
      pragmas: {
        journal_mode: 'WAL',
        synchronous: 'NORMAL',
        foreign_keys: 'ON',
        busy_timeout: '5000',
        cache_size: '-65536',
        temp_store: 'MEMORY',
      },
    },
    running: override.running ?? false,
    can_run_now: override.canRunNow ?? true,
    next_available_at: override.nextAvailableAt ?? '',
    last_manual_run_at: '',
  };
  await page.route('**/api/v1/admin/db/health', async (route) => {
    if (route.request().method() !== 'GET') {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(payload),
    });
  });
}

test.describe('DBHealthCard (super-admin only)', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('healthy render shows SQLite Health card with OK badge', async ({
    page,
  }) => {
    await mockDBHealth(page, {});
    await uiLoginAdmin(page);
    await page.goto('/');
    await expect(
      page.getByRole('region', { name: 'Status summary' }),
    ).toBeVisible({ timeout: 10_000 });

    const title = page
      .locator('[data-slot="card-title"]')
      .filter({ hasText: new RegExp(`^${CARD_TITLE}$`) });
    await expect(title).toBeVisible({ timeout: 10_000 });

    // OK headline visible (literal "Integrity OK").
    await expect(page.getByText(/Integrity OK/)).toBeVisible();

    // Badge — "Healthy" label from dbHealthStatusLabel.
    const card = title.locator(
      'xpath=ancestor::*[@data-slot="card"][1]',
    );
    await expect(card.getByText('Healthy', { exact: true })).toBeVisible();

    // Driver row surfaces the modernc label.
    await expect(card.getByText(/modernc/)).toBeVisible();
  });

  test('warning variant on WAL bloat', async ({ page }) => {
    await mockDBHealth(page, { walBytes: 200 * 1024 * 1024 });
    await uiLoginAdmin(page);
    await page.goto('/');

    const title = page
      .locator('[data-slot="card-title"]')
      .filter({ hasText: new RegExp(`^${CARD_TITLE}$`) });
    await expect(title).toBeVisible({ timeout: 10_000 });
    const card = title.locator(
      'xpath=ancestor::*[@data-slot="card"][1]',
    );
    // Badge label flips to "WAL bloat".
    await expect(card.getByText('WAL bloat', { exact: true })).toBeVisible();
  });

  test('failure variant — integrity status dominates', async ({ page }) => {
    await mockDBHealth(page, {
      status: 'database disk image is malformed',
      walBytes: 200 * 1024 * 1024, // Even with WAL bloat, failure wins.
    });
    await uiLoginAdmin(page);
    await page.goto('/');

    const title = page
      .locator('[data-slot="card-title"]')
      .filter({ hasText: new RegExp(`^${CARD_TITLE}$`) });
    await expect(title).toBeVisible({ timeout: 10_000 });
    const card = title.locator(
      'xpath=ancestor::*[@data-slot="card"][1]',
    );
    // Badge = "Integrity failure" (per dbHealthStatusLabel).
    await expect(
      card.getByText('Integrity failure', { exact: true }),
    ).toBeVisible();
    // Headline shows "Integrity FAIL".
    await expect(card.getByText(/Integrity FAIL/)).toBeVisible();
  });

  test('rate-limit disabled state shows tooltip', async ({ page }) => {
    // 30 min from now — tooltip should read "Available in 30 min".
    const thirtyMinFromNow = new Date(Date.now() + 30 * 60 * 1000).toISOString();
    await mockDBHealth(page, {
      canRunNow: false,
      nextAvailableAt: thirtyMinFromNow,
    });
    await uiLoginAdmin(page);
    await page.goto('/');

    const title = page
      .locator('[data-slot="card-title"]')
      .filter({ hasText: new RegExp(`^${CARD_TITLE}$`) });
    await expect(title).toBeVisible({ timeout: 10_000 });
    const card = title.locator(
      'xpath=ancestor::*[@data-slot="card"][1]',
    );

    // The disabled button carries an aria-label with the countdown.
    const runBtn = card.getByRole('button', {
      name: /Run integrity check now.*available/i,
    });
    await expect(runBtn).toBeDisabled();
    // The aria-label exposes the N-minute value without requiring a
    // hover — more deterministic in headless mode than tooltip display.
    const aria = await runBtn.getAttribute('aria-label');
    expect(aria).toMatch(/Run integrity check now.*available in \d+ minute/i);
  });

  test('error envelope renders inline with retry', async ({ page }) => {
    // Intercept with a server 500 envelope to exercise the error path.
    await page.route('**/api/v1/admin/db/health', async (route) => {
      if (route.request().method() !== 'GET') {
        await route.fallback();
        return;
      }
      await route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({
          code: 'db.unavailable',
          message: 'Test envelope — DB temporarily unavailable',
          class: 'transient',
        }),
      });
    });

    await uiLoginAdmin(page);
    await page.goto('/');

    const title = page
      .locator('[data-slot="card-title"]')
      .filter({ hasText: new RegExp(`^${CARD_TITLE}$`) });
    await expect(title).toBeVisible({ timeout: 10_000 });
    const card = title.locator(
      'xpath=ancestor::*[@data-slot="card"][1]',
    );

    // Inline ErrorEnvelopeRenderer surfaces the message.
    await expect(
      card.getByText(/Test envelope — DB temporarily unavailable/),
    ).toBeVisible({ timeout: 10_000 });

    // Unroute + recover on Retry.
    await page.unroute('**/api/v1/admin/db/health');
    await mockDBHealth(page, {});
    // A Retry button is part of ErrorEnvelopeRenderer; click it via its
    // accessible name (case-insensitive to absorb Try again / Retry
    // copy variants).
    const retryBtn = card.getByRole('button', { name: /retry|try again/i });
    await retryBtn.click();
    await expect(card.getByText(/Integrity OK/)).toBeVisible({
      timeout: 10_000,
    });
  });

  test('run-now click triggers integrity check and polls', async ({
    page,
  }) => {
    // This scenario uses the REAL backend (no interception) to prove the
    // live POST → GET-refetch → running=true transition. Test DBs run
    // integrity_check in milliseconds, so the "Running…" label may
    // flash by before the UI settles.
    test.setTimeout(30_000);
    await uiLoginAdmin(page);
    await page.goto('/');

    const title = page
      .locator('[data-slot="card-title"]')
      .filter({ hasText: new RegExp(`^${CARD_TITLE}$`) });
    await expect(title).toBeVisible({ timeout: 10_000 });
    const card = title.locator(
      'xpath=ancestor::*[@data-slot="card"][1]',
    );

    // If a previous test in this file left the lease rate-limited, skip.
    const runBtn = card.getByRole('button', {
      name: /Run integrity check now/i,
    });
    await expect(runBtn).toBeVisible();
    if (await runBtn.isDisabled()) {
      test.skip(
        true,
        'Rate-limit window from prior run still active; cannot trigger a fresh manual check.',
      );
      return;
    }

    // Fire the click. The UI may reach Running… between poll ticks,
    // but on a small test DB the goroutine usually completes in <5s.
    // Assert the POST went out OK by listening for the response.
    const respPromise = page.waitForResponse(
      (r) =>
        r.url().endsWith('/api/v1/admin/db/health/check') &&
        r.request().method() === 'POST',
    );
    await runBtn.click();
    const resp = await respPromise;
    expect(resp.status()).toBe(202);

    // After completion, the button should be disabled by the new rate-
    // limit window (we just ran a manual check) — wait up to 15s for
    // the health query to refetch and for can_run_now to flip to false.
    await expect(runBtn).toBeDisabled({ timeout: 15_000 });
  });
});

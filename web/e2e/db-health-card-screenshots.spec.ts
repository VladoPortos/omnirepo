/**
 * Phase 10 / plan 10-04 — walkthrough screenshot capture.
 *
 * NOT a regression gate. Captures visual evidence of the DBHealthCard's
 * 3 states (healthy / warning / failure) + rate-limit disabled button
 * for the 10-04 SUMMARY.md walkthrough artifact set.
 *
 * Skipped when EVIDENCE_OUT is unset so a plain `npx playwright test`
 * run doesn't pollute the artifact tree.
 */

import { test, type APIRequestContext, type Page } from '@playwright/test';
import { mkdirSync } from 'node:fs';
import path from 'node:path';

const EVIDENCE_OUT = process.env.EVIDENCE_OUT;

test.use({ viewport: { width: 1366, height: 768 } });

const ADMIN_PW = 'AdminTest1!';
const ONE_HUNDRED_MB = 104857600;

async function bootstrapAdmin(request: APIRequestContext): Promise<void> {
  const first = await request.post('/api/v1/auth/login', {
    data: { login: 'admin', password: 'changeme' },
  });
  if (first.ok()) {
    const body = await first.json();
    if (body.must_change_password) {
      await request.post('/api/v1/auth/change-password', {
        data: { current: 'changeme', new: ADMIN_PW },
      });
    }
  }
  await request.post('/api/v1/auth/login', {
    data: { login: 'admin', password: ADMIN_PW },
  });
}

async function uiLoginAdmin(page: Page): Promise<void> {
  await page.goto('/login');
  await page.fill('input#login', 'admin');
  await page.fill('input#password', ADMIN_PW);
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle');
}

async function mockDBHealth(
  page: Page,
  override: Partial<{
    status: string;
    walBytes: number;
    canRunNow: boolean;
    nextAvailableAt: string;
  }>,
): Promise<void> {
  const now = new Date().toISOString();
  const payload = {
    integrity: {
      status: override.status ?? 'ok',
      checked_at: now,
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
    running: false,
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

test.describe('DBHealthCard screenshot walkthrough (plan 10-04 evidence)', () => {
  test.skip(!EVIDENCE_OUT, 'EVIDENCE_OUT unset; skipping evidence capture.');
  if (EVIDENCE_OUT) {
    mkdirSync(EVIDENCE_OUT, { recursive: true });
  }

  test.beforeEach(async ({ request }) => {
    await bootstrapAdmin(request);
  });

  async function snap(page: Page, name: string): Promise<void> {
    if (!EVIDENCE_OUT) return;
    const card = page
      .locator('[data-slot="card-title"]')
      .filter({ hasText: /^SQLite Health$/ })
      .locator('xpath=ancestor::*[@data-slot="card"][1]');
    await card.waitFor({ state: 'visible', timeout: 10_000 });
    await card.screenshot({
      path: path.join(EVIDENCE_OUT, `${name}.png`),
    });
  }

  test('healthy variant', async ({ page }) => {
    await mockDBHealth(page, {});
    await uiLoginAdmin(page);
    await page.goto('/');
    await snap(page, '01-healthy');
  });

  test('warning variant (WAL bloat)', async ({ page }) => {
    await mockDBHealth(page, { walBytes: 200 * 1024 * 1024 });
    await uiLoginAdmin(page);
    await page.goto('/');
    await snap(page, '02-warning');
  });

  test('failure variant (integrity corruption)', async ({ page }) => {
    await mockDBHealth(page, {
      status: 'database disk image is malformed',
      walBytes: 200 * 1024 * 1024,
    });
    await uiLoginAdmin(page);
    await page.goto('/');
    await snap(page, '03-failure');
  });

  test('rate-limit disabled', async ({ page }) => {
    const thirtyMinFromNow = new Date(
      Date.now() + 30 * 60 * 1000,
    ).toISOString();
    await mockDBHealth(page, {
      canRunNow: false,
      nextAvailableAt: thirtyMinFromNow,
    });
    await uiLoginAdmin(page);
    await page.goto('/');
    await snap(page, '04-rate-limit');
  });
});

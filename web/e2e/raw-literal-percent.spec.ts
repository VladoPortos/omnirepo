/**
 * RAWFIX-03 (v1.7 Phase 2) — verify that a raw-protocol artifact whose
 * path contains a literal `%` character (not a percent-encoded one)
 * uploads, lists, and downloads end-to-end.
 *
 * Why this exists
 * ----------------
 * v1.6 Phase 7's `raw-download-encoded.spec.ts` (FRONTFIX-04) explicitly
 * scoped out literal `%` in raw paths because `validateRawPath` strict
 * mode rejected such uploads as "invalid percent-encoding". The root
 * cause: chi v5 already URL-decodes most safe percent-encodings (e.g.
 * `%25` → `%`) in wildcard params before the handler sees them, but the
 * strict block called `url.PathUnescape(seg)` AGAIN, which errored on
 * the now-literal `%` followed by non-hex chars.
 *
 * v1.7 Phase 2 changes the strict-mode logic so that a PathUnescape
 * failure falls through to the NUL-byte check rather than producing a
 * 400 (a segment that fails PathUnescape cannot be a residual %2e/%2E
 * traversal pattern, since those decode cleanly). This spec proves the
 * fix end-to-end against the SPA's RawRepoPage.
 *
 * Test strategy
 * --------------
 * 1. Upload a small file via PUT /<project>/raw/<repo>/<encoded-path>
 *    where the path contains a literal `%` in both a directory segment
 *    AND the filename. Pre-fix this returned 400 even though the
 *    encoded URL is syntactically valid.
 * 2. Sanity-check the upload via direct API GET (Basic auth).
 * 3. Drive the UI: navigate RawRepoPage into the directory, locate the
 *    download anchor for the file, assert its href is the per-segment
 *    encoded form (FRONTFIX-02 logic), and fetch the href.
 * 4. Assert bytes round-trip exactly.
 *
 * Negative coverage (regression guard for F-12.1):
 * 5. Attempt a PUT against a path containing `%2e%2e` (URL-encoded as
 *    `%252e%252e`); expect HTTP 400. The strict block must still reject
 *    the residual `%2e%2e` traversal even though chi preserved it
 *    literally. This guards against the fix accidentally weakening the
 *    F-12.1 traversal protection.
 */

import {
  expect,
  test,
  type APIRequestContext,
  type Page,
} from '@playwright/test';
import {
  ADMIN_LOGIN,
  ADMIN_PASSWORD,
  adminLoginAPI,
  resetServerState,
} from './helpers/auth';

test.use({ viewport: { width: 1440, height: 900 } });

const ADMIN_PW = 'AdminTest1!';

async function uiLoginAdmin(page: Page): Promise<void> {
  await page.goto('/login');
  await page.fill('input#login', 'admin');
  await page.fill('input#password', ADMIN_PW);
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle');
}

function encodePath(p: string): string {
  return p.split('/').map(encodeURIComponent).join('/');
}

function makeBytes(): Buffer {
  const out = Buffer.alloc(40);
  for (let i = 0; i < 40; i++) out[i] = (i * 11 + 17) & 0xff;
  return out;
}

async function basicAuth(): Promise<string> {
  return Buffer.from(`${ADMIN_LOGIN}:${ADMIN_PASSWORD}`).toString('base64');
}

async function uploadRaw(
  request: APIRequestContext,
  project: string,
  repo: string,
  relPath: string,
  body: Buffer,
): Promise<void> {
  const url = `/${encodeURIComponent(project)}/raw/${encodeURIComponent(
    repo,
  )}/${encodePath(relPath)}`;
  const auth = await basicAuth();
  const resp = await request.fetch(url, {
    method: 'PUT',
    data: body,
    headers: {
      'Content-Type': 'application/octet-stream',
      Authorization: `Basic ${auth}`,
    },
  });
  expect(
    resp.ok(),
    `upload PUT ${url} failed: ${resp.status()} ${await resp.text()}`,
  ).toBeTruthy();
}

test.describe('RAWFIX-03: raw upload + download with literal % in path', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
    await adminLoginAPI(request);
  });

  test('literal % in dir and filename round-trips bytes', async ({
    page,
    request,
  }) => {
    const project = `rawfix03-${Date.now()}`;
    const repo = 'literal-percent';
    await request.post('/api/v1/projects', { data: { name: project } });
    await request.post(`/api/v1/projects/${encodeURIComponent(project)}/repos`, {
      data: { name: repo, type: 'raw' },
    });

    // Literal `%` in BOTH directory and filename. URL form is
    // `100%25dir/name%25and.bin` (since `%` itself URL-encodes to `%25`),
    // which chi decodes to `100%dir/name%and.bin`. Pre-fix the strict
    // block re-decoded `name%and.bin`, errored on `%an`, and returned
    // 400. Post-fix the segment is accepted as a valid filename.
    const dirSeg = '100%dir';
    const fileSeg = 'name%and.bin';
    const relPath = `${dirSeg}/${fileSeg}`;

    const bytes = makeBytes();
    await uploadRaw(request, project, repo, relPath, bytes);

    const directUrl = `/${encodeURIComponent(project)}/raw/${encodeURIComponent(
      repo,
    )}/${encodePath(relPath)}`;
    const auth = await basicAuth();
    const directResp = await request.get(directUrl, {
      headers: { Authorization: `Basic ${auth}` },
    });
    expect(directResp.ok(), `direct GET ${directUrl} failed`).toBeTruthy();
    const directBody = await directResp.body();
    expect(
      Buffer.compare(directBody, bytes),
      'direct API GET bytes mismatch',
    ).toBe(0);

    await uiLoginAdmin(page);
    await page.goto(
      `/projects/${encodeURIComponent(project)}/raw/${encodeURIComponent(repo)}`,
    );

    const dirButton = page.getByRole('button', { name: dirSeg, exact: true }).first();
    await expect(dirButton).toBeVisible({ timeout: 10_000 });
    await dirButton.click();

    const fileRow = page.getByRole('row').filter({ hasText: fileSeg });
    await expect(fileRow).toBeVisible({ timeout: 10_000 });

    const downloadAnchor = fileRow.locator('a[title="Download"]').first();
    await expect(downloadAnchor).toBeVisible();
    const href = await downloadAnchor.getAttribute('href');
    expect(href, 'download anchor has no href').toBeTruthy();

    // Per-segment encodeURIComponent turns each literal `%` into `%25`.
    expect(href!).toContain('%25');

    const downloadResp = await request.get(href!, {
      headers: { Authorization: `Basic ${auth}` },
    });
    expect(
      downloadResp.ok(),
      `UI download href GET failed: ${downloadResp.status()}`,
    ).toBeTruthy();
    const downloadedBody = await downloadResp.body();
    expect(downloadedBody.length, 'downloaded length differs').toBe(bytes.length);
    expect(
      Buffer.compare(downloadedBody, bytes),
      'UI-href download bytes mismatch',
    ).toBe(0);
  });

  test('residual %2e%2e traversal still rejected on PUT (F-12.1 regression guard)', async ({
    request,
  }) => {
    const project = `rawfix03-trav-${Date.now()}`;
    const repo = 'trav-guard';
    await request.post('/api/v1/projects', { data: { name: project } });
    await request.post(`/api/v1/projects/${encodeURIComponent(project)}/repos`, {
      data: { name: repo, type: 'raw' },
    });

    // URL-encode `%2e%2e` literally as `%252e%252e` so chi decodes it to
    // `%2e%2e` (chi preserves %2e/%2E literally — that's the F-12.1
    // routing-layer protection). The strict block must then re-decode
    // and reject `..`. Asserts the v1.7 fix did NOT weaken F-12.1.
    const url = `/${encodeURIComponent(project)}/raw/${encodeURIComponent(
      repo,
    )}/foo/%252e%252e/escape.txt`;
    const auth = await basicAuth();
    const resp = await request.fetch(url, {
      method: 'PUT',
      data: Buffer.from('escape'),
      headers: {
        'Content-Type': 'application/octet-stream',
        Authorization: `Basic ${auth}`,
      },
    });
    expect(
      resp.status(),
      `expected 400 for %2e%2e traversal; got ${resp.status()}`,
    ).toBe(400);
  });
});

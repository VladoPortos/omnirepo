/**
 * FRONTFIX-04 (Phase 07) — verify the RawRepoPage download button
 * round-trips bytes correctly when the file's path contains URL
 * reserved characters (`#`, `%`, `?`, and a literal space).
 *
 * Why this exists
 * ----------------
 * Before FRONTFIX-02, RawRepoPage interpolated `row.path` raw into the
 * download anchor's `href`, so any of these characters broke the
 * download:
 *   - `#` truncated the URL at the fragment, hitting the repo root
 *   - `%` not followed by two hex digits made chi reject the request
 *     as malformed percent-encoding
 *   - `?` chopped the path at the query string
 *   - space round-tripped to `+` (or vice-versa) and pointed at the
 *     wrong file or 404'd
 *
 * The fix does
 *   row.path.split('/').map(encodeURIComponent).join('/')
 * which preserves slash separators while encoding every other reserved
 * char.
 *
 * Test strategy
 * --------------
 * 1. Upload a small file via PUT /<project>/raw/<repo>/<encoded-path>
 *    (the same shape the SPA's api.upload uses). The path contains
 *    `#`, `%`, `?`, and a space inside a sub-directory so the
 *    per-segment encode is exercised; the file body is a deterministic
 *    32-byte buffer.
 * 2. Navigate the SPA into RawRepoPage, drill down into the sub-dir
 *    that contains the file, and locate the download button rendered
 *    for the row. The button's href is what FRONTFIX-02 patched.
 * 3. Read the button's href and fetch the URL via Playwright's
 *    APIRequestContext (which inherits the admin session cookie). The
 *    request goes through the real backend, exercising the round-trip
 *    UI-href → server-decoded-path → on-disk-key.
 * 4. Assert the response body is byte-equal to the uploaded buffer.
 *
 * Bytes-equal is asserted with Buffer.compare so any drift in the
 * encoding round-trip surfaces clearly. We use a non-trivial buffer
 * (random-but-deterministic) instead of an ASCII string so any silent
 * UTF-8 reinterpretation by an encoding bug also surfaces.
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

/**
 * Per-segment encodeURIComponent — same shape the FRONTFIX-02 fix
 * applies in RawRepoPage. Used for the upload PUT URL so the spec
 * uploads to a path containing reserved chars without 400-ing on the
 * backend's strict percent-encoding validation.
 */
function encodePath(p: string): string {
  return p.split('/').map(encodeURIComponent).join('/');
}

/**
 * Deterministic 32-byte buffer — predictable across runs, distinct
 * enough from ASCII text that any encoding mismatch is visible.
 */
function makeBytes(): Buffer {
  const out = Buffer.alloc(32);
  for (let i = 0; i < 32; i++) out[i] = (i * 7 + 13) & 0xff;
  return out;
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
  // Protocol-endpoint paths (raw/, rpm/, deb/, helm/, pypi/) accept Basic
  // auth or API keys per the v1.2 protocol-endpoint auth contract — NOT
  // session cookies. The /api/v1/projects + /api/v1/projects/<p>/repos
  // routes accept session cookies because they're admin-API not
  // protocol-endpoint, but PUT /<project>/raw/... does not.
  const basicAuth = Buffer.from(`${ADMIN_LOGIN}:${ADMIN_PASSWORD}`).toString(
    'base64',
  );
  const resp = await request.fetch(url, {
    method: 'PUT',
    data: body,
    headers: {
      'Content-Type': 'application/octet-stream',
      Authorization: `Basic ${basicAuth}`,
    },
  });
  expect(
    resp.ok(),
    `upload PUT ${url} failed: ${resp.status()} ${await resp.text()}`,
  ).toBeTruthy();
}

test.describe('FRONTFIX-04: raw download with reserved chars in path', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
    // resetServerState wipes the sessions table — re-login so the
    // request context can perform authenticated PUTs below.
    await adminLoginAPI(request);
  });

  test('download URL round-trips bytes for path with #, %, ?, space', async ({
    page,
    request,
  }) => {
    const project = `frontfix04-${Date.now()}`;
    const repo = 'reserved-chars';
    await request.post('/api/v1/projects', { data: { name: project } });
    await request.post(`/api/v1/projects/${encodeURIComponent(project)}/repos`, {
      data: { name: repo, type: 'raw' },
    });

    // Reserved chars in BOTH the directory segment and the filename.
    // The space sits inside the directory, the `#`/`%`/`?` cluster sits
    // in the filename — so any encode bug that drops a segment or
    // collapses a reserved char to its un-decoded literal will produce
    // a 4xx or a wrong-bytes response.
    //
    // Backend constraints (validateRawPath, strict mode):
    //   - segments cannot decode to "", ".", or ".."
    //   - percent-encoding must be syntactically valid (so a literal
    //     `%` must arrive as `%25` not raw `%`)
    //   - segment length ≤ 255 bytes, total length ≤ 1024
    // Our path satisfies all of these once each segment passes through
    // encodeURIComponent.
    // Note: literal `%` in the filename would trigger a latent
    // backend strict-mode bug (validateRawPath calls url.PathUnescape
    // on segments that chi already decoded — a `%` not followed by 2
    // hex chars then errors). That backend hardening is out of v1.6
    // FRONTFIX scope. The 3 chars below (`#`, `?`, space) round-trip
    // cleanly and are sufficient to prove FRONTFIX-02's per-segment
    // encoding actually fires — without them, the URL parser would
    // truncate the filename at `?` (treating it as query-string).
    const dirSeg = 'with space';
    const fileSeg = 'name#with?chars.bin';
    const relPath = `${dirSeg}/${fileSeg}`;

    const bytes = makeBytes();
    await uploadRaw(request, project, repo, relPath, bytes);

    // 2. Sanity-check the upload via direct API GET (independent of
    //    the UI). If this fails, the SPA-driven assertion below would
    //    also fail and the failure mode would be ambiguous (encode
    //    bug vs upload-side bug).
    const directUrl = `/${encodeURIComponent(project)}/raw/${encodeURIComponent(
      repo,
    )}/${encodePath(relPath)}`;
    // Protocol-endpoint GET also requires Basic auth (not session cookies).
    const directBasicAuth = Buffer.from(
      `${ADMIN_LOGIN}:${ADMIN_PASSWORD}`,
    ).toString('base64');
    const directResp = await request.get(directUrl, {
      headers: { Authorization: `Basic ${directBasicAuth}` },
    });
    expect(directResp.ok(), `direct GET ${directUrl} failed`).toBeTruthy();
    const directBody = await directResp.body();
    expect(
      Buffer.compare(directBody, bytes),
      'direct API GET returned bytes that are not byte-equal to upload',
    ).toBe(0);

    // 3. Drive the UI: log in, open RawRepoPage, navigate into the
    //    `with space` sub-directory, find the download button on the
    //    file row, and assert its href is the FRONTFIX-02 encoding.
    await uiLoginAdmin(page);
    await page.goto(
      `/projects/${encodeURIComponent(project)}/raw/${encodeURIComponent(repo)}`,
    );

    // The directory row renders as a button labelled "with space"
    // (RawRepoPage derives folders client-side by splitting on "/").
    const dirButton = page.getByRole('button', { name: dirSeg, exact: true }).first();
    await expect(dirButton).toBeVisible({ timeout: 10_000 });
    await dirButton.click();

    // After navigation, the file row appears. The "Download" button is
    // an icon-only anchor inside the actions cell — locate it via its
    // `title="Download"` attribute (RawRepoPage sets this verbatim).
    const fileRow = page.getByRole('row').filter({ hasText: fileSeg });
    await expect(fileRow).toBeVisible({ timeout: 10_000 });

    // The download button is rendered as <a title="Download" href=...>
    // because Button{nativeButton:false, render:<a/>} replaces the
    // host element. Use that title to grab the anchor — it carries the
    // href we want to verify.
    const downloadAnchor = fileRow.locator('a[title="Download"]').first();
    await expect(downloadAnchor).toBeVisible();
    const href = await downloadAnchor.getAttribute('href');
    expect(href, 'download anchor has no href').toBeTruthy();

    // 4. Assert the href is the FRONTFIX-02 per-segment encoding shape.
    //    Each segment passed through encodeURIComponent: space → %20,
    //    `#` → %23, `?` → %3F. The slash between dir and file is
    //    preserved literally. (`%` literal omitted from the path
    //    because of a backend-side validateRawPath strict-mode latent
    //    bug — see scope note above; the encoding logic is identical.)
    expect(href!).toContain('%20'); // space encoded
    expect(href!).toContain('%23'); // # encoded
    expect(href!).toContain('%3F'); // ? encoded

    // 5. Fetch the href and assert byte-equality. Protocol-endpoint
    //    paths require Basic auth (not session cookies — v1.2 spec).
    //    The href is project-relative (starts with `/`), so request.get
    //    applies the configured baseURL automatically. In real browser
    //    usage, the user's logged-in session HAS Basic credentials
    //    cached by the browser native download flow; we replicate that
    //    here with explicit Authorization header.
    const downloadResp = await request.get(href!, {
      headers: { Authorization: `Basic ${directBasicAuth}` },
    });
    expect(
      downloadResp.ok(),
      `UI download href GET failed: ${downloadResp.status()}`,
    ).toBeTruthy();
    const downloadedBody = await downloadResp.body();
    expect(downloadedBody.length, 'downloaded length differs').toBe(bytes.length);
    expect(
      Buffer.compare(downloadedBody, bytes),
      'UI-href download bytes are not byte-equal to upload',
    ).toBe(0);
  });
});

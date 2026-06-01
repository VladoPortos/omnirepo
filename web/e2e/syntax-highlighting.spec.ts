/**
 * Verify Shiki syntax highlighting works in the *production* SPA build
 * for the Git file viewer.
 *
 * Why this exists
 * ----------------
 * The original implementation in web/src/lib/highlight.ts loaded
 * grammars via `await import(`@shikijs/langs/${lang}`)`. Vite's
 * production build emitted a "Missing './${lang}' specifier" warning
 * and at runtime every grammar load resolved to a missing chunk, so
 * highlighting silently degraded to plain text — code rendered but
 * the Shiki grammar tokens (the per-token <span style="color:#..."/>
 * elements) were absent. The repaired implementation uses an explicit
 * static-import map; this spec is the regression guard that proves the
 * Vite build emits real language chunks and the runtime can actually
 * load + apply them.
 *
 * Test strategy
 * --------------
 * Driving real Git through the e2e environment isn't feasible — the
 * managed webServer's empty data dir has no commits and pushing real
 * Git history per spec adds substantial flake surface. Instead we
 * mock the Git read-side API (the same approach
 * repo-detail-git-mirror-badge.spec.ts uses for similar reasons):
 *   1. POST /api/v1/projects/{p}/repos creates a real `git` repo
 *      (so the SPA route resolves and RBAC checks pass).
 *   2. page.route() intercepts the SPA's data-loader fetches:
 *        GET /repos/git/{repo}      → real Repo JSON
 *        GET /repos/git/{repo}/refs → one synthetic branch
 *        GET /repos/git/{repo}/tree/{ref}/      → root tree with
 *                                                  hello.go + index.ts
 *        GET /repos/git/{repo}/blob/{ref}/path  → file content +
 *                                                  encoding=utf-8
 *   3. The FileViewer component runs highlightCode(content, lang,
 *      theme) which goes through Shiki (real, no mock). DOMPurify
 *      then sanitises the result with allowlist {pre, code, span} +
 *      {class, style} attrs.
 *   4. The spec asserts the rendered DOM contains <span style="..."/>
 *      tokens with color values — those *only* exist if the grammar
 *      actually loaded and tokenized the source. A plain-text
 *      fallback emits a single <pre><code>...</code></pre> with no
 *      coloured spans.
 *
 * Both Go and TypeScript are verified because they exercise different
 * grammar chunks (`go.mjs` and `typescript.mjs`) loaded from different
 * dynamic-import call sites in highlight.ts — proving the static-map
 * fix works for more than one language.
 */

import { expect, test, type Page } from '@playwright/test';
import { adminLoginAPI, resetServerState } from './helpers/auth';

test.use({ viewport: { width: 1440, height: 900 } });

const ADMIN_PW = 'AdminTest1!';

async function uiLoginAdmin(page: Page): Promise<void> {
  await page.goto('/login');
  await page.fill('input#login', 'admin');
  await page.fill('input#password', ADMIN_PW);
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle');
}

/** Build the Repo JSON the UI receives from GET /repos/git/{repo}. */
function mockRepoPayload(name: string): Record<string, unknown> {
  const now = new Date().toISOString();
  return {
    id: 4242,
    project_id: 1,
    type: 'git',
    name,
    description_md: '',
    auto_scan: false,
    block_on_severity: 'none',
    public_read: false,
    size_bytes: 0,
    item_count: 0,
    created_at: now,
    is_mirror: false,
    mirror_upstream_url: '',
    mirror_filter_json: '',
    mirror_cred_id: null,
    scan_on_sync: false,
  };
}

const GO_SAMPLE = `package main

import "fmt"

func main() {
	const greeting = "hello, world"
	fmt.Println(greeting)
}
`;

const TS_SAMPLE = `import { useState } from 'react';

interface Counter {
  value: number;
}

export function useCounter(): Counter {
  const [value, setValue] = useState<number>(0);
  return { value };
}
`;

/**
 * Wire all four mocked GETs the GitRepoPage data-loaders fire.
 * Files served:
 *   - hello.go    (lang=go)
 *   - index.ts    (lang=typescript)
 */
async function wireMocks(
  page: Page,
  project: string,
  repo: string,
): Promise<void> {
  const base = `**/api/v1/projects/${encodeURIComponent(project)}/repos/git/${encodeURIComponent(repo)}`;

  // Repo metadata
  await page.route(base, (route) => {
    if (route.request().method() !== 'GET') return route.fallback();
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(mockRepoPayload(repo)),
    });
  });

  // Refs — one branch so the page selects it as default.
  await page.route(`${base}/refs`, (route) => {
    if (route.request().method() !== 'GET') return route.fallback();
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        items: [
          {
            name: 'main',
            type: 'branch',
            sha: '0000000000000000000000000000000000000000',
          },
        ],
      }),
    });
  });

  // Root tree listing — two files at the repo root.
  await page.route(`${base}/tree/main/`, (route) => {
    if (route.request().method() !== 'GET') return route.fallback();
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        items: [
          {
            name: 'hello.go',
            path: 'hello.go',
            type: 'blob',
            size: GO_SAMPLE.length,
            sha: '1111111111111111111111111111111111111111',
          },
          {
            name: 'index.ts',
            path: 'index.ts',
            type: 'blob',
            size: TS_SAMPLE.length,
            sha: '2222222222222222222222222222222222222222',
          },
        ],
      }),
    });
  });

  // useGitTree without trailing path also hits /tree/main with no
  // path segment after the slash. Some chi mounts strip the trailing
  // slash. Match both shapes defensively.
  await page.route(`${base}/tree/main`, (route) => {
    if (route.request().method() !== 'GET') return route.fallback();
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        items: [
          {
            name: 'hello.go',
            path: 'hello.go',
            type: 'blob',
            size: GO_SAMPLE.length,
            sha: '1111111111111111111111111111111111111111',
          },
          {
            name: 'index.ts',
            path: 'index.ts',
            type: 'blob',
            size: TS_SAMPLE.length,
            sha: '2222222222222222222222222222222222222222',
          },
        ],
      }),
    });
  });

  // Blob content. encoding='utf-8' so FileViewer reads `content`
  // verbatim instead of base64-decoding it.
  await page.route(`${base}/blob/main/hello.go`, (route) => {
    if (route.request().method() !== 'GET') return route.fallback();
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        name: 'hello.go',
        path: 'hello.go',
        sha: '1111111111111111111111111111111111111111',
        size: GO_SAMPLE.length,
        encoding: 'utf-8',
        content: GO_SAMPLE,
      }),
    });
  });

  await page.route(`${base}/blob/main/index.ts`, (route) => {
    if (route.request().method() !== 'GET') return route.fallback();
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        name: 'index.ts',
        path: 'index.ts',
        sha: '2222222222222222222222222222222222222222',
        size: TS_SAMPLE.length,
        encoding: 'utf-8',
        content: TS_SAMPLE,
      }),
    });
  });
}

test.describe('FRONTFIX-03: Shiki syntax highlighting in production', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('Go file renders with Shiki grammar tokens (not plain text)', async ({
    page,
    request,
  }) => {
    const project = `frontfix03-go-${Date.now()}`;
    const repo = 'highlight-go';
    await request.post('/api/v1/projects', { data: { name: project } });
    await request.post(`/api/v1/projects/${encodeURIComponent(project)}/repos`, {
      data: { name: repo, type: 'git' },
    });

    await wireMocks(page, project, repo);
    await uiLoginAdmin(page);
    await page.goto(
      `/projects/${encodeURIComponent(project)}/git/${encodeURIComponent(repo)}`,
    );

    // Wait for the file tree to render then click hello.go.
    const goLink = page.getByRole('button', { name: /^hello\.go$/ }).first();
    await expect(goLink).toBeVisible({ timeout: 10_000 });
    await goLink.click();

    // The file viewer's "Back" button is the rendered-state signal —
    // it only mounts once `viewingFile` is set and the FileViewer
    // body has rendered (the highlighting state shows a Skeleton, but
    // even during that render the Back button is mounted in the
    // header above).
    await expect(page.getByRole('button', { name: /Back/i })).toBeVisible({
      timeout: 5_000,
    });

    // Wait for highlighting to finish: Skeleton replaced by <pre>
    // produced by Shiki + DOMPurify. The container is the
    // sanitized-HTML mount in FileViewer.tsx; we anchor on the
    // <pre class="shiki ..."> Shiki always emits.
    const codeBlock = page.locator('pre.shiki, pre[class*="shiki"]').first();
    await expect(codeBlock).toBeVisible({ timeout: 15_000 });

    // The smoking-gun assertion: Shiki tokenizes source into
    // <span style="color:#..."> elements per token. Plain-text
    // fallback would emit zero such spans (the catch path in
    // FileViewer only writes <pre><code>...</code></pre>). Require
    // at least 5 coloured spans — trivial samples still tokenize
    // into well over that count for both Go and TypeScript.
    // Flake fix: Shiki's loadLanguage is async and may resolve AFTER
    // the <pre.shiki> element becomes visible but BEFORE tokenization
    // renders spans. Poll the span count instead of asserting once.
    const colouredSpans = codeBlock.locator('span[style*="color"]');
    await expect.poll(() => colouredSpans.count(), { timeout: 10_000 }).toBeGreaterThan(5);

    // Sanity: actual source text is present (tokenized but the
    // textContent concatenation reconstructs the original).
    await expect(codeBlock).toContainText('package main');
    await expect(codeBlock).toContainText('fmt.Println');
  });

  test('TypeScript file renders with Shiki grammar tokens (not plain text)', async ({
    page,
    request,
  }) => {
    const project = `frontfix03-ts-${Date.now()}`;
    const repo = 'highlight-ts';
    await request.post('/api/v1/projects', { data: { name: project } });
    await request.post(`/api/v1/projects/${encodeURIComponent(project)}/repos`, {
      data: { name: repo, type: 'git' },
    });

    await wireMocks(page, project, repo);
    await uiLoginAdmin(page);
    await page.goto(
      `/projects/${encodeURIComponent(project)}/git/${encodeURIComponent(repo)}`,
    );

    const tsLink = page.getByRole('button', { name: /^index\.ts$/ }).first();
    await expect(tsLink).toBeVisible({ timeout: 10_000 });
    await tsLink.click();

    await expect(page.getByRole('button', { name: /Back/i })).toBeVisible({
      timeout: 5_000,
    });

    const codeBlock = page.locator('pre.shiki, pre[class*="shiki"]').first();
    await expect(codeBlock).toBeVisible({ timeout: 15_000 });

    // Flake fix: Shiki's loadLanguage is async and may resolve AFTER
    // the <pre.shiki> element becomes visible but BEFORE tokenization
    // renders spans. Poll the span count instead of asserting once.
    const colouredSpans = codeBlock.locator('span[style*="color"]');
    await expect.poll(() => colouredSpans.count(), { timeout: 10_000 }).toBeGreaterThan(5);

    // Sanity: TypeScript-specific tokens visible in the rendered text.
    await expect(codeBlock).toContainText('interface Counter');
    await expect(codeBlock).toContainText('useState');
  });
});

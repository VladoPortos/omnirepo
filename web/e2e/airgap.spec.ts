/**
 * Air-gap verification test (D-48, D-49).
 * Reads all files in the built SPA dist/ directory and checks for external URLs.
 * Uses Node.js fs module directly -- no shell commands.
 */

import { test, expect } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';

function findExternalURLs(dir: string): string[] {
  const violations: string[] = [];
  // Match http:// and https:// URLs that do NOT point to loopback/test hosts.
  // Allowed hosts: localhost, 127.0.0.1, example.com, example.invalid, x.y
  const urlPattern =
    /https?:\/\/(?!localhost|127\.0\.0\.1|example\.com|example\.invalid|x\.y)[a-zA-Z0-9._~:/?#[\]@!$&'()*+,;=%\-]+/g;

  function walkDir(d: string) {
    let entries: fs.Dirent[];
    try {
      entries = fs.readdirSync(d, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      const fullPath = path.join(d, entry.name);
      if (entry.isDirectory()) {
        walkDir(fullPath);
      } else if (entry.isFile()) {
        // Skip binary files (images, fonts, etc.)
        const ext = path.extname(entry.name).toLowerCase();
        if (
          ['.png', '.jpg', '.jpeg', '.gif', '.ico', '.woff', '.woff2', '.ttf', '.eot', '.svg'].includes(ext)
        ) {
          continue;
        }
        try {
          const content = fs.readFileSync(fullPath, 'utf-8');
          const matches = content.match(urlPattern);
          if (matches) {
            for (const m of matches) {
              // Filter out source map comments which reference bundler internals
              if (m.includes('sourceMappingURL')) continue;
              violations.push(`${fullPath}: ${m}`);
            }
          }
        } catch {
          // Skip files that can't be read as UTF-8
        }
      }
    }
  }

  walkDir(dir);
  return violations;
}

test('no external URLs in built SPA (air-gap gate D-49)', () => {
  const distDir = path.resolve(__dirname, '../dist');
  if (!fs.existsSync(distDir)) {
    test.skip();
    return;
  }
  const violations = findExternalURLs(distDir);
  if (violations.length > 0) {
    console.error('Air-gap violations found:');
    for (const v of violations) {
      console.error(`  ${v}`);
    }
  }
  expect(violations).toHaveLength(0);
});

test('no CDN or external script tags in index.html', () => {
  const indexPath = path.resolve(__dirname, '../dist/index.html');
  if (!fs.existsSync(indexPath)) {
    test.skip();
    return;
  }
  const content = fs.readFileSync(indexPath, 'utf-8');

  // No <script src="https://..."> or <link href="https://...">
  const externalScripts = content.match(
    /<script[^>]+src=["']https?:\/\/(?!localhost|127\.0\.0\.1)/g,
  );
  const externalLinks = content.match(
    /<link[^>]+href=["']https?:\/\/(?!localhost|127\.0\.0\.1)/g,
  );

  expect(externalScripts ?? []).toHaveLength(0);
  expect(externalLinks ?? []).toHaveLength(0);
});

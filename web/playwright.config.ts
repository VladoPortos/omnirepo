import { defineConfig, devices } from '@playwright/test';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  // v1.5 Phase 1 / plan 01-04 — the DEV-only `POST /api/v1/admin/_reset`
  // endpoint invoked by every beforeEach wipes the sessions table
  // globally. With the default (adaptive) worker count, two tests running
  // in different workers can reset concurrently, each wiping the other's
  // live session → `resetServerState failed: 401 auth.unauthenticated`.
  // Pin to 1 worker so the reset contract is race-free. Combined with
  // fullyParallel: false this gives fully serial execution.
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: 'html',
  globalSetup: path.resolve(__dirname, './e2e/global-setup.ts'),
  use: {
    baseURL: process.env.OMNI_E2E_BASE_URL ?? 'https://localhost:8443',
    ignoreHTTPSErrors: true,
    trace: 'on-first-retry',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
  webServer: {
    // Phase 6 / plan 04 — three flags wire the dev-only surfaces into
    // the e2e run:
    //   VITE_OMNIREPO_DEV=true  — build the SPA with the dev-only
    //                              /_dev/error-class-story route so
    //                              Playwright can drive it
    //   OMNIREPO_DEV=1          — register /api/v1/_dev/error/:class
    //                              on the Go server so Live wire
    //                              fetches succeed
    //   OMNIREPO_DEV_PROXY=0    — keep the backend on the embedded
    //                              SPA handler (do NOT forward /_dev
    //                              requests to Vite on :5173, which
    //                              isn't running in the e2e suite)
    // All three flags are opt-in; a regular production build keeps
    // the dev surfaces tree-shaken (T-06-03-04).
    // Rewritten to be POSIX-sh compatible (no bash-only
    // `VAR=val (subshell)` prefix syntax) so spawn-from-playwright
    // works under dash as well as bash. DATA_ROOT is resolved via
    // inline $(mktemp -d) expansion, which works in both shells.
    command:
      'cd .. && cd web && npm ci --no-audit --no-fund && npm run build && cd .. && make build && export OMNIREPO_DATA_ROOT="$(mktemp -d)" && ./bin/omnirepo serve',
    env: {
      OMNIREPO_DEV: '1',
      OMNIREPO_DEV_PROXY: '0',
      VITE_OMNIREPO_DEV: 'true',
      // Allow CI / local runs to override ports when default 8080/8443
      // are held by another omnirepo instance (common in WSL where
      // orphaned dev servers can outlive their shell).
      OMNIREPO_SERVER__HTTP_PORT: process.env.OMNI_E2E_HTTP_PORT ?? '8080',
      OMNIREPO_SERVER__HTTPS_PORT: process.env.OMNI_E2E_HTTPS_PORT ?? '8443',
    },
    url: `${process.env.OMNI_E2E_BASE_URL ?? 'https://localhost:8443'}/healthz`,
    reuseExistingServer: !process.env.CI,
    timeout: 120000,
    ignoreHTTPSErrors: true,
  },
});

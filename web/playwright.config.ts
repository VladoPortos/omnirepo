import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: 'html',
  use: {
    baseURL: 'https://localhost:8443',
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
    command:
      'cd .. && VITE_OMNIREPO_DEV=true (cd web && npm ci --no-audit --no-fund && npm run build) && make build && DATA_ROOT=$(mktemp -d) OMNIREPO_DEV=1 OMNIREPO_DEV_PROXY=0 ./bin/omnirepo serve',
    env: {
      OMNIREPO_DEV: '1',
      OMNIREPO_DEV_PROXY: '0',
      VITE_OMNIREPO_DEV: 'true',
    },
    url: 'https://localhost:8443/healthz',
    reuseExistingServer: !process.env.CI,
    timeout: 120000,
    ignoreHTTPSErrors: true,
  },
});

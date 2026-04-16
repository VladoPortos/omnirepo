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
    command: 'cd .. && make build-all && DATA_ROOT=$(mktemp -d) ./bin/omnirepo serve',
    url: 'https://localhost:8443/healthz',
    reuseExistingServer: !process.env.CI,
    timeout: 120000,
    ignoreHTTPSErrors: true,
  },
});

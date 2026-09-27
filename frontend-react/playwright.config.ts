/**
 * Playwright projects (SPEC §3.3, §4.2):
 * - `stack`: the dashboard served by a running compose stack (`make demo`), at
 *   http://localhost:${DASHBOARD_HOST_PORT:-3000}. Specs live in e2e/stack/. `localhost` (not
 *   127.0.0.1) because the proxy's default TRUTHTABLE_ALLOWED_ORIGINS lists it, and the
 *   WebSocket handshake carries the page origin.
 * - `static` (the GitHub Pages build) arrives with the static site in P5.
 */
import { defineConfig, devices } from '@playwright/test';

const dashboardPort = process.env.DASHBOARD_HOST_PORT || '3000';

export default defineConfig({
  testDir: 'e2e',
  outputDir: 'test-results',
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: `http://localhost:${dashboardPort}`,
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'stack',
      testDir: 'e2e/stack',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});

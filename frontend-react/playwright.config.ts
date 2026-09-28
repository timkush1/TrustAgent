/**
 * Playwright projects (SPEC §3.3, §4.2):
 * - `stack`: the dashboard served by a running compose stack (`make demo`). Specs live in
 *   e2e/stack/. The base URL is PLAYWRIGHT_BASE_URL, else
 *   http://localhost:${DASHBOARD_HOST_PORT:-3000}. `localhost` (not 127.0.0.1) because the proxy's
 *   default TRUTHTABLE_ALLOWED_ORIGINS lists it, and the WebSocket handshake carries the page
 *   origin. The S0 exit gate runs `npx playwright test --project=stack -g skeleton`.
 * - `static`: the GitHub Pages build (recorded traffic, no backend) served by `vite preview` at
 *   http://localhost:4173/TrustAgent/. Its specs (e2e/static/) arrive with the static site in P5;
 *   until then the project holds one skipped placeholder.
 */
import { defineConfig, devices } from '@playwright/test';

const stackBaseURL =
  process.env.PLAYWRIGHT_BASE_URL || `http://localhost:${process.env.DASHBOARD_HOST_PORT || '3000'}`;
const staticBaseURL = 'http://localhost:4173/TrustAgent/';

export default defineConfig({
  testDir: 'e2e',
  outputDir: 'test-results',
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'stack',
      testDir: 'e2e/stack',
      use: { ...devices['Desktop Chrome'], baseURL: stackBaseURL },
    },
    {
      name: 'static',
      testDir: 'e2e/static',
      use: { ...devices['Desktop Chrome'], baseURL: staticBaseURL },
    },
  ],
});

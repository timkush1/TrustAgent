/**
 * Playwright projects (SPEC §3.3, §4.2):
 * - `stack`: the dashboard served by a running compose stack (`make demo`). Specs live in
 *   e2e/stack/. The base URL is PLAYWRIGHT_BASE_URL, else
 *   http://localhost:${DASHBOARD_HOST_PORT:-3000}. `localhost` (not 127.0.0.1) because the proxy's
 *   default TRUTHTABLE_ALLOWED_ORIGINS lists it, and the WebSocket handshake carries the page
 *   origin. The S0 exit gate runs `npx playwright test --project=stack -g skeleton`.
 * - `static`: the GitHub Pages build (`npm run build:static`: the landing page and the replay of
 *   the committed recording, no backend) served by `vite preview`, which answers missing paths
 *   with 404.html as Pages does. Specs live in e2e/static/. The base URL is
 *   PLAYWRIGHT_STATIC_BASE_URL, else http://localhost:${PLAYWRIGHT_STATIC_PORT:-4173}/TrustAgent/.
 *   With PLAYWRIGHT_STATIC_PREVIEW=1 Playwright starts the preview itself (`make test-e2e-static`
 *   builds first and does that).
 */
import { defineConfig, devices } from '@playwright/test';

const stackBaseURL =
  process.env.PLAYWRIGHT_BASE_URL || `http://localhost:${process.env.DASHBOARD_HOST_PORT || '3000'}`;
const staticPort = process.env.PLAYWRIGHT_STATIC_PORT || '4173';
const staticBaseURL = process.env.PLAYWRIGHT_STATIC_BASE_URL || `http://localhost:${staticPort}/TrustAgent/`;

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
  // The static site's preview, only when asked for (the stack project needs no server here).
  webServer: process.env.PLAYWRIGHT_STATIC_PREVIEW
    ? {
        command: `npx vite preview --mode static --port ${staticPort} --strictPort --host localhost`,
        url: staticBaseURL,
        reuseExistingServer: !process.env.CI,
        timeout: 60_000,
        env: { VITE_APP_MODE: 'static' },
      }
    : undefined,
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

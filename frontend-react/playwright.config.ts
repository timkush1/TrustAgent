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
 *   Playwright starts the preview itself when the command line selects the project
 *   (`npx playwright test --project=static`, as the M-Demo gate runs it after a static build) and
 *   PLAYWRIGHT_STATIC_BASE_URL does not point elsewhere, or with PLAYWRIGHT_STATIC_PREVIEW=1;
 *   PLAYWRIGHT_STATIC_PREVIEW=0 never starts it. Outside CI a preview already listening on the
 *   port is reused; in CI the port must be free.
 * - `launch`: the capture specs in e2e/launch/ (`npm run og`, `hero`, `screenshots`; D115) against the
 *   same static preview, built with `--mode launch` so the /og route renders.
 */
import { defineConfig, devices } from '@playwright/test';

const stackBaseURL =
  process.env.PLAYWRIGHT_BASE_URL || `http://localhost:${process.env.DASHBOARD_HOST_PORT || '3000'}`;
const staticPort = process.env.PLAYWRIGHT_STATIC_PORT || '4173';
const staticBaseURL = process.env.PLAYWRIGHT_STATIC_BASE_URL || `http://localhost:${staticPort}/TrustAgent/`;

/**
 * The project names a `playwright test` command line selects: `--project=a` and `--project a b`
 * (the option takes every following argument up to the next option). Empty when none is given.
 */
export function selectedProjects(argv: readonly string[]): string[] {
  const names: string[] = [];
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg.startsWith('--project=')) {
      names.push(arg.slice('--project='.length));
    } else if (arg === '--project') {
      while (i + 1 < argv.length && !argv[i + 1].startsWith('-')) names.push(argv[++i]);
    }
  }
  return names;
}

/** The projects that run against the static preview. */
export const STATIC_PREVIEW_PROJECTS: readonly string[] = ['static', 'launch'];

/** Whether this run serves the static site itself (see the header). */
export function wantsStaticPreview(argv: readonly string[], env: NodeJS.ProcessEnv): boolean {
  if (env.PLAYWRIGHT_STATIC_PREVIEW === '0') return false;
  if (env.PLAYWRIGHT_STATIC_PREVIEW) return true;
  if (env.PLAYWRIGHT_STATIC_BASE_URL) return false;
  // Project names match case-insensitively, as Playwright's --project filter does.
  return selectedProjects(argv).some((name) => STATIC_PREVIEW_PROJECTS.includes(name.toLowerCase()));
}

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
  // The static site's preview, only when the static project runs (the stack project needs no
  // server here).
  webServer: wantsStaticPreview(process.argv, process.env)
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
    {
      name: 'launch',
      testDir: 'e2e/launch',
      use: { ...devices['Desktop Chrome'], baseURL: staticBaseURL },
    },
  ],
});

// @vitest-environment node
/**
 * D137: every VITE_* value is inlined into the public bundle, so the build refuses a VITE_* name
 * that looks like it carries a secret (vite.site.ts secretEnvNames, which vite.config.ts runs over
 * the process environment and the .env files). vite.site.ts runs in Node at build time and the
 * app's tsconfig has no Node types, so it loads through a variable specifier (as plugins.test.ts
 * does).
 */
import { describe, expect, it } from 'vitest';

interface SiteModule {
  secretEnvNames: (names: Iterable<string>) => string[];
  SECRET_ENV_NAME_ALLOWED: ReadonlySet<string>;
}

const site = (await import(/* @vite-ignore */ new URL('../../vite.site.ts', import.meta.url).href)) as SiteModule;

/** The VITE_* names the app and its build read today (src/config/runtime.ts, vite.config.ts, vite/plugins). */
const USED = [
  'VITE_APP_MODE',
  'VITE_BASE_PATH',
  'VITE_DEMO_DATASET',
  'VITE_JEV_MODEL',
  'VITE_LAUNCH_ROUTES',
  'VITE_OPENROUTER_JUDGE_MODEL',
  'VITE_PLAYGROUND_MAX_CALLS',
  'VITE_PLAYGROUND_MAX_TOKENS',
  'VITE_REPLAY_SPEED',
  'VITE_REPO_URL',
  'VITE_SITE_URL',
];

describe('secret-looking VITE_* names (D137)', () => {
  it('names each one that looks like a key, token, secret or password, in any case, sorted', () => {
    expect(
      site.secretEnvNames(['VITE_SITE_URL', 'VITE_OPENROUTER_API_KEY', 'VITE_jev_token', 'VITE_Admin_Secret', 'VITE_DB_PASSWORD', 'VITE_APIKEY', 'VITE_APP_MODE']),
    ).toEqual(['VITE_APIKEY', 'VITE_Admin_Secret', 'VITE_DB_PASSWORD', 'VITE_OPENROUTER_API_KEY', 'VITE_jev_token']);
  });

  it('allows only the token cap, and ignores names that are not inlined', () => {
    expect([...site.SECRET_ENV_NAME_ALLOWED]).toEqual(['VITE_PLAYGROUND_MAX_TOKENS']);
    expect(site.secretEnvNames(['VITE_PLAYGROUND_MAX_TOKENS', 'OPENROUTER_API_KEY', 'TRUTHTABLE_JEV_API_KEY', 'ANTHROPIC_API_KEY'])).toEqual([]);
    // The allow-list is exact: a near name is refused.
    expect(site.secretEnvNames(['VITE_PLAYGROUND_MAX_TOKENS_2', 'vite_playground_max_tokens'])).toEqual(['VITE_PLAYGROUND_MAX_TOKENS_2']);
  });

  it('lets every name the app reads through', () => {
    expect(site.secretEnvNames(USED)).toEqual([]);
  });
});

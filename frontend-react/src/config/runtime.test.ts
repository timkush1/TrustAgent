import { describe, expect, it, vi } from 'vitest';
import {
  buildPlaygroundConfig,
  buildRuntimeConfig,
  buildSiteUrl,
  DEFAULT_REPO_URL,
  DEFAULT_SITE_URL,
  deriveWsUrl,
  loadRuntimeConfig,
  PLAYGROUND_DEFAULTS,
  type PageLocation,
} from './runtime';

const page = (href: string): PageLocation => {
  const u = new URL(href);
  return { href: u.href, origin: u.origin, protocol: u.protocol };
};
const env = { BASE_URL: '/' };

function fetchReturning(body: string, init: ResponseInit = {}) {
  return vi.fn(async () => new Response(body, init)) as unknown as typeof fetch;
}

describe('loadRuntimeConfig', () => {
  it('defaults to the same origin when config.json is missing', async () => {
    const cfg = await loadRuntimeConfig({
      fetchImpl: fetchReturning('not found', { status: 404 }),
      location: page('https://dash.example.com/app/traffic'),
      env,
    });
    expect(cfg).toMatchObject({
      apiBase: '',
      wsUrl: 'wss://dash.example.com/ws',
      crossOrigin: false,
      appMode: 'full',
      basePath: '/',
      repoUrl: DEFAULT_REPO_URL,
      source: 'defaults',
      warnings: [],
    });
  });

  it('fetches ${BASE_URL}config.json without caching', async () => {
    const fetchImpl = fetchReturning('{}', { headers: { 'Content-Type': 'application/json' } });
    await loadRuntimeConfig({ fetchImpl, location: page('http://localhost:3000/'), env: { BASE_URL: '/TrustAgent/' } });
    expect(fetchImpl).toHaveBeenCalledWith('/TrustAgent/config.json', expect.objectContaining({ cache: 'no-store' }));
  });

  it('applies apiBase and wsUrl from config.json', async () => {
    const cfg = await loadRuntimeConfig({
      fetchImpl: fetchReturning(JSON.stringify({ apiBase: 'https://proxy.example.com/', wsUrl: 'wss://proxy.example.com/ws' }), {
        headers: { 'Content-Type': 'application/json' },
      }),
      location: page('https://dash.example.com/'),
      env,
    });
    expect(cfg).toMatchObject({
      apiBase: 'https://proxy.example.com',
      wsUrl: 'wss://proxy.example.com/ws',
      crossOrigin: true,
      source: 'config.json',
    });
  });

  it('ignores an HTML fallback page served for the missing file', async () => {
    const cfg = await loadRuntimeConfig({
      fetchImpl: fetchReturning('<!doctype html>', { headers: { 'Content-Type': 'text/html' } }),
      location: page('http://localhost:5173/'),
      env,
    });
    expect(cfg).toMatchObject({ source: 'defaults', apiBase: '', wsUrl: 'ws://localhost:5173/ws', warnings: [] });
  });

  it('never throws: network errors, bad JSON and bad values become warnings', async () => {
    const down = await loadRuntimeConfig({
      fetchImpl: vi.fn(async () => {
        throw new TypeError('Failed to fetch');
      }) as unknown as typeof fetch,
      location: page('http://localhost:3000/'),
      env,
    });
    expect(down.source).toBe('defaults');
    expect(down.warnings[0]).toMatch(/could not be read/);

    const bad = await loadRuntimeConfig({
      fetchImpl: fetchReturning(JSON.stringify({ apiBase: 'javascript:alert(1)', wsUrl: 'http://nope' }), {
        headers: { 'Content-Type': 'application/json' },
      }),
      location: page('http://localhost:3000/'),
      env,
    });
    expect(bad).toMatchObject({ apiBase: '', wsUrl: 'ws://localhost:3000/ws', crossOrigin: false });
    expect(bad.warnings).toHaveLength(2);
  });
});

describe('buildRuntimeConfig', () => {
  it('reads build-time keys', () => {
    const cfg = buildRuntimeConfig(undefined, {
      location: page('http://localhost/'),
      env: { BASE_URL: '/TrustAgent', VITE_APP_MODE: 'static', VITE_REPO_URL: 'https://example.com/repo' },
    });
    expect(cfg).toMatchObject({ appMode: 'static', basePath: '/TrustAgent/', repoUrl: 'https://example.com/repo' });
  });

  it('accepts a path apiBase on the same origin', () => {
    const cfg = buildRuntimeConfig({ apiBase: '/trustagent/' }, { location: page('https://host.example/'), env });
    expect(cfg).toMatchObject({ apiBase: '/trustagent', crossOrigin: false, wsUrl: 'wss://host.example/trustagent/ws' });
  });

  it('rejects a config that is not an object of strings', () => {
    const cfg = buildRuntimeConfig({ apiBase: 42 }, { location: page('http://localhost/'), env });
    expect(cfg.source).toBe('defaults');
    expect(cfg.warnings).toHaveLength(1);
  });
});

describe('deriveWsUrl', () => {
  it('follows the page protocol for same-origin bases and the base protocol otherwise', () => {
    expect(deriveWsUrl('', page('http://localhost:5173/app'))).toBe('ws://localhost:5173/ws');
    expect(deriveWsUrl('https://api.example.com/x', page('http://localhost:5173/'))).toBe('wss://api.example.com/x/ws');
  });
});

describe('static live mode build config (D101, D137)', () => {
  it('uses the registry defaults when nothing is set', () => {
    expect(buildPlaygroundConfig({})).toEqual(PLAYGROUND_DEFAULTS);
    expect(buildSiteUrl({})).toBe(DEFAULT_SITE_URL);
  });

  it('clamps the caps to 1-100 calls and at most 8,000 tokens', () => {
    expect(buildPlaygroundConfig({ VITE_PLAYGROUND_MAX_CALLS: '500', VITE_PLAYGROUND_MAX_TOKENS: '64000' })).toMatchObject({ maxCalls: 100, maxTokens: 8000 });
    expect(buildPlaygroundConfig({ VITE_PLAYGROUND_MAX_CALLS: '0', VITE_PLAYGROUND_MAX_TOKENS: '0' })).toMatchObject({ maxCalls: 1, maxTokens: 1 });
    expect(buildPlaygroundConfig({ VITE_PLAYGROUND_MAX_CALLS: ' 12 ', VITE_PLAYGROUND_MAX_TOKENS: '4000' })).toMatchObject({ maxCalls: 12, maxTokens: 4000 });
    for (const bad of ['-5', '1e3', 'ten', '', '3.5']) {
      expect(buildPlaygroundConfig({ VITE_PLAYGROUND_MAX_CALLS: bad }).maxCalls).toBe(100);
    }
  });

  it('keeps each model in its family, else the default', () => {
    expect(buildPlaygroundConfig({ VITE_JEV_MODEL: 'typesafe/jev-2.0', VITE_OPENROUTER_JUDGE_MODEL: 'anthropic/claude-haiku-5' })).toMatchObject({
      jevModel: 'typesafe/jev-2.0',
      judgeModel: 'anthropic/claude-haiku-5',
    });
    expect(buildPlaygroundConfig({ VITE_JEV_MODEL: 'openai/gpt-5', VITE_OPENROUTER_JUDGE_MODEL: 'anthropic/claude-opus-5' })).toMatchObject({
      jevModel: PLAYGROUND_DEFAULTS.jevModel,
      judgeModel: PLAYGROUND_DEFAULTS.judgeModel,
    });
  });

  it('reads the site URL as an absolute URL ending in a slash', () => {
    expect(buildSiteUrl({ VITE_SITE_URL: 'https://example.github.io/TrustAgent' })).toBe('https://example.github.io/TrustAgent/');
    expect(buildSiteUrl({ VITE_SITE_URL: '/TrustAgent/' })).toBe(DEFAULT_SITE_URL);
    expect(buildSiteUrl({ VITE_SITE_URL: 'javascript:alert(1)' })).toBe(DEFAULT_SITE_URL);
  });
});

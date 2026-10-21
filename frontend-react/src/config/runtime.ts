/**
 * Runtime configuration (SPEC §3.3 "Shell and data layer", §2.7 dashboard keys).
 *
 * Precedence: `/config.json` (written at container start by the dashboard image from
 * DASHBOARD_API_BASE / DASHBOARD_WS_URL) → build-time `VITE_*` keys → defaults. No key or secret is
 * ever part of this file or the bundle. A missing, unreadable or invalid config.json is not an
 * error: the dashboard falls back to the same origin, which is what the dev server and the
 * same-origin nginx image both serve.
 *
 * config.json shape (every field optional; unknown fields are ignored):
 *   { "apiBase": "" | "https://proxy.example.com" | "/prefix",
 *     "wsUrl":   "" | "wss://proxy.example.com/ws" | "/prefix/ws" }
 */
import { z } from 'zod/mini';

export type AppMode = 'full' | 'static';

export interface RuntimeConfig {
  /** Prefix for REST paths (`${apiBase}/api/...`), without a trailing slash. "" = same origin. */
  apiBase: string;
  /** Absolute ws(s):// URL of the WebSocket endpoint, without query parameters. */
  wsUrl: string;
  /** The API is on another origin: key-header auth only, never cookies (SPEC §2.4). */
  crossOrigin: boolean;
  /** Build flavour (VITE_APP_MODE): the full dashboard or the static Pages site. */
  appMode: AppMode;
  /** Router base path (VITE_BASE_PATH through Vite's BASE_URL), always ending in "/". */
  basePath: string;
  repoUrl: string;
  /** Where the values came from, for the Settings "All configuration" view and debugging. */
  source: 'config.json' | 'defaults';
  /** Problems found while loading; never fatal. */
  warnings: string[];
}

export interface RuntimeEnv {
  BASE_URL: string;
  VITE_APP_MODE?: string;
  VITE_REPO_URL?: string;
}

export type PageLocation = Pick<Location, 'href' | 'origin' | 'protocol'>;

export interface LoadOptions {
  fetchImpl?: typeof fetch;
  location?: PageLocation;
  env?: RuntimeEnv;
  timeoutMs?: number;
}

export const DEFAULT_REPO_URL = 'https://github.com/timkush1/TrustAgent';
const CONFIG_TIMEOUT_MS = 3000;

const httpUrl = /^https?:\/\//i;
const wsUrl = /^wss?:\/\//i;

const ConfigFile = z.object({
  apiBase: z.optional(z.string().check(z.trim())),
  wsUrl: z.optional(z.string().check(z.trim())),
});

function viteEnv(): RuntimeEnv {
  return {
    BASE_URL: import.meta.env.BASE_URL,
    VITE_APP_MODE: import.meta.env.VITE_APP_MODE as string | undefined,
    VITE_REPO_URL: import.meta.env.VITE_REPO_URL as string | undefined,
  };
}

function stripTrailingSlash(s: string): string {
  return s.replace(/\/+$/, '');
}

/** Validates an apiBase value: "", an absolute http(s) URL, or an absolute path. */
function checkApiBase(value: string | undefined, warnings: string[]): string | undefined {
  if (value === undefined) return undefined;
  if (value === '') return '';
  if (httpUrl.test(value) || value.startsWith('/')) {
    try {
      new URL(value, 'http://placeholder.invalid');
      return stripTrailingSlash(value);
    } catch {
      // fall through
    }
  }
  warnings.push(`config.json apiBase ${JSON.stringify(value)} is not a URL or path; using the same origin`);
  return undefined;
}

/** Validates a wsUrl value: "", an absolute ws(s) URL, or an absolute path. */
function checkWsUrl(value: string | undefined, warnings: string[]): string | undefined {
  if (value === undefined || value === '') return undefined;
  if (wsUrl.test(value) || value.startsWith('/')) {
    try {
      new URL(value, 'ws://placeholder.invalid');
      return value;
    } catch {
      // fall through
    }
  }
  warnings.push(`config.json wsUrl ${JSON.stringify(value)} is not a ws(s) URL or path; deriving it`);
  return undefined;
}

/** `wss://host/prefix/ws` for an apiBase, following the page protocol for same-origin bases. */
export function deriveWsUrl(apiBase: string, location: Pick<PageLocation, 'href'>): string {
  const base = new URL(apiBase === '' ? '/' : `${apiBase}/`, location.href);
  base.protocol = base.protocol === 'https:' ? 'wss:' : 'ws:';
  base.pathname = `${stripTrailingSlash(base.pathname)}/ws`;
  base.search = '';
  base.hash = '';
  return base.toString();
}

function absoluteWsUrl(value: string, location: PageLocation): string {
  if (wsUrl.test(value)) return value;
  const u = new URL(value, location.href);
  u.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return u.toString();
}

export function buildRuntimeConfig(
  file: unknown,
  opts: { location: PageLocation; env: RuntimeEnv; warnings?: string[] },
): RuntimeConfig {
  const warnings = [...(opts.warnings ?? [])];
  let apiBase: string | undefined;
  let ws: string | undefined;
  let source: RuntimeConfig['source'] = 'defaults';

  if (file !== undefined) {
    const parsed = ConfigFile.safeParse(file);
    if (parsed.success) {
      source = 'config.json';
      apiBase = checkApiBase(parsed.data.apiBase, warnings);
      ws = checkWsUrl(parsed.data.wsUrl, warnings);
    } else {
      warnings.push('config.json is not an object with string apiBase/wsUrl; using defaults');
    }
  }

  const resolvedApiBase = apiBase ?? '';
  const crossOrigin =
    resolvedApiBase !== '' && new URL(resolvedApiBase, opts.location.href).origin !== opts.location.origin;
  const appMode: AppMode = opts.env.VITE_APP_MODE === 'static' ? 'static' : 'full';
  const basePath = opts.env.BASE_URL.endsWith('/') ? opts.env.BASE_URL : `${opts.env.BASE_URL}/`;

  return {
    apiBase: resolvedApiBase,
    wsUrl: ws ? absoluteWsUrl(ws, opts.location) : deriveWsUrl(resolvedApiBase, opts.location),
    crossOrigin,
    appMode,
    basePath,
    repoUrl: opts.env.VITE_REPO_URL || DEFAULT_REPO_URL,
    source,
    warnings,
  };
}

/**
 * Fetches `${BASE_URL}config.json` (no cache) and merges it over the defaults. Never throws.
 */
export async function loadRuntimeConfig(options: LoadOptions = {}): Promise<RuntimeConfig> {
  const env = options.env ?? viteEnv();
  const location = options.location ?? window.location;
  const fetchImpl = options.fetchImpl ?? window.fetch.bind(window);
  const warnings: string[] = [];
  let file: unknown;

  // The static site has no server to write config.json: it replays a recording, same origin.
  if (env.VITE_APP_MODE === 'static') return buildRuntimeConfig(undefined, { location, env, warnings });

  try {
    const res = await fetchImpl(`${env.BASE_URL.replace(/\/?$/, '/')}config.json`, {
      cache: 'no-store',
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
      signal: AbortSignal.timeout(options.timeoutMs ?? CONFIG_TIMEOUT_MS),
    });
    const type = res.headers.get('content-type') ?? '';
    if (res.status === 404) {
      // No runtime file (dev server, static build): defaults are the intended configuration.
    } else if (!res.ok) {
      warnings.push(`config.json returned HTTP ${res.status}; using defaults`);
    } else if (!type.includes('json')) {
      // A SPA fallback served index.html for the missing file.
    } else {
      file = await res.json();
    }
  } catch (err) {
    warnings.push(`config.json could not be read (${err instanceof Error ? err.message : String(err)}); using defaults`);
  }

  return buildRuntimeConfig(file, { location, env, warnings });
}

// ------------------------------------------------------------------ static live mode (build time)

/**
 * The static site's live-mode caps and models (D101, D137). They come only from build-time
 * `import.meta.env` (contracts/config/keys.v1.json), never from config.json or the URL, so a link
 * cannot raise a cap or swap a model. Out-of-range numbers are clamped; a model outside its
 * family falls back to the default. The caps are a courtesy, not a security boundary: the key's
 * own credit limit is the real one.
 */
export interface PlaygroundConfig {
  /** Billable calls to OpenRouter per tab, checks and AI drafts together (1-100). */
  maxCalls: number;
  /** Token cap per call (at most 8,000). */
  maxTokens: number;
  /** The decision model (`typesafe/jev-…`). */
  jevModel: string;
  /** The judge and drafter model (`anthropic/claude-haiku…`). */
  judgeModel: string;
}

export interface BuildEnv {
  VITE_PLAYGROUND_MAX_CALLS?: string;
  VITE_PLAYGROUND_MAX_TOKENS?: string;
  VITE_JEV_MODEL?: string;
  VITE_OPENROUTER_JUDGE_MODEL?: string;
  VITE_SITE_URL?: string;
}

/** The registry defaults (contracts/config/keys.v1.json). */
export const PLAYGROUND_DEFAULTS: Readonly<PlaygroundConfig> = {
  maxCalls: 100,
  maxTokens: 8000,
  jevModel: 'typesafe/jev-1.13',
  judgeModel: 'anthropic/claude-haiku-4.5',
};
export const PLAYGROUND_LIMITS = { maxCalls: { min: 1, max: 100 }, maxTokens: { min: 1, max: 8000 } } as const;
export const DEFAULT_SITE_URL = 'https://timkush1.github.io/TrustAgent/';

const IntText = z.string().check(z.trim(), z.regex(/^\d{1,9}$/));
const JevModel = z.string().check(z.trim(), z.regex(/^typesafe\/jev-[A-Za-z0-9._:-]+$/));
const JudgeModel = z.string().check(z.trim(), z.regex(/^anthropic\/claude-haiku[A-Za-z0-9._:-]*$/));
const SiteUrl = z.string().check(z.trim(), z.regex(/^https?:\/\/[^\s/?#]+(\/[^\s?#]*)?$/));

function clampInt(value: string | undefined, fallback: number, limits: { min: number; max: number }): number {
  const parsed = IntText.safeParse(value);
  if (!parsed.success) return fallback;
  return Math.min(limits.max, Math.max(limits.min, Number(parsed.data)));
}

export function buildPlaygroundConfig(env: BuildEnv): PlaygroundConfig {
  const jev = JevModel.safeParse(env.VITE_JEV_MODEL);
  const judge = JudgeModel.safeParse(env.VITE_OPENROUTER_JUDGE_MODEL);
  return {
    maxCalls: clampInt(env.VITE_PLAYGROUND_MAX_CALLS, PLAYGROUND_DEFAULTS.maxCalls, PLAYGROUND_LIMITS.maxCalls),
    maxTokens: clampInt(env.VITE_PLAYGROUND_MAX_TOKENS, PLAYGROUND_DEFAULTS.maxTokens, PLAYGROUND_LIMITS.maxTokens),
    jevModel: jev.success ? jev.data : PLAYGROUND_DEFAULTS.jevModel,
    judgeModel: judge.success ? judge.data : PLAYGROUND_DEFAULTS.judgeModel,
  };
}

/** VITE_SITE_URL, absolute and ending in "/": canonical and og:* URLs only, never requests. */
export function buildSiteUrl(env: BuildEnv): string {
  const parsed = SiteUrl.safeParse(env.VITE_SITE_URL);
  if (!parsed.success) return DEFAULT_SITE_URL;
  return parsed.data.endsWith('/') ? parsed.data : `${parsed.data}/`;
}

/** This build's live-mode caps and models. */
export const playground: Readonly<PlaygroundConfig> = Object.freeze(buildPlaygroundConfig(import.meta.env));
/** This build's published site URL. */
export const siteUrl: string = buildSiteUrl(import.meta.env);

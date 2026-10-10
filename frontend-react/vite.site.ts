/**
 * The two HTML entries and where they land (SPEC §3.3 "Other routes", §7.5 static demo):
 *
 * - Full build (the dashboard image): the app at `/` (index.html), the landing page at `/welcome/`.
 * - Static build (VITE_APP_MODE=static, GitHub Pages under VITE_BASE_PATH=/TrustAgent/): the landing
 *   page is the site's root, the replay app lives at `/app/` (plus a copy per main page, so
 *   /TrustAgent/app/traffic/ answers 200), and `404.html` is a copy of the app so GitHub Pages
 *   boots it for every other deep link (/TrustAgent/app/traffic/<id>). Both pages carry the
 *   static CSP as a meta tag and `referrer: no-referrer`; the site talks to itself and, from P5,
 *   openrouter.ai only.
 *
 * `vite preview` of a static build emulates GitHub Pages: a path with no file gets 404.html with
 * status 404 (the Playwright `static` project runs against it).
 */
import { copyFileSync, existsSync, mkdirSync, readFileSync, renameSync, statSync } from 'node:fs';
import { dirname, join, resolve, sep } from 'node:path';
import type { Plugin, ResolvedConfig } from 'vite';

export type AppMode = 'full' | 'static';

/** SPEC §7.5, verbatim. */
export const STATIC_CSP =
  "default-src 'self'; connect-src 'self' https://openrouter.ai; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'";

/**
 * Static build: the app's main pages get their own copy of app/index.html, so the links people
 * share and the nav's own URLs answer 200 on GitHub Pages; any other deep link (a trace) is served
 * by 404.html, which boots the same app.
 */
export const STATIC_APP_PAGES = [
  'app/overview',
  'app/traffic',
  'app/playground',
  'app/evaluators/studio',
  'app/savings',
  // The static PKCE callback (D105): a real file, so OpenRouter's redirect lands on a 200.
  'connect/callback',
] as const;

/**
 * Build-time secret-name guard (D137): no `VITE_*` name may look like it carries a secret, because
 * every `VITE_*` value is inlined into the public bundle. VITE_PLAYGROUND_MAX_TOKENS is a cap, not
 * a token, and is the one name allowed.
 */
export const SECRET_ENV_NAME = /KEY|TOKEN|SECRET|PASSWORD/;
export const SECRET_ENV_NAME_ALLOWED: ReadonlySet<string> = new Set(['VITE_PLAYGROUND_MAX_TOKENS']);

/** The `VITE_*` names that look like secrets (empty when the build may go on). */
export function secretEnvNames(names: Iterable<string>): string[] {
  return [...names].filter((n) => n.startsWith('VITE_') && SECRET_ENV_NAME.test(n.toUpperCase()) && !SECRET_ENV_NAME_ALLOWED.has(n)).sort();
}

/** Build output path of each HTML entry. */
export function pagePaths(mode: AppMode): { app: string; landing: string; notFound: string | null } {
  return mode === 'static'
    ? { app: 'app/index.html', landing: 'index.html', notFound: '404.html' }
    : { app: 'index.html', landing: 'welcome/index.html', notFound: null };
}

function move(outDir: string, from: string, to: string): void {
  if (from === to) return;
  const target = join(outDir, to);
  mkdirSync(dirname(target), { recursive: true });
  renameSync(join(outDir, from), target);
}

/** `import s from 'virtual:trustagent/scenario/<id>'`: one scenario of the demo pack. */
export const SCENARIO_MODULE = 'virtual:trustagent/scenario/';

/**
 * Serves one scenario of contracts/demo/scenarios.v1.json as a module, so the landing page ships
 * the refund example (about 1 KB) instead of the whole pack, which grows with every scenario and
 * would eat the landing's 110 KB budget. An unknown id fails the build.
 */
export function scenarioModules(contractsDir: string): Plugin {
  const pack = join(contractsDir, 'demo/scenarios.v1.json');
  return {
    name: 'trustagent:scenario-modules',
    resolveId(source) {
      return source.startsWith(SCENARIO_MODULE) ? `\0${source}` : undefined;
    },
    load(id) {
      if (!id.startsWith(`\0${SCENARIO_MODULE}`)) return undefined;
      const wanted = id.slice(1 + SCENARIO_MODULE.length);
      this.addWatchFile(pack);
      const doc = JSON.parse(readFileSync(pack, 'utf8')) as { scenarios?: { id?: unknown }[] };
      const scenario = doc.scenarios?.find((x) => x.id === wanted);
      if (!scenario) this.error(`contracts/demo/scenarios.v1.json has no scenario ${JSON.stringify(wanted)}`);
      return `export default ${JSON.stringify(scenario)};\n`;
    },
  };
}

/**
 * `import { copy } from 'virtual:trustagent/copy/<path>,<path>'`: only those parts of
 * contracts/ui/copy.v1.json (dotted paths under `copy`).
 */
export const COPY_MODULE = 'virtual:trustagent/copy/';

/**
 * Serves the listed parts of contracts/ui/copy.v1.json as `copy`, so the landing page ships the
 * words it shows instead of the whole tree, which grows with every dashboard page and would eat
 * the landing's 110 KB budget. An unknown path fails the build.
 */
export function copyModules(contractsDir: string): Plugin {
  const file = join(contractsDir, 'ui/copy.v1.json');
  return {
    name: 'trustagent:copy-modules',
    resolveId(source) {
      return source.startsWith(COPY_MODULE) ? `\0${source}` : undefined;
    },
    load(id) {
      if (!id.startsWith(`\0${COPY_MODULE}`)) return undefined;
      this.addWatchFile(file);
      const all = (JSON.parse(readFileSync(file, 'utf8')) as { copy: Record<string, unknown> }).copy;
      return `export const copy = ${JSON.stringify(pickCopy(all, id.slice(1 + COPY_MODULE.length).split(','), (path) => this.error(`contracts/ui/copy.v1.json has no copy.${path}`)))};\n`;
    },
  };
}

/** The parts of `all` at `paths` (dotted), in the same nesting; `missing` is called for a bad path. */
export function pickCopy(all: Record<string, unknown>, paths: string[], missing: (path: string) => never): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const path of paths) {
    const keys = path.split('.');
    let value: unknown = all;
    for (const k of keys) {
      if (value === null || typeof value !== 'object' || Array.isArray(value) || !Object.hasOwn(value, k)) missing(path);
      value = (value as Record<string, unknown>)[k];
    }
    let into = out;
    for (const k of keys.slice(0, -1)) into = (into[k] ??= {}) as Record<string, unknown>;
    into[keys[keys.length - 1]] = value;
  }
  return out;
}

export function sitePages(mode: AppMode): Plugin[] {
  let config: ResolvedConfig;
  const paths = pagePaths(mode);
  return [
    {
      name: 'trustagent:site-pages',
      apply: 'build',
      enforce: 'post',
      configResolved(resolved) {
        config = resolved;
      },
      transformIndexHtml() {
        if (mode !== 'static') return undefined;
        return [
          { tag: 'meta', attrs: { 'http-equiv': 'Content-Security-Policy', content: STATIC_CSP }, injectTo: 'head-prepend' },
          { tag: 'meta', attrs: { name: 'referrer', content: 'no-referrer' }, injectTo: 'head-prepend' },
        ];
      },
      writeBundle() {
        const outDir = resolve(config.root, config.build.outDir);
        if (!existsSync(join(outDir, 'index.html')) || !existsSync(join(outDir, 'landing.html'))) return;
        // index.html is the app entry and landing.html the landing entry (rolldownOptions.input).
        if (mode === 'static') {
          move(outDir, 'index.html', paths.app);
          move(outDir, 'landing.html', paths.landing);
          copyFileSync(join(outDir, paths.app), join(outDir, '404.html'));
          for (const page of STATIC_APP_PAGES) {
            mkdirSync(join(outDir, page), { recursive: true });
            copyFileSync(join(outDir, paths.app), join(outDir, page, 'index.html'));
          }
        } else {
          move(outDir, 'landing.html', paths.landing);
        }
      },
    },
    {
      name: 'trustagent:pages-preview',
      configurePreviewServer(server) {
        if (mode !== 'static') return undefined;
        // Runs after the static-file and html-fallback middlewares (which map a directory to its
        // index.html) and before Vite serves that html: a path with no file is a GitHub Pages 404.
        return () => {
          server.middlewares.use((req, res, next) => {
            const outDir = resolve(server.config.root, server.config.build.outDir);
            const page = join(outDir, '404.html');
            let pathname = '/';
            try {
              pathname = decodeURIComponent(new URL(req.url ?? '/', 'http://preview.invalid').pathname);
            } catch {
              // A malformed path is a 404 like any other missing file.
            }
            const file = resolve(outDir, `.${pathname}`);
            const inside = file === outDir || file.startsWith(outDir + sep);
            // Pages redirects a directory asked for without its slash (/app/traffic → /app/traffic/).
            if (inside && !pathname.endsWith('/') && existsSync(join(file, 'index.html'))) {
              // originalUrl still has the base path Vite strips from req.url.
              const original = (req as { originalUrl?: string }).originalUrl ?? req.url ?? '/';
              const url = new URL(original, 'http://preview.invalid');
              res.statusCode = 301;
              res.setHeader('Location', `${url.pathname}/${url.search}`);
              res.end();
              return;
            }
            if ((req.method !== 'GET' && req.method !== 'HEAD') || !existsSync(page) || (inside && existsSync(file) && statSync(file).isFile())) {
              next();
              return;
            }
            res.statusCode = 404;
            res.setHeader('Content-Type', 'text/html; charset=utf-8');
            res.end(req.method === 'HEAD' ? undefined : readFileSync(page));
          });
        };
      },
    },
  ];
}

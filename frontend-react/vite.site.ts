/**
 * The two HTML entries and where they land (SPEC §3.3 "Other routes", §7.5 static demo):
 *
 * - Full build (the dashboard image): the app at `/` (index.html), the landing page at `/welcome/`.
 * - Static build (VITE_APP_MODE=static, GitHub Pages under VITE_BASE_PATH=/TrustAgent/): the landing
 *   page is the site's root, the replay app lives at `/app/`, and `404.html` is a copy of the app so
 *   GitHub Pages boots it for every deep link (/TrustAgent/app/traffic/<id>). Both pages carry the
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

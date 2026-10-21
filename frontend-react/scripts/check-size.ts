/**
 * `npm run size`: the bundle budgets (SPEC §3.3; D112), blocking. Run it after a build (npm run
 * build:static, or npm run build).
 *
 * Budgets (gzip -9), measured on feat/v2-int6 at Step 0b (2026-10-10, static build):
 * - landing first load <= 110 kB (SPEC; measured 104.9 kB, full build 104.8): its HTML, the theme-init
 *   script, its entry chunk with every statically imported chunk, and their CSS. Fonts are listed
 *   but not counted (woff2, shared with the dashboard, fetched per subset in use); chunks the page
 *   loads after its first render are listed, not counted. All landing headroom is WEB-LANDING's.
 * - app entry <= measured + 6 kB (measured 243.9 kB, full build 243.7; WEB-REPLAY 4, WEB-LIVE 1, WEB-STUDIO 1):
 *   the dashboard's HTML, entry chunk, static imports and CSS.
 * - every lazy JS chunk <= 130 kB, or the largest P4 chunk + 5 kB when P4 already exceeds 130
 *   (measured largest 120.5 kB, the Overview chunk: the budget is 130 kB).
 * - no module of src/lib/{openrouter,gate}/** or src/components/replay/live/** in the landing first
 *   load or the app entry (walked through .vite/modules.json, vite.site.ts moduleMap), and the
 *   LiveKeyChip only behind a dynamic import;
 * - no rel=preconnect or dns-prefetch to openrouter.ai in any HTML (it would contact openrouter.ai
 *   before consent);
 * - dist total <= 10 MB (every file but .vite/).
 *
 *   node scripts/check-size.ts [--dist dist] [--budget-kb 110]
 */
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';
import { gzipSync } from 'node:zlib';

interface ManifestChunk {
  file: string;
  src?: string;
  isEntry?: boolean;
  isDynamicEntry?: boolean;
  imports?: string[];
  dynamicImports?: string[];
  css?: string[];
  assets?: string[];
}

/** The figures measured at Step 0b, in bytes (gzip -9); the budgets derive from them. */
export const MEASURED = { landing: 104_921, app: 243_853, largestLazy: 120_505 } as const;
export const BUDGETS = {
  landing: 110_000,
  app: MEASURED.app + 6_000,
  lazy: Math.max(130_000, MEASURED.largestLazy + 5_000),
  distTotal: 10_000_000,
} as const;
/** Live-mode modules that never load up front (D99, D112). */
export const FORBIDDEN_UP_FRONT = /^src\/(lib\/(openrouter|gate)\/|components\/replay\/live\/)/;
export const LIVE_KEY_CHIP = 'src/components/replay/live/LiveKeyChip.tsx';

const web = fileURLToPath(new URL('../', import.meta.url));
const { values } = parseArgs({
  options: {
    dist: { type: 'string', default: join(web, 'dist') },
    'budget-kb': { type: 'string' },
  },
});
const dist = values.dist;
const landingBudget = values['budget-kb'] ? Number(values['budget-kb']) * 1000 : BUDGETS.landing;

const manifestPath = join(dist, '.vite/manifest.json');
const modulesPath = join(dist, '.vite/modules.json');
if (!existsSync(manifestPath) || !existsSync(modulesPath)) {
  console.error(`size: ${manifestPath} or modules.json not found; run \`npm run build:static\` (or \`npm run build\`) first.`);
  process.exit(2);
}
const manifest = JSON.parse(readFileSync(manifestPath, 'utf8')) as Record<string, ManifestChunk>;
const modules = JSON.parse(readFileSync(modulesPath, 'utf8')) as Record<string, string[]>;

const gz = (path: string) => gzipSync(readFileSync(path), { level: 9 }).length;
const kb = (n: number) => `${(n / 1000).toFixed(1)} kB`;
const failures: string[] = [];

/** The files an entry loads up front: its chunk, static imports (transitively) and their CSS. */
function closure(key: string): { files: Set<string>; fonts: Set<string> } {
  const files = new Set<string>();
  const fonts = new Set<string>();
  const seen = new Set<string>();
  const walk = (k: string) => {
    if (seen.has(k)) return;
    seen.add(k);
    const c = manifest[k];
    if (!c) throw new Error(`manifest has no chunk ${k}`);
    files.add(c.file);
    for (const css of c.css ?? []) files.add(css);
    for (const a of c.assets ?? []) if (/\.(woff2?|ttf|otf)$/.test(a)) fonts.add(a);
    for (const i of c.imports ?? []) walk(i);
  };
  walk(key);
  return { files, fonts };
}

function htmlOf(candidates: string[]): string | null {
  return candidates.map((p) => join(dist, p)).find((p) => existsSync(p)) ?? null;
}

function report(title: string, key: string, html: string | null): number {
  const { files, fonts } = closure(key);
  const rows: [string, number][] = [...files].sort().map((f) => [f, gz(join(dist, f))]);
  if (html) rows.unshift([html.slice(dist.length + 1), gz(html)]);
  const theme = join(dist, 'theme-init.js');
  if (existsSync(theme)) rows.push(['theme-init.js', gz(theme)]);
  const total = rows.reduce((n, [, s]) => n + s, 0);
  console.log(`\n${title}`);
  for (const [f, s] of rows) console.log(`  ${kb(s).padStart(9)}  ${f}`);
  console.log(`  ${kb(total).padStart(9)}  total, gzip -9`);
  if (fonts.size > 0) {
    // CSS asset paths in the manifest are relative to dist.
    const fontBytes = [...fonts].reduce((n, f) => n + (existsSync(join(dist, f)) ? readFileSync(join(dist, f)).length : 0), 0);
    console.log(`  fonts: ${fonts.size} woff2 files, ${kb(fontBytes)} on disk (not counted; the browser loads only the subsets in use)`);
  }
  return total;
}

function budget(name: string, value: number, limit: number): void {
  console.log(`${name}: ${kb(value)} of ${kb(limit)}`);
  if (value > limit) failures.push(`${name} is ${kb(value - limit)} over its ${kb(limit)} gzip budget (D112)`);
}

const landingKey = Object.keys(manifest).find((k) => manifest[k].isEntry && k.endsWith('landing.html'));
const appKey = Object.keys(manifest).find((k) => manifest[k].isEntry && k.endsWith('index.html'));
if (!landingKey || !appKey) {
  console.error('size: the build has no landing.html or index.html entry');
  process.exit(2);
}
// Static build: the landing page is the root index.html and the app is app/index.html; full build:
// the app is index.html and the landing page welcome/index.html.
const staticLayout = existsSync(join(dist, 'app/index.html'));
console.log(`${staticLayout ? 'Static' : 'Full'} build in ${dist}`);
const landing = report('Landing page (first load)', landingKey, htmlOf([staticLayout ? 'index.html' : 'welcome/index.html']));
const landingUpFront = closure(landingKey).files;

// What the landing page fetches after its first render (React.lazy parts): listed, not counted.
const later = new Set<string>();
const visited = new Set<string>();
const followDynamic = (k: string) => {
  if (visited.has(k)) return;
  visited.add(k);
  for (const d of manifest[k]?.dynamicImports ?? []) {
    for (const f of closure(d).files) if (!landingUpFront.has(f)) later.add(f);
    followDynamic(d);
  }
  for (const i of manifest[k]?.imports ?? []) followDynamic(i);
};
followDynamic(landingKey);
if (later.size > 0) {
  const lazyTotal = [...later].reduce((n, f) => n + gz(join(dist, f)), 0);
  console.log(`  after first render (lazy, not counted): ${[...later].sort().join(', ')}, ${kb(lazyTotal)}`);
}

const app = report('Dashboard entry (first load)', appKey, htmlOf([staticLayout ? 'app/index.html' : 'index.html']));
const appUpFront = closure(appKey).files;

// Every other JS chunk loads lazily: each has its own budget.
const upFront = new Set([...landingUpFront, ...appUpFront]);
const lazy = Object.keys(modules)
  .filter((f) => f.endsWith('.js') && !upFront.has(f))
  .map((f) => [f, gz(join(dist, f))] as const)
  .sort((a, b) => b[1] - a[1]);
console.log(`\nLazy chunks: ${lazy.length}, the largest ${lazy.slice(0, 3).map(([f, s]) => `${f} ${kb(s)}`).join(', ')}`);

console.log('');
budget('Landing first load', landing, landingBudget);
budget('App entry', app, BUDGETS.app);
for (const [f, s] of lazy) if (s > BUDGETS.lazy) failures.push(`lazy chunk ${f} is ${kb(s)}, over the ${kb(BUDGETS.lazy)} budget (D112)`);
console.log(`Largest lazy chunk: ${kb(lazy[0]?.[1] ?? 0)} of ${kb(BUDGETS.lazy)}`);

// The live feature stays out of the first loads (D99, D112).
for (const [where, files] of [
  ['the landing first load', landingUpFront],
  ['the app entry', appUpFront],
] as const) {
  for (const f of files) {
    for (const m of modules[f] ?? []) if (FORBIDDEN_UP_FRONT.test(m)) failures.push(`${m} is in ${where} (${f}); load it lazily (D112)`);
  }
}
const chip = manifest[LIVE_KEY_CHIP];
if (!chip) failures.push(`${LIVE_KEY_CHIP} is not a chunk of its own: load it with lazy(() => import(…)) (D112)`);
else if (!chip.isDynamicEntry || upFront.has(chip.file)) failures.push(`${LIVE_KEY_CHIP} must load only through a dynamic import (D112)`);
console.log(`Live modules up front: none${chip ? `; LiveKeyChip in ${chip.file}` : ''}`);

// Nothing contacts openrouter.ai before consent: no preconnect or dns-prefetch hints (D112).
function filesUnder(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? filesUnder(join(dir, e.name)) : [join(dir, e.name)]));
}
const all = filesUnder(dist).filter((f) => !relative(dist, f).startsWith('.vite'));
for (const html of all.filter((f) => f.endsWith('.html'))) {
  for (const tag of readFileSync(html, 'utf8').match(/<link\b[^>]*>/gi) ?? []) {
    if (/rel\s*=\s*["']?[^"'>]*\b(preconnect|dns-prefetch)\b/i.test(tag) && /openrouter\.ai/i.test(tag)) {
      failures.push(`${relative(dist, html)} hints a connection to openrouter.ai before consent: ${tag}`);
    }
  }
}
const total = all.reduce((n, f) => n + statSync(f).size, 0);
console.log(`dist total: ${(total / 1e6).toFixed(2)} MB of ${(BUDGETS.distTotal / 1e6).toFixed(0)} MB (${all.length} files)`);
if (total > BUDGETS.distTotal) failures.push(`dist is ${(total / 1e6).toFixed(2)} MB, over 10 MB (D112)`);

if (failures.length > 0) {
  for (const f of failures) console.error(`size: ${f}`);
  process.exit(1);
}
console.log('size: every budget holds.');

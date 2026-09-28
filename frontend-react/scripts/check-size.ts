/**
 * `npm run size`: the landing page's weight budget (SPEC §3.3: separate entry, <= 110 KB gzip,
 * blocking). Run it after a build (npm run build or npm run build:static).
 *
 * Counted: everything the landing page loads before it is interactive, gzip -9: its HTML, the
 * theme-init script, its entry chunk with every statically imported chunk, and their CSS. Fonts are
 * listed but not counted: they are woff2 (already compressed), shared with the dashboard, and the
 * browser fetches only the subsets a page uses. The dashboard entry is reported for information.
 *
 *   node scripts/check-size.ts [--dist dist] [--budget-kb 110]
 */
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';
import { gzipSync } from 'node:zlib';

interface ManifestChunk {
  file: string;
  src?: string;
  isEntry?: boolean;
  imports?: string[];
  css?: string[];
  assets?: string[];
}

const web = fileURLToPath(new URL('../', import.meta.url));
const { values } = parseArgs({
  options: {
    dist: { type: 'string', default: join(web, 'dist') },
    'budget-kb': { type: 'string', default: '110' },
  },
});
const dist = values.dist;
const budget = Number(values['budget-kb']) * 1000;

const manifestPath = join(dist, '.vite/manifest.json');
if (!existsSync(manifestPath)) {
  console.error(`size: ${manifestPath} not found; run \`npm run build:static\` (or \`npm run build\`) first.`);
  process.exit(2);
}
const manifest = JSON.parse(readFileSync(manifestPath, 'utf8')) as Record<string, ManifestChunk>;

const gz = (path: string) => gzipSync(readFileSync(path), { level: 9 }).length;
const kb = (n: number) => `${(n / 1000).toFixed(1)} kB`;

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

function report(title: string, key: string, html: string | null, counted: boolean): number {
  const { files, fonts } = closure(key);
  const rows: [string, number][] = [...files].sort().map((f) => [f, gz(join(dist, f))]);
  if (html) rows.unshift([html.slice(dist.length + 1), gz(html)]);
  const theme = join(dist, 'theme-init.js');
  if (existsSync(theme)) rows.push(['theme-init.js', gz(theme)]);
  const total = rows.reduce((n, [, s]) => n + s, 0);
  console.log(`\n${title}${counted ? '' : ' (informational)'}`);
  for (const [f, s] of rows) console.log(`  ${kb(s).padStart(9)}  ${f}`);
  console.log(`  ${kb(total).padStart(9)}  total, gzip -9`);
  if (fonts.size > 0) {
    // CSS asset paths in the manifest are relative to dist.
    const fontBytes = [...fonts].reduce((n, f) => n + (existsSync(join(dist, f)) ? readFileSync(join(dist, f)).length : 0), 0);
    console.log(`  fonts: ${fonts.size} woff2 files, ${kb(fontBytes)} on disk (not counted; the browser loads only the subsets in use)`);
  }
  return total;
}

const landingKey = Object.keys(manifest).find((k) => manifest[k].isEntry && k.endsWith('landing.html'));
const appKey = Object.keys(manifest).find((k) => manifest[k].isEntry && k.endsWith('index.html'));
if (!landingKey) {
  console.error('size: the build has no landing.html entry');
  process.exit(2);
}
// Static build: the landing page is the root index.html and the app is app/index.html; full build:
// the app is index.html and the landing page welcome/index.html.
const staticLayout = existsSync(join(dist, 'app/index.html'));
const total = report('Landing page', landingKey, htmlOf([staticLayout ? 'index.html' : 'welcome/index.html']), true);
if (appKey) report('Dashboard entry', appKey, htmlOf([staticLayout ? 'app/index.html' : 'index.html']), false);

console.log(`\nLanding budget: ${kb(total)} of ${kb(budget)}`);
if (total > budget) {
  console.error(`size: the landing page is ${kb(total - budget)} over its ${kb(budget)} gzip budget (SPEC §3.3).`);
  process.exit(1);
}

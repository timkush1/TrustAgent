/**
 * The landing's build-time plugins and generator: vite/plugins/digest.ts (the tape card),
 * vite/plugins/landingData.ts (the landing-only contract data), vite/plugins/ogMeta.ts (the share
 * and search meta) and scripts/gen-landing-glassbox.ts --check (the "What Jev was asked" panel's
 * drift check, D116).
 *
 * The plugins and the script run in Node at build time; the app's tsconfig has no Node types, so
 * they and the Node helpers load through variable specifiers (as replay.test.ts does).
 */
import { afterAll, describe, expect, it } from 'vitest';
import outcomesDoc from '@contracts/ui/outcomes.v1.json';
import { DEFAULT_SITE_URL } from '@/config/runtime';
import { committedManifest, scenarioTrace } from '@/test/recording';

interface PluginLike {
  resolveId: (id: string) => string | undefined;
  load: (this: unknown, id: string) => string | undefined;
  configResolved?: (config: unknown) => void;
  transformIndexHtml?: (html: string, ctx: { filename: string }) => unknown;
}
interface Trace {
  trace_id: string;
  created_at: string;
  gate_status: string;
  action: string | null;
  would_action: string | null;
  answer_source: string | null;
  question_preview: string | null;
  response_preview: string | null;
  verdict?: string | null;
  audit?: { verdict?: string | null } | null;
  checked_against?: { used?: { labels?: string[] }[] } | null;
}
interface Digest {
  kind: string;
  label: string;
  recorded_at: string;
  snapshot: string | null;
  judge: string | null;
  outcomes: Record<string, number>;
  examples: (Trace & { documents: string[] })[];
}
interface DigestModule {
  DIGEST_MODULE: string;
  DIGEST_EXAMPLES: number;
  buildDigest: (manifest: unknown, rules: unknown) => Digest;
  outcomeOf: (t: Trace, rules: unknown) => string;
  digestPlugin: (options?: { webDir?: string; contractsDir?: string; dataset?: string | undefined }) => PluginLike;
}
interface LandingDataModule {
  LANDING_DATA_MODULE: string;
  outcomesSubset: (doc: unknown) => unknown;
  landingDataPlugin: (contractsDir: string) => PluginLike;
}
interface Tag {
  tag: string;
  attrs: Record<string, string>;
}
interface OgModule {
  DEFAULT_SITE_URL: string;
  siteUrlOf: (value: string | undefined) => string;
  ogTags: (siteUrl: string, hasImage: boolean) => Tag[];
  ogMetaPlugin: () => PluginLike;
}

const nodeSpecifiers = ['node:fs', 'node:os', 'node:path', 'node:url', 'node:child_process'];
const [fsMod, osMod, pathMod, urlMod, cpMod] = (await Promise.all(nodeSpecifiers.map((m) => import(/* @vite-ignore */ m)))) as [
  {
    mkdtempSync: (prefix: string) => string;
    mkdirSync: (p: string, o: { recursive: boolean }) => void;
    writeFileSync: (p: string, t: string) => void;
    readFileSync: (p: string, e: 'utf8') => string;
    rmSync: (p: string, o: { recursive: boolean; force: boolean }) => void;
  },
  { tmpdir: () => string },
  { join: (...parts: string[]) => string },
  { fileURLToPath: (url: string) => string },
  { spawnSync: (cmd: string, args: string[], o: { cwd: string; encoding: 'utf8'; env: Record<string, string | undefined> }) => { status: number | null; stdout: string; stderr: string } },
];
const proc = (globalThis as unknown as { process: { execPath: string; env: Record<string, string | undefined> } }).process;
const here = import.meta.url;
const webDir = urlMod.fileURLToPath(new URL('../../', here).href);
const contractsDir = urlMod.fileURLToPath(new URL('../../../contracts/', here).href);
const load = async <T,>(rel: string) => (await import(/* @vite-ignore */ new URL(rel, here).href)) as T;
const digestMod = await load<DigestModule>('../../vite/plugins/digest.ts');
const landingDataMod = await load<LandingDataModule>('../../vite/plugins/landingData.ts');
const ogMod = await load<OgModule>('../../vite/plugins/ogMeta.ts');

const made: string[] = [];
afterAll(() => {
  for (const dir of made) fsMod.rmSync(dir, { recursive: true, force: true });
});
function tempDir(prefix: string): string {
  const dir = fsMod.mkdtempSync(pathMod.join(osMod.tmpdir(), prefix));
  made.push(dir);
  return dir;
}

/** A plugin hook's `this`: errors throw, watch files are ignored. */
const hookContext = {
  error(message: string): never {
    throw new Error(message);
  },
  addWatchFile() {},
};

// ------------------------------------------------------------------------------------- digest

describe('tape digest plugin', () => {
  const trace = (over: Partial<Trace> & { trace_id: string; created_at: string }): Trace => ({
    gate_status: 'ok',
    action: 'pass',
    would_action: 'pass',
    answer_source: 'scripted',
    question_preview: `q ${over.trace_id}`,
    response_preview: `a ${over.trace_id}`,
    verdict: null,
    audit: null,
    ...over,
  });
  const at = (s: number) => new Date(Date.UTC(2026, 8, 28, 9, 0, s)).toISOString();

  it('digests the committed tape: kind, label, date, snapshot, judge, counts and five examples', () => {
    const manifest = committedManifest();
    const d = digestMod.buildDigest(manifest, outcomesDoc);
    expect([d.kind, d.label, d.recorded_at, d.snapshot, d.judge]).toEqual([manifest.kind, manifest.label, manifest.recorded_at, manifest.decision_model.snapshot, `${manifest.judge.provider}/${manifest.judge.model}`]);
    expect(Object.values(d.outcomes).reduce((a, n) => a + n, 0)).toBe(manifest.traces.length);
    for (const [id, n] of Object.entries(d.outcomes)) {
      expect(manifest.traces.filter((t) => digestMod.outcomeOf(t as Trace, outcomesDoc.rules) === id), id).toHaveLength(n);
    }
    expect(d.examples.length).toBeLessThanOrEqual(digestMod.DIGEST_EXAMPLES);
    expect(d.examples.length).toBe(Math.min(digestMod.DIGEST_EXAMPLES, Object.keys(d.outcomes).length));
    // One per outcome, oldest first, with only the fields the card reads.
    const outcomes = d.examples.map((t) => digestMod.outcomeOf(t, outcomesDoc.rules));
    expect(new Set(outcomes).size).toBe(outcomes.length);
    expect(d.examples.map((t) => t.created_at)).toEqual([...d.examples.map((t) => t.created_at)].sort());
    for (const t of d.examples) expect(Object.keys(t).sort()).toEqual(['action', 'answer_source', 'created_at', 'documents', 'gate_status', 'question_preview', 'response_preview', 'trace_id', 'would_action']);
    // The featured refund request is the flagged example, checked against the refund policy; an
    // example the gateway could not check had no documents.
    const flagged = d.examples.find((t) => t.action === 'flag');
    expect(flagged?.trace_id).toBe(scenarioTrace('refund-contradiction-flag').trace_id);
    expect(flagged?.documents).toEqual(['refund-policy.md']);
    for (const t of d.examples.filter((x) => x.gate_status === 'not_assessed')) expect(t.documents).toEqual([]);
  });

  it('keeps the featured answer, a pass and the gateway’s actions when there are more outcomes than examples', () => {
    const traces = [
      trace({ trace_id: 'observe', created_at: at(1), action: 'observe', would_action: 'block' }),
      trace({ trace_id: 'flag-unsupported', created_at: at(2), action: 'flag', would_action: 'flag', verdict: 'unsupported' }),
      trace({ trace_id: 'error', created_at: at(3), gate_status: 'timeout' }),
      trace({ trace_id: 'pass', created_at: at(4) }),
      trace({ trace_id: 'flag-contradicted', created_at: at(5), action: 'flag', would_action: 'block', verdict: 'contradicted' }),
      trace({ trace_id: 'nocheck', created_at: at(6), gate_status: 'not_assessed' }),
      trace({ trace_id: 'block', created_at: at(7), action: 'block', would_action: 'block' }),
      trace({ trace_id: 'fallback', created_at: at(8), action: 'fallback', would_action: 'block' }),
      trace({ trace_id: 'pending', created_at: at(0), gate_status: 'pending', action: null, would_action: null }),
    ];
    const d = digestMod.buildDigest({ kind: 'recorded', label: 'Recorded', recorded_at: at(0), decision_model: null, judge: null, traces }, outcomesDoc);
    expect(d.examples.map((t) => t.trace_id)).toEqual(['pass', 'flag-contradicted', 'nocheck', 'block', 'fallback']);
    expect([d.snapshot, d.judge]).toEqual([null, null]);
    // A request still being checked is counted but never an example.
    expect(d.outcomes.checking).toBe(1);
  });

  it('refuses a manifest that is not a recording', () => {
    expect(() => digestMod.buildDigest({ kind: 'surprise', label: 'x', recorded_at: at(0), traces: [] }, outcomesDoc)).toThrow(/kind "surprise"/);
    expect(() => digestMod.buildDigest({ kind: 'mock', label: 'x', recorded_at: at(0), traces: null }, outcomesDoc)).toThrow(/no label, recorded_at or traces/);
    // The card formats the date; an unreadable one would throw in the browser instead.
    expect(() => digestMod.buildDigest({ kind: 'mock', label: 'x', recorded_at: 'yesterday', traces: [] }, outcomesDoc)).toThrow(/recorded_at "yesterday" is not a date/);
  });

  it('serves the module from the dataset the build ships, and fails the build when there is none', () => {
    const plugin = digestMod.digestPlugin({ webDir, contractsDir, dataset: 'mock' });
    const id = plugin.resolveId(digestMod.DIGEST_MODULE) as string;
    expect(id).toBe(`\0${digestMod.DIGEST_MODULE}`);
    expect(plugin.resolveId('virtual:trustagent/other')).toBeUndefined();
    const code = plugin.load.call(hookContext, id) as string;
    const served = JSON.parse(code.replace(/^export const digest = /, '').replace(/;\n$/, '')) as Digest;
    expect(served).toEqual(digestMod.buildDigest(committedManifest(), outcomesDoc));

    const empty = tempDir('ta-digest-');
    fsMod.mkdirSync(pathMod.join(empty, 'demo-data', 'simulated'), { recursive: true });
    const broken = digestMod.digestPlugin({ webDir: empty, contractsDir, dataset: 'recorded' });
    expect(() => broken.load.call(hookContext, id)).toThrow(/tape-digest: VITE_DEMO_DATASET=recorded, but .*does not exist/);
  });
});

// -------------------------------------------------------------------------------- landing data

describe('landing data plugin', () => {
  it('serves the example policy as one string and only the outcome fields the landing reads', () => {
    const plugin = landingDataMod.landingDataPlugin(contractsDir);
    const id = plugin.resolveId(landingDataMod.LANDING_DATA_MODULE) as string;
    const code = plugin.load.call(hookContext, id) as string;
    // `export const <name> = <JSON>;` per line.
    const mod = Object.fromEntries(
      code
        .trim()
        .split('\n')
        .map((line) => {
          const m = /^export const (\w+) = (.*);$/.exec(line);
          if (!m) throw new Error(`unexpected module line: ${line}`);
          return [m[1], JSON.parse(m[2]) as unknown];
        }),
    ) as { examplePolicy: string; outcomes: { outcomes: Record<string, unknown>[] } };
    expect(mod.examplePolicy).toBe(fsMod.readFileSync(pathMod.join(contractsDir, 'policies/example.policies.yaml'), 'utf8'));
    expect(mod.outcomes).toEqual(landingDataMod.outcomesSubset(outcomesDoc));
    for (const o of mod.outcomes.outcomes) expect(Object.keys(o).sort()).toEqual(['headline', 'id', 'label', 'tone']);
    // Smaller than the contract it is cut from.
    expect(JSON.stringify(mod.outcomes).length).toBeLessThan(JSON.stringify(outcomesDoc).length);
  });
});

// ------------------------------------------------------------------------------------- og meta

describe('og meta plugin (D115)', () => {
  it('normalizes VITE_SITE_URL like the runtime config, defaulting to the published site', () => {
    expect(ogMod.DEFAULT_SITE_URL).toBe(DEFAULT_SITE_URL);
    expect(ogMod.siteUrlOf(undefined)).toBe(DEFAULT_SITE_URL);
    expect(ogMod.siteUrlOf('not a url')).toBe(DEFAULT_SITE_URL);
    expect(ogMod.siteUrlOf('/TrustAgent/')).toBe(DEFAULT_SITE_URL);
    expect(ogMod.siteUrlOf(' https://example.com/site ')).toBe('https://example.com/site/');
  });

  it('writes absolute canonical and og URLs, and og:image only with an image', () => {
    const site = 'https://example.com/site/';
    const without = ogMod.ogTags(site, false);
    const attr = (tags: Tag[], key: string, value: string) => tags.find((t) => t.attrs[key] === value)?.attrs;
    expect(attr(without, 'rel', 'canonical')?.href).toBe(site);
    expect(attr(without, 'property', 'og:url')?.content).toBe(site);
    expect(attr(without, 'name', 'twitter:card')?.content).toBe('summary');
    expect(without.filter((t) => t.attrs.name === 'theme-color').map((t) => t.attrs.media)).toEqual(['(prefers-color-scheme: light)', '(prefers-color-scheme: dark)']);
    expect(attr(without, 'property', 'og:image')).toBeUndefined();
    const withImage = ogMod.ogTags(site, true);
    expect(attr(withImage, 'property', 'og:image')?.content).toBe('https://example.com/site/og.png');
    expect(attr(withImage, 'name', 'twitter:card')?.content).toBe('summary_large_image');
    for (const t of withImage) {
      for (const v of [t.attrs.href, t.attrs.content].filter((x) => x?.includes('/'))) expect(v).toMatch(/^https:\/\//);
    }
  });

  it('meta-tags the landing only, and finds og.png in the public directory', () => {
    const publicDir = tempDir('ta-og-');
    const plugin = ogMod.ogMetaPlugin();
    plugin.configResolved?.({ env: { VITE_SITE_URL: 'https://example.com/x' }, publicDir });
    expect(plugin.transformIndexHtml?.('', { filename: '/w/index.html' })).toBeUndefined();
    const tags = plugin.transformIndexHtml?.('', { filename: '/w/landing.html' }) as Tag[];
    expect(tags.find((t) => t.attrs.rel === 'canonical')?.attrs.href).toBe('https://example.com/x/');
    expect(tags.some((t) => t.attrs.property === 'og:image')).toBe(false);

    fsMod.writeFileSync(pathMod.join(publicDir, 'og.png'), 'png');
    plugin.configResolved?.({ env: {}, publicDir });
    const withImage = plugin.transformIndexHtml?.('', { filename: '/w/landing.html' }) as Tag[];
    expect(withImage.find((t) => t.attrs.property === 'og:image')?.attrs.content).toBe(`${DEFAULT_SITE_URL}og.png`);
  });
});

// ------------------------------------------------------------------------------ glass-box drift

describe('gen-landing-glassbox --check (D116)', () => {
  const run = (...args: string[]) =>
    cpMod.spawnSync(proc.execPath, ['--import', './scripts/lib/aliasHooks.ts', 'scripts/gen-landing-glassbox.ts', ...args], {
      cwd: webDir,
      encoding: 'utf8',
      env: { PATH: proc.env.PATH, HOME: proc.env.HOME },
    });

  it('passes on the committed file and fails on a stale one, naming the fix', () => {
    const ok = run('--check');
    expect(ok.stderr).toBe('');
    expect(ok.status).toBe(0);
    expect(ok.stdout).toMatch(/glassbox\.gen\.json is up to date/);

    const stale = pathMod.join(tempDir('ta-glassbox-'), 'glassbox.gen.json');
    fsMod.writeFileSync(stale, '{}\n');
    const bad = run('--check', '--out', stale);
    expect(bad.status).toBe(1);
    expect(bad.stderr).toMatch(/is stale: run `node --import \.\/scripts\/lib\/aliasHooks\.ts scripts\/gen-landing-glassbox\.ts`/);

    // Writing it brings it back in step.
    expect(run('--out', stale).status).toBe(0);
    expect(run('--check', '--out', stale).status).toBe(0);
    expect(fsMod.readFileSync(stale, 'utf8')).toBe(fsMod.readFileSync(pathMod.join(webDir, 'src/landing/glassbox.gen.json'), 'utf8'));
  }, 30_000);
});

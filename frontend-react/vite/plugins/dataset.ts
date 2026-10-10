/**
 * The static replay's dataset (D110). Sources live outside public/:
 * - `demo-data/simulated/`: the deterministic mock tape (`npm run recording`);
 * - `demo-data/recorded/`: a `kind: recorded` export from `make record-demo` (absent until P6).
 *
 * VITE_DEMO_DATASET picks one: `mock` the simulated tape, `recorded` the recorded one (the build
 * fails without it), `auto` (default) the recorded one when its manifest exists, else the
 * simulated one. Only that directory is copied to `dist/demo/` and served at `demo/` in dev;
 * `virtual:trustagent/dataset` tells the replay which kind it must find there. Owned by WEB-REPLAY.
 */
import { cpSync, createReadStream, existsSync, readFileSync, statSync } from 'node:fs';
import { join, resolve, sep } from 'node:path';
import type { Plugin, ResolvedConfig } from 'vite';

export const DATASET_MODULE = 'virtual:trustagent/dataset';
/** Where the chosen dataset is served, under the base path. */
export const DATASET_URL_DIR = 'demo/';

export type DatasetKind = 'mock' | 'recorded';

export interface DatasetChoice {
  /** The source directory. */
  dir: string;
  /** The manifest kind the replay must find. */
  expected: DatasetKind;
}

/** Picks the dataset directory for a VITE_DEMO_DATASET value (unknown values mean `auto`). */
export function chooseDataset(dataDir: string, value: string | undefined): DatasetChoice {
  const simulated = join(dataDir, 'simulated');
  const recorded = join(dataDir, 'recorded');
  const hasRecorded = existsSync(join(recorded, 'manifest.json'));
  const want = value === 'mock' || value === 'recorded' ? value : 'auto';
  if (want === 'recorded' && !hasRecorded) {
    throw new Error(`VITE_DEMO_DATASET=recorded, but ${recorded}/manifest.json does not exist (run make record-demo)`);
  }
  if (want === 'recorded' || (want === 'auto' && hasRecorded)) {
    const kind = (JSON.parse(readFileSync(join(recorded, 'manifest.json'), 'utf8')) as { kind?: unknown }).kind;
    if (kind !== 'recorded') throw new Error(`${recorded}/manifest.json is kind ${JSON.stringify(kind)}, not "recorded"`);
    return { dir: recorded, expected: 'recorded' };
  }
  if (!existsSync(join(simulated, 'manifest.json'))) throw new Error(`${simulated}/manifest.json does not exist (run npm run recording)`);
  return { dir: simulated, expected: 'mock' };
}

export function datasetPlugin(webDir: string, value: string | undefined = process.env.VITE_DEMO_DATASET): Plugin {
  const resolved = `\0${DATASET_MODULE}`;
  let config: ResolvedConfig;
  let choice: DatasetChoice | undefined;
  const chosen = () => (choice ??= chooseDataset(join(webDir, 'demo-data'), value));
  return {
    name: 'trustagent:dataset',
    configResolved(c) {
      config = c;
    },
    resolveId(source) {
      return source === DATASET_MODULE ? resolved : undefined;
    },
    load(id) {
      if (id !== resolved) return undefined;
      return `export const expected = ${JSON.stringify(chosen().expected)};\n`;
    },
    writeBundle() {
      const out = join(resolve(config.root, config.build.outDir), DATASET_URL_DIR);
      cpSync(chosen().dir, out, { recursive: true, filter: (src) => !src.endsWith('.gitkeep') });
    },
    configureServer(server) {
      const { dir } = chosen();
      server.middlewares.use((req, res, next) => {
        const prefix = `${server.config.base}${DATASET_URL_DIR}`;
        const url = (req.url ?? '').split('?')[0];
        if ((req.method !== 'GET' && req.method !== 'HEAD') || !url.startsWith(prefix)) return next();
        let rel: string;
        try {
          rel = decodeURIComponent(url.slice(prefix.length));
        } catch {
          return next();
        }
        const file = resolve(dir, rel);
        if (!file.startsWith(dir + sep) || !existsSync(file) || !statSync(file).isFile()) return next();
        res.setHeader('Content-Type', file.endsWith('.json') ? 'application/json' : 'application/octet-stream');
        res.setHeader('Cache-Control', 'no-store');
        if (req.method === 'HEAD') return res.end();
        createReadStream(file).pipe(res);
      });
    },
  };
}

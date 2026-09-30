/**
 * Writes the static demo's recording (trustagent.recording/v1, SPEC §2.13) to
 * public/demo/recorded: manifest.json plus traces/<trace_id>.json.
 *
 *   node scripts/build-demo-recording.ts            regenerate the committed recording
 *   node scripts/build-demo-recording.ts --check    exit 1 when the committed files are stale
 *   node scripts/build-demo-recording.ts --out DIR --set s0 --rounds 8 --interval-s 30 --start 2026-09-28T09:00:00Z
 *
 * The recording is SIMULATED: the demo scenario pack (contracts/demo/scenarios.v1.json, set
 * `s0`: grounded, refund 60 vs 30 days, no documents) run through the TypeScript decision core with the scenarios' scripted mock answers and
 * mock judge claims, on the support-bot route of config/policies.yaml. Its manifest is kind `mock`
 * with the fixed label "Simulated (mock checks + mock judge): not model output". P5/P6 replace it
 * with `make record-demo` (Go export-replay) output; scripts/check_recording.py checks either.
 *
 * The drift test (src/data/replay/recorder/demoRecording.test.ts) runs the same build under vitest.
 */
import './lib/aliasHooks.ts';

import { execFileSync } from 'node:child_process';
import { mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';
import { parse as parseYaml } from 'yaml';

const repo = fileURLToPath(new URL('../../', import.meta.url));
const web = fileURLToPath(new URL('../', import.meta.url));

const { values } = parseArgs({
  options: {
    out: { type: 'string', default: join(web, 'public/demo/recorded') },
    set: { type: 'string' },
    rounds: { type: 'string' },
    'interval-s': { type: 'string' },
    start: { type: 'string' },
    'git-sha': { type: 'string' },
    check: { type: 'boolean', default: false },
  },
});

const { buildDemoRecording, DEFAULT_BUILD_OPTIONS, serializeRecording, sameRecordingFiles } = await import(
  '../src/data/replay/recorder/index.ts'
);

function gitSha(): string {
  if (values['git-sha']) return values['git-sha'];
  try {
    return execFileSync('git', ['rev-parse', '--short=12', 'HEAD'], { cwd: repo, encoding: 'utf8' }).trim();
  } catch {
    console.error('build-demo-recording: cannot read the git revision; pass --git-sha');
    process.exit(2);
  }
}

const readJson = (path: string) => JSON.parse(readFileSync(path, 'utf8'));
const templatesDir = join(repo, 'contracts/evaluators');
const inputs = {
  scenarios: readJson(join(repo, 'contracts/demo/scenarios.v1.json')),
  policy: parseYaml(readFileSync(join(repo, 'config/policies.yaml'), 'utf8')),
  templates: readdirSync(templatesDir)
    .filter((f) => f.endsWith('.json'))
    .sort()
    .map((f) => readJson(join(templatesDir, f))),
};
const version = readFileSync(join(repo, 'VERSION'), 'utf8').trim();

const opts = {
  ...DEFAULT_BUILD_OPTIONS,
  ...(values.set ? { set: values.set } : {}),
  ...(values.rounds ? { rounds: Number(values.rounds) } : {}),
  ...(values['interval-s'] ? { intervalMs: Number(values['interval-s']) * 1000 } : {}),
  ...(values.start ? { startAt: new Date(values.start).toISOString() } : {}),
  gitSha: values.check ? '0000000' : gitSha(),
  version,
};

const built = buildDemoRecording(inputs, opts);
const files: Record<string, string> = serializeRecording(built);
const out = values.out;

function readDir(dir: string): Record<string, string> {
  const found: Record<string, string> = {};
  try {
    found['manifest.json'] = readFileSync(join(dir, 'manifest.json'), 'utf8');
    for (const f of readdirSync(join(dir, 'traces'))) {
      if (f.endsWith('.json')) found[`traces/${f}`] = readFileSync(join(dir, 'traces', f), 'utf8');
    }
  } catch {
    // A missing directory or manifest is stale.
  }
  return found;
}

if (values.check) {
  if (!sameRecordingFiles(readDir(out), files)) {
    console.error(`${out} is stale: run \`node scripts/build-demo-recording.ts\` and commit the result.`);
    process.exit(1);
  }
  console.log(`${out} is up to date (${built.manifest.traces.length} traces).`);
} else {
  mkdirSync(join(out, 'traces'), { recursive: true });
  for (const f of readdirSync(join(out, 'traces'))) {
    if (f.endsWith('.json') && !(`traces/${f}` in files)) rmSync(join(out, 'traces', f));
  }
  for (const [name, text] of Object.entries(files)) writeFileSync(join(out, name), text);
  const m = built.manifest;
  console.log(`wrote ${out}: ${m.traces.length} traces, ${m.events.length} events, kind ${m.kind} ("${m.label}")`);
}

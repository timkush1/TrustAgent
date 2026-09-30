/**
 * The committed demo recording (public/demo/recorded) is exactly what the recorder builds from the
 * contracts today, and it is honest about being simulated. When this fails after a contract change,
 * run `npm run recording` and commit the result.
 */
import { describe, expect, it } from 'vitest';
import { parse as parseYaml } from 'yaml';
import scenarioPack from '@contracts/demo/scenarios.v1.json';
import { BUILTIN_TEMPLATES } from '@/lib/decision/templates';
import type { TraceDetail } from '@/types/api';
import {
  buildDemoRecording,
  DEFAULT_BUILD_OPTIONS,
  MOCK_LABEL,
  RecordingBuildError,
  sameRecordingFiles,
  serializeRecording,
  uuidv7,
  type BuildInputs,
  type PolicyInput,
  type Scenario,
} from './index';

// config/ and VERSION sit outside Vite's served directories: read them from disk, as
// scripts/build-demo-recording.ts does. The specifiers are variables because the app's tsconfig
// has no Node types (see charts/tokenContrast.test.ts).
const [fsSpecifier, urlSpecifier] = ['node:fs', 'node:url'];
const { readFileSync } = (await import(/* @vite-ignore */ fsSpecifier)) as { readFileSync: (path: string, encoding: 'utf8') => string };
const { fileURLToPath } = (await import(/* @vite-ignore */ urlSpecifier)) as { fileURLToPath: (url: string) => string };
const here = import.meta.url;
const repoFile = (path: string) => readFileSync(fileURLToPath(new URL(`../../../../../${path}`, here).href), 'utf8');
const policyYaml = repoFile('config/policies.yaml');
const versionText = repoFile('VERSION');

const committed = import.meta.glob<string>('../../../../public/demo/recorded/**/*.json', { query: '?raw', import: 'default', eager: true });
const committedFiles = Object.fromEntries(
  Object.entries(committed).map(([path, text]) => [path.slice(path.indexOf('/recorded/') + '/recorded/'.length), text]),
);

const inputs = (): BuildInputs => ({
  scenarios: structuredClone(scenarioPack) as unknown as { scenarios: Scenario[] },
  policy: parseYaml(policyYaml) as PolicyInput,
  templates: Object.values(BUILTIN_TEMPLATES),
});
const opts = { ...DEFAULT_BUILD_OPTIONS, gitSha: '0000000', version: versionText.trim() };

describe('demo recording', () => {
  const built = buildDemoRecording(inputs(), opts);
  const { manifest, details } = built;

  it('matches the committed files (run `npm run recording` after a contract change)', () => {
    expect(Object.keys(committedFiles).length).toBeGreaterThan(1);
    expect(sameRecordingFiles(committedFiles, serializeRecording(built))).toBe(true);
  });

  it('is deterministic', () => {
    expect(serializeRecording(buildDemoRecording(inputs(), opts))).toEqual(serializeRecording(built));
  });

  it('records the s0 scenarios, six rounds of three', () => {
    expect(DEFAULT_BUILD_OPTIONS.set).toBe('s0');
    expect(manifest.traces).toHaveLength(18);
    expect(new Set(manifest.traces.map((t) => t.response_preview)).size).toBe(3);
    expect(manifest.totals.traces).toBe(18);
    expect(manifest.answer_sources).toEqual({ scripted: 18, generated: 0 });
  });

  it('says it is simulated, everywhere', () => {
    expect(manifest.kind).toBe('mock');
    expect(manifest.label).toBe(MOCK_LABEL);
    expect(manifest.decision_model.backend).toBe('mock');
    expect(manifest.judge).toEqual({ provider: 'mock', model: 'mock', fallback_active: false });
    expect(manifest.roi.simulated).toBe(true);
    expect(manifest.calibration_state).not.toBe('measured');
    const text = JSON.stringify(built);
    expect(text).not.toMatch(/"label":"measured"/);
    for (const m of text.matchAll(/"cost_source":"([a-z_]+)"/g)) expect(m[1]).toBe('simulated');
  });

  it('plays each trace as started, decided, then audited', () => {
    for (const t of manifest.traces) {
      const mine = manifest.events.filter((e) => e.trace_id === t.trace_id).map((e) => e.type);
      expect(mine[0]).toBe('trace_started');
      expect(mine[1]).toBe('gate_decision');
      if (t.audit.audit_id) expect(mine.slice(2)).toEqual(['audit_queued', 'audit_completed']);
      else expect(mine).toHaveLength(2);
    }
    const times = manifest.events.map((e) => e.t_ms);
    expect(times).toEqual([...times].sort((a, b) => a - b));
    expect(times[0]).toBe(0);
  });

  it('marks "60 days" in the refund answer with UTF-16 spans', () => {
    const refund = details.find((d) => d.action === 'flag') as TraceDetail;
    expect(refund.reasons).toEqual(['contradiction:critical', 'groundedness:critical', 'det:numbers_not_in_evidence']);
    expect(refund.would_action).toBe('block');
    const answer = refund.body?.response_text ?? '';
    const claim = refund.audit_detail?.claims[0];
    expect(claim?.status).toBe('CONTRADICTED');
    expect(claim?.span && answer.slice(claim.span.start, claim.span.end)).toBe('60 days');
    const finding = refund.decisions[0].detectors.find((d) => d.id === 'numbers_not_in_evidence')?.findings[0];
    expect(finding && answer.slice(finding.start, finding.end)).toBe('60');
    expect(refund.audit_detail?.verdict).toBe('HALLUCINATED');
    expect(refund.checked_against?.used.map((u) => u.labels).flat()).toEqual(['refund-policy.md']);
  });

  it('never asks about groundedness without documents', () => {
    const bare = details.find((d) => d.gate_status === 'not_assessed') as TraceDetail;
    expect(bare.decision_model).toBeNull();
    expect(bare.audit.status).toBe('not_assessed');
    expect(bare.audit_detail).toBeNull();
  });

  it('fails the build when a decision differs from its scenario', () => {
    const changed = inputs();
    const refund = changed.scenarios.scenarios.find((s) => s.id === 'refund-contradiction-flag') as Scenario;
    refund.expect.action = 'block';
    expect(() => buildDemoRecording(changed, opts)).toThrow(RecordingBuildError);
    expect(() => buildDemoRecording(changed, opts)).toThrow(/refund-contradiction-flag: action flag, expected block/);
  });

  it('refuses an empty set and bad options', () => {
    expect(() => buildDemoRecording(inputs(), { ...opts, set: 'nope' })).toThrow(/no scenario is in set/);
    expect(() => buildDemoRecording(inputs(), { ...opts, rounds: 0 })).toThrow(/rounds/);
    expect(() => buildDemoRecording(inputs(), { ...opts, startAt: 'soon' })).toThrow(/not a time/);
  });

  it('writes canonical UUIDv7 ids', () => {
    const id = uuidv7(Date.parse('2026-09-28T09:00:00Z'), 'f'.repeat(64));
    expect(id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    for (const t of manifest.traces) expect(t.trace_id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  });

  it('compares files without the git revision', () => {
    const files = serializeRecording(built);
    const other = { ...files, 'manifest.json': files['manifest.json'].replace('"git_sha": "0000000"', '"git_sha": "abcdef1"') };
    expect(sameRecordingFiles(files, other)).toBe(true);
    const { ['manifest.json']: _manifest, ...traces } = files;
    expect(sameRecordingFiles(files, traces)).toBe(false);
  });
});

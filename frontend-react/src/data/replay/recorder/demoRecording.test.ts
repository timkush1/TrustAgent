/**
 * The committed simulated recording (demo-data/simulated) is exactly what the recorder builds from the
 * contracts today, it reproduces every recorded scenario's `expect` through the browser gate
 * pipeline, it models what the gateway delivers and what the engine audits, and it is honest about
 * being simulated. When the drift test fails after a contract change, run `npm run recording` and
 * commit the result.
 */
import { describe, expect, it } from 'vitest';
import { parse as parseYaml } from 'yaml';
import scenarioPack from '@contracts/demo/scenarios.v1.json';
import { outcomeOf } from '@/domain/presentation';
import { BUILTIN_TEMPLATES } from '@/lib/decision/templates';
import { scenarioTrace, scenarioTraces } from '@/test/recording';
import type { Recording, TraceDetail } from '@/types/api';
import {
  AWAITING_RUNNER,
  bodyMessages,
  buildDemoRecording,
  DEFAULT_BUILD_OPTIONS,
  MOCK_LABEL,
  NO_CLAIMS_TRACE,
  NO_EVIDENCE_TRACE,
  RecordingBuildError,
  sameRecordingFiles,
  secretIn,
  selectScenarios,
  serializeRecording,
  uuidv7,
  viaFloat32,
  type BuildInputs,
  type BuildOptions,
  type BuiltRecording,
  type CalibrationFileInput,
  type PolicyInput,
  type Scenario,
} from './index';

// config/ and VERSION sit outside Vite's served directories: read them from disk, as
// scripts/build-demo-recording.ts does. The specifiers are variables because the app's tsconfig
// has no Node types (see charts/tokenContrast.test.ts).
const [fsSpecifier, urlSpecifier] = ['node:fs', 'node:url'];
const { readFileSync, readdirSync } = (await import(/* @vite-ignore */ fsSpecifier)) as {
  readFileSync: (path: string, encoding: 'utf8') => string;
  readdirSync: (path: string) => string[];
};
const { fileURLToPath } = (await import(/* @vite-ignore */ urlSpecifier)) as { fileURLToPath: (url: string) => string };
// A variable base keeps Vite from turning `new URL(…, import.meta.url)` into an asset glob.
const here = import.meta.url;
const repoPath = (path: string) => fileURLToPath(new URL(`../../../../../${path}`, here).href);
const repoFile = (path: string) => readFileSync(repoPath(path), 'utf8');
const policyYaml = repoFile('config/policies.yaml');
const versionText = repoFile('VERSION');
const calibrationFiles = readdirSync(repoPath('config/calibration'))
  .filter((f) => f.endsWith('.json'))
  .sort()
  .map((f) => JSON.parse(repoFile(`config/calibration/${f}`)) as CalibrationFileInput);

const committed = import.meta.glob<string>('../../../../demo-data/simulated/**/*.json', { query: '?raw', import: 'default', eager: true });
const committedFiles = Object.fromEntries(
  Object.entries(committed).map(([path, text]) => [path.slice(path.indexOf('/simulated/') + '/simulated/'.length), text]),
);

const inputs = (): BuildInputs => ({
  scenarios: structuredClone(scenarioPack) as unknown as { scenarios: Scenario[] },
  policy: parseYaml(policyYaml) as PolicyInput,
  templates: Object.values(BUILTIN_TEMPLATES),
  calibrations: structuredClone(calibrationFiles),
});
const opts: BuildOptions = { ...DEFAULT_BUILD_OPTIONS, gitSha: '0000000', version: versionText.trim() };

const built = await buildDemoRecording(inputs(), opts);
const { manifest, details } = built;
const detailOf = (id: string): TraceDetail => details.find((d) => d.trace_id === id) as TraceDetail;
const firstOf = (scenarioId: string): TraceDetail => detailOf(scenarioTrace(scenarioId, 0, manifest).trace_id);
const scenario = (id: string): Scenario => (scenarioPack.scenarios as unknown as Scenario[]).find((s) => s.id === id) as Scenario;

/** The tape's scenarios, in pack order: what a reader of the replay sees. */
const RECORDED = [
  'grounded-pass',
  'refund-contradiction-flag',
  'no-evidence-not-assessed',
  'unsupported-date-corroborated',
  'tool-results-grounded',
  'developer-role-unsupported',
  'line-header-context-grounded',
  'extension-misquote-flag',
  'untagged-policy-prose',
  'untagged-custom-tag',
  'untagged-pasted-clause',
  'pii-leak-flag',
  'refusal-flag',
  'injection-success-flag',
  'observe-would-block',
  'help-center-link-block-json',
  'help-center-uncalibrated-flag',
];

/** One round of a scenario set built from inputs a test changed (the scenarios join set `probe`). */
async function probe(ids: readonly string[], change: (i: BuildInputs) => void = () => undefined, awaiting: Record<string, string> = {}): Promise<BuiltRecording> {
  const i = inputs();
  for (const s of i.scenarios.scenarios) if (ids.includes(s.id)) s.sets = [...s.sets, 'probe'];
  change(i);
  return buildDemoRecording(i, { ...opts, set: 'probe', rounds: 1, awaiting });
}

/** payments-faq as runGate can run it today: raw thresholds and no calibration requirement. */
function rawPaymentsRoute(i: BuildInputs): void {
  const route = i.policy.routes?.find((r) => r.name === 'payments-faq') as Record<string, unknown>;
  route.require_calibration = false;
  route.evaluators = (route.evaluators as { ref: string }[]).map((e) => ({ ref: e.ref }));
}

describe('demo recording', () => {
  it('matches the committed files (run `npm run recording` after a contract change)', () => {
    expect(Object.keys(committedFiles).length).toBeGreaterThan(1);
    expect(sameRecordingFiles(committedFiles, serializeRecording(built))).toBe(true);
  });

  it('is deterministic', async () => {
    expect(serializeRecording(await buildDemoRecording(inputs(), opts))).toEqual(serializeRecording(built));
  });

  it('records every recordable demo scenario, four rounds over two hours', () => {
    expect(DEFAULT_BUILD_OPTIONS.set).toBe('demo');
    for (const id of RECORDED) expect(scenarioTraces(id, manifest), id).toHaveLength(DEFAULT_BUILD_OPTIONS.rounds);
    expect(manifest.traces).toHaveLength(RECORDED.length * DEFAULT_BUILD_OPTIONS.rounds);
    expect(manifest.totals.traces).toBe(manifest.traces.length);
    expect(manifest.answer_sources).toEqual({ scripted: manifest.traces.length, generated: 0 });
    // Round 0 opens with the walking-skeleton trio, so the refund answer is the first caught one.
    expect(manifest.traces.slice(0, 3).map((t) => t.trace_id)).toEqual(
      ['grounded-pass', 'refund-contradiction-flag', 'no-evidence-not-assessed'].map((id) => scenarioTrace(id, 0, manifest).trace_id),
    );
    const span = Date.parse(manifest.traces[manifest.traces.length - 1].created_at) - Date.parse(manifest.recorded_at);
    expect(span).toBeGreaterThanOrEqual(2 * 3_600_000);
    expect(manifest.roi.window).toBe('24h');
  });

  it('says why each other scenario of the set stays off the tape', () => {
    expect(Object.fromEntries(built.skipped.map((s) => [s.id, s.reason]))).toEqual({
      'secret-leak-flag': 'secret',
      'commitment-unapproved-flag': 'awaiting_runner',
      'off-topic-flag': 'awaiting_runner',
      'stream-passthrough-flag': 'stream',
      'anthropic-system-grounded': 'api_format',
      'completions-extension-grounded': 'api_format',
      'payments-fallback': 'awaiting_runner',
      'help-center-link-block': 'stream',
      'generated-monthly-refund': 'generated',
      'generated-weekend-shipping': 'generated',
    });
    expect(built.skipped.find((s) => s.id === 'secret-leak-flag')?.detail).toMatch(/an sk- API key/);
  });

  it('shows each recorded scenario as the UI words its expected outcome, in every round', () => {
    for (const id of RECORDED) {
      const want = scenario(id).expect.outcome;
      for (const t of scenarioTraces(id, manifest)) expect(outcomeOf(t), id).toBe(want);
    }
    const outcomes = new Set(manifest.traces.map((t) => outcomeOf(t)));
    expect([...outcomes].sort()).toEqual(['blocked', 'flagged', 'not_checked', 'passed', 'would_block']);
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
    expect(secretIn({ manifest, details })).toBeNull();
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
    const refund = firstOf('refund-contradiction-flag');
    expect(refund.action).toBe('flag');
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
    const bare = firstOf('no-evidence-not-assessed');
    expect(bare.gate_status).toBe('not_assessed');
    expect(bare.decision_model).toBeNull();
    expect(bare.audit.status).toBe('not_assessed');
    expect(bare.audit_detail).toBeNull();
  });

  it('delivers the route’s block message for a block, and audits the original answer', () => {
    const blocked = firstOf('help-center-link-block-json');
    expect(blocked.action).toBe('block');
    expect(blocked.body?.response_text).toBe(scenario('help-center-link-block-json').response);
    expect(blocked.body?.final_response_text).toBe("I can't verify that answer against our help articles, so I've held it back.");
    expect(blocked.audit_detail?.gate?.delivered).toBe('blocked');
    expect(blocked.audit_detail?.llm_response).toBe(blocked.body?.response_text);
    expect(blocked.decisions.map((d) => d.attempt)).toEqual([0]);
  });

  it('answers first and decides after in observe mode', () => {
    const observed = firstOf('observe-would-block');
    expect([observed.action, observed.would_action]).toEqual(['observe', 'block']);
    expect(observed.gate_status).toBe('ok');
    expect(observed.body?.final_response_text).toBe(observed.body?.response_text);
    const { upstream_ms, gate_ms, overhead_ms, total_ms } = observed.timings;
    // The response went out before the gate ran: the request's time does not include it.
    expect(total_ms).toBe((upstream_ms ?? 0) + (overhead_ms ?? 0));
    expect(Date.parse(observed.decisions[0].created_at)).toBeGreaterThanOrEqual(Date.parse(observed.created_at) + (total_ms ?? 0) + (gate_ms ?? 0));
  });

  it('reports the gateway overhead as everything but the upstream call', () => {
    for (const t of manifest.traces) expect(t.timings.overhead_ms).toBe((t.timings.total_ms ?? 0) - (t.timings.upstream_ms ?? 0));
  });

  it('lets the engine say NOT_ASSESSED without evidence and NO_CLAIMS when the judge finds none', () => {
    const refusal = firstOf('refusal-flag');
    expect(refusal.audit.status).toBe('not_assessed');
    expect(refusal.audit.verdict).toBe('NOT_ASSESSED');
    expect(refusal.audit_detail).toMatchObject({ status: 'completed', verdict: 'NOT_ASSESSED', not_assessed_reason: 'no_evidence', scores: null, claims: [], costs: [] });
    expect(refusal.audit_detail?.reasoning_trace).toBe(NO_EVIDENCE_TRACE);

    const injection = firstOf('injection-success-flag');
    expect(injection.audit).toMatchObject({ status: 'completed', verdict: 'NO_CLAIMS' });
    expect(injection.audit_detail?.reasoning_trace).toBe(NO_CLAIMS_TRACE);
    // The judge decomposed the answer; with no claims there was nothing to verify.
    expect(injection.audit_detail?.costs.map((c) => c.component)).toEqual(['decompose']);
    expect(Object.keys(injection.audit_detail?.step_timings ?? {}).sort()).toEqual(['decompose_ms', 'score_ms', 'total_ms']);
  });

  it('uses the demo stack’s mock calibration, which a raw-threshold route does not count', () => {
    const refund = firstOf('refund-contradiction-flag');
    expect(refund.decisions[0].evaluators['groundedness@1']).toMatchObject({ calibration_state: 'mock', calibrated: false, threshold_space: 'raw' });
    const capped = firstOf('help-center-uncalibrated-flag');
    expect([capped.action, capped.would_action]).toEqual(['flag', 'block']);
    expect(capped.reasons).toContain('calibration_missing');
  });

  it('stores the request’s messages as the gateway does: text only, no tool calls', () => {
    const tools = firstOf('tool-results-grounded');
    for (const m of tools.body?.messages ?? []) expect(Object.keys(m).sort()).toEqual(['content', 'role']);
    expect(tools.body?.messages.find((m) => m.role === 'assistant')?.content).toBe('');
    expect(tools.body?.messages.find((m) => m.role === 'tool')?.content).toMatch(/^Order A-1043/);
    expect(
      bodyMessages([
        { role: 'user', content: [{ type: 'text', text: 'a' }, { type: 'image_url', image_url: { url: 'x' } }, { type: 'input_text', text: 'b' }] },
        { role: 'tool', name: 'get_order', tool_call_id: 'c1', content: 'ok' },
      ]),
    ).toEqual([
      { role: 'user', content: 'a\nb' },
      { role: 'tool', name: 'get_order', content: 'ok' },
    ]);
  });

  it('serves max_risk as the gateway reads it back from its REAL column', () => {
    expect(viaFloat32(0.9500000000000001)).toBe(0.95);
    expect(viaFloat32(0.06)).toBe(0.06);
    expect(viaFloat32(1)).toBe(1);
    for (const t of manifest.traces) if (t.max_risk !== null) expect(viaFloat32(t.max_risk)).toBe(t.max_risk);
  });

  it('models a fallback: the re-gated fallback answer is delivered, the original is audited', async () => {
    const { details: [d] } = await probe(['payments-fallback'], rawPaymentsRoute);
    const fb = scenario('payments-fallback').fallback;
    expect([d.action, d.would_action]).toEqual(['fallback', 'block']);
    expect(d.flags).toEqual(['fallback_used']);
    expect(d.body?.response_text).toBe(scenario('payments-fallback').response);
    expect(d.body?.final_response_text).toBe(fb?.response);
    expect(d.decisions.map((x) => [x.attempt, x.action])).toEqual([
      [0, 'fallback'],
      [1, 'pass'],
    ]);
    expect(Date.parse(d.decisions[1].created_at)).toBeGreaterThan(Date.parse(d.decisions[0].created_at));
    expect(d.decisions[1].max_risk).toBe(fb?.expect.max_risk);
    // The trace's fast-check cost is attempt 0's, as the gateway stores it.
    expect(d.decision_model?.cost_usd).toBe(d.decisions[0].decision_model?.cost_usd);
    expect(d.audit_detail?.gate).toMatchObject({ action: 'fallback', delivered: 'fallback' });
    expect(d.audit_detail?.llm_response).toBe(d.body?.response_text);
    expect(outcomeOf(d)).toBe('replaced');
  });

  it('applies on_fail when the fallback answer fails its re-gate too', async () => {
    const failing = (i: BuildInputs) => {
      rawPaymentsRoute(i);
      const s = i.scenarios.scenarios.find((x) => x.id === 'payments-fallback') as Scenario;
      if (!s.fallback) throw new Error('payments-fallback has no fallback');
      s.fallback.mock_answers = s.mock_answers;
      s.fallback.expect = { would_action: 'block', verdict: 'contradicted', max_risk: 0.97, reasons: ['groundedness:critical', 'contradiction:critical'] };
      s.expect.action = 'block';
      s.expect.outcome = 'blocked';
      s.expect.delivered_text = "I can't confirm that from our policy documents. A person will follow up.";
    };
    const { details: [d] } = await probe(['payments-fallback'], failing);
    expect(d.action).toBe('block');
    expect(d.flags).toEqual([]);
    expect(d.body?.final_response_text).toBe("I can't confirm that from our policy documents. A person will follow up.");
    expect(d.audit_detail?.gate?.delivered).toBe('blocked');
  });

  it('stores the route’s resolved rules with the body', async () => {
    const { details: [d] } = await probe(['off-topic-flag']);
    expect(d.body?.rules).toEqual({
      allowed_commitments: 'Annual plans: refund within 30 days of purchase. No other refunds, credits or discounts.',
      allowed_scope: 'Acme billing: invoices, payments, plans, refunds and credits.',
    });
    expect(firstOf('grounded-pass').body?.rules).toEqual({});
  });

  // When runGate gains gstudio Runner.Evaluate parity (route thresholds and rules, WEB-LIVE LV-3),
  // these fail: drop the scenario from AWAITING_RUNNER and run `npm run recording`.
  it('keeps waiting only on runGate gaps that still exist', async () => {
    expect(Object.keys(AWAITING_RUNNER).sort()).toEqual(['commitment-unapproved-flag', 'off-topic-flag', 'payments-fallback']);
    await expect(probe(['payments-fallback'])).rejects.toThrow(/payments-fallback: would_action .*|payments-fallback: reasons .*calibration_missing/);
    await expect(probe(['commitment-unapproved-flag'])).rejects.toThrow(/commitment-unapproved-flag: (would_action|reasons)/);
    const { details: [offTopic] } = await probe(['off-topic-flag']);
    const numbers = offTopic.decisions[0].detectors.find((x) => x.id === 'numbers_not_in_evidence');
    expect(numbers?.ran).toBe(false);
  });

  it('fails the build when a decision differs from its scenario', async () => {
    const changed = inputs();
    const refund = changed.scenarios.scenarios.find((s) => s.id === 'refund-contradiction-flag') as Scenario;
    refund.expect.action = 'block';
    await expect(buildDemoRecording(changed, opts)).rejects.toThrow(RecordingBuildError);
    await expect(buildDemoRecording(changed, opts)).rejects.toThrow(/refund-contradiction-flag: action flag, expected block/);
  });

  it('fails the build when the outcome or the delivered text differs', async () => {
    await expect(probe(['help-center-link-block-json'], (i) => {
      (i.scenarios.scenarios.find((s) => s.id === 'help-center-link-block-json') as Scenario).expect.delivered_text = 'Nope.';
    })).rejects.toThrow(/help-center-link-block-json: delivered/);
    await expect(probe(['grounded-pass'], (i) => {
      (i.scenarios.scenarios.find((s) => s.id === 'grounded-pass') as Scenario).expect.outcome = 'flagged';
    })).rejects.toThrow(/grounded-pass: outcome passed, expected flagged/);
  });

  it('fails the build for a missing scripted answer, route or judge fixture', async () => {
    const noAnswer = inputs();
    const refund = noAnswer.scenarios.scenarios.find((s) => s.id === 'refund-contradiction-flag') as Scenario;
    refund.mock_answers = {};
    await expect(buildDemoRecording(noAnswer, opts)).rejects.toThrow(/refund-contradiction-flag has no mock answer/);
    const noRoute = inputs();
    noRoute.policy = { ...noRoute.policy, routes: [] };
    await expect(buildDemoRecording(noRoute, opts)).rejects.toThrow(RecordingBuildError);
    await expect(probe(['grounded-pass'], (i) => {
      (i.scenarios.scenarios.find((s) => s.id === 'grounded-pass') as Scenario).judge = null;
    })).rejects.toThrow(/grounded-pass: the audit has evidence/);
  });

  it('refuses an empty set and bad options', async () => {
    await expect(buildDemoRecording(inputs(), { ...opts, set: 'nope' })).rejects.toThrow(/no recordable scenario is in set/);
    await expect(buildDemoRecording(inputs(), { ...opts, rounds: 0 })).rejects.toThrow(/rounds/);
    await expect(buildDemoRecording(inputs(), { ...opts, startAt: 'soon' })).rejects.toThrow(/not a time/);
    await expect(buildDemoRecording(inputs(), { ...opts, intervalMs: 1000 })).rejects.toThrow(/raise intervalMs/);
  });

  it('keeps text a published recording may not carry off the tape', () => {
    expect(secretIn({ a: ['fine', 'key sk-abcdefgh12345'] })).toBe('an sk- API key');
    expect(secretIn('Authorization: Bearer abcdefgh.ijk')).toBe('a bearer token');
    expect(secretIn('mail dana@acme-corp.com')).toBe('an e-mail address');
    expect(secretIn('mail dana@example.com or ops@mail.test')).toBeNull();
    const pack = (scenarioPack.scenarios as unknown as Scenario[]).map((s) => ({ ...s, sets: ['x'] }));
    const leaked = { ...pack[0], id: 'leaky', response: 'Your key is sk-live0123456789' };
    expect(selectScenarios([...pack, leaked], 'x').skipped.find((s) => s.id === 'leaky')?.reason).toBe('secret');
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

  it('finds every committed trace by its scenario', () => {
    const committedManifest = JSON.parse(committedFiles['manifest.json']) as Recording;
    const found = RECORDED.flatMap((id) => scenarioTraces(id, committedManifest).map((t) => t.trace_id));
    expect(found.sort()).toEqual(committedManifest.traces.map((t) => t.trace_id).sort());
  });
});

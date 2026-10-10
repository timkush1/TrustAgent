/**
 * ReplayDataSource over the committed demo recording: the manifest checks, the tape's view of each
 * trace over time, the compressed-time player, and the DataSource contract the pages rely on
 * (reads at the tape position, a read-only hello, labelled numbers, the unavailable control plane).
 */
import { afterAll, describe, expect, it } from 'vitest';
import scenarioPack from '@contracts/demo/scenarios.v1.json';
import { ApiError } from '../errors';
import type { StreamSink, TraceListParams } from '../source';
import type { StreamEvent } from '../stream/types';
import { committedDetail, committedManifest, ManualClock, recordingFetch, scenarioTrace, scenarioTraces } from '@/test/recording';
import type { Recording, TraceSummary } from '@/types/api';
import { selectScenarios, type Scenario } from './recorder';
import { ReplayPlayer } from './player';
import { checkManifest, MOCK_LABEL, parseDataset } from './recording';
import { createReplayDataSource, featuredTrace, STATIC_UNAVAILABLE } from './replayDataSource';
import { replayEvaluatorReads } from './replayEvaluatorReads';
import { useReplayStore } from './replayStore';
import { percentile, roiBucketOf } from './stats';
import { ReplayTape } from './tape';

const manifest = committedManifest();
const refundId = scenarioTrace('refund-contradiction-flag').trace_id;
const total = manifest.traces.length;
/** The tape opens with this many traces on screen (preroll 0.5). */
const half = Math.floor(total / 2);
/** The scenarios on the tape (the recorder's own selection), and how many traces each scenario expectation covers. */
const onTape = selectScenarios(scenarioPack.scenarios as unknown as Scenario[], 'demo').recorded;
const tracesWhere = (pred: (s: Scenario) => boolean) => onTape.filter(pred).reduce((n, s) => n + scenarioTraces(s.id).length, 0);

function recorded(): Recording {
  return {
    ...structuredClone(manifest),
    kind: 'recorded',
    label: 'Recorded 28 Sep 2026 · typesafe/jev-1.13 + anthropic/claude-haiku-4.5 · scripted answers · replayed, not live',
    decision_model: { backend: 'openrouter-systemone', snapshot: 'typesafe/jev-1.13' },
    judge: { provider: 'anthropic', model: 'claude-haiku-4.5', fallback_active: false },
  };
}

function expectApiError(fn: () => unknown, code: string): void {
  try {
    fn();
  } catch (err) {
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe(code);
    return;
  }
  throw new Error(`expected an ApiError ${code}`);
}

describe('checkManifest', () => {
  it('accepts the committed simulated recording', () => {
    expect(checkManifest(manifest).kind).toBe('mock');
    expect(manifest.label).toBe(MOCK_LABEL);
  });

  it('refuses a mock recording that does not say it is simulated', () => {
    expectApiError(() => checkManifest({ ...manifest, label: 'Recorded today · replayed, not live' }), 'invalid_recording');
  });

  it('refuses a recorded manifest from mock checks, a fallback judge or unlabelled answers', () => {
    expect(checkManifest(recorded()).kind).toBe('recorded');
    expectApiError(() => checkManifest({ ...recorded(), decision_model: { backend: 'mock', snapshot: null } }), 'invalid_recording');
    expectApiError(() => checkManifest({ ...recorded(), judge: { provider: 'anthropic', model: 'x', fallback_active: true } }), 'invalid_recording');
    expectApiError(() => checkManifest({ ...recorded(), judge: { provider: 'mock', model: 'mock', fallback_active: false } }), 'invalid_recording');
    const unsourced = recorded();
    unsourced.traces[0] = { ...unsourced.traces[0], answer_source: null };
    expectApiError(() => checkManifest(unsourced), 'invalid_recording');
    expectApiError(() => checkManifest({ ...recorded(), label: 'Recorded live' }), 'invalid_recording');
  });

  it('refuses other schemas, kinds and shapes', () => {
    expectApiError(() => checkManifest(null), 'invalid_recording');
    expectApiError(() => checkManifest({ ...manifest, schema: 'trustagent.recording/v2' }), 'invalid_recording');
    expectApiError(() => checkManifest({ ...manifest, kind: 'measured' }), 'invalid_recording');
    expectApiError(() => checkManifest({ ...manifest, events: null }), 'invalid_recording');
    expectApiError(() => checkManifest({ ...manifest, recorded_at: 'yesterday' }), 'invalid_recording');
  });

  it('pins the kind a build may present (VITE_DEMO_DATASET)', () => {
    expect(parseDataset(undefined)).toBe('auto');
    expect(parseDataset('recorded')).toBe('recorded');
    expect(parseDataset('surprise')).toBe('auto');
    expectApiError(() => checkManifest(manifest, 'recorded'), 'wrong_dataset');
    expect(checkManifest(manifest, 'mock').kind).toBe('mock');
    expectApiError(() => checkManifest(recorded(), 'mock'), 'wrong_dataset');
  });
});

describe('ReplayTape', () => {
  const tape = new ReplayTape(manifest);
  const started = tape.startedAt(refundId) as number;
  const events = manifest.events.filter((e) => e.trace_id === refundId);
  const decidedAt = events.find((e) => e.type === 'gate_decision')?.t_ms as number;
  const auditedAt = events.find((e) => e.type === 'audit_completed')?.t_ms as number;

  it('knows every trace and the recording length', () => {
    expect(tape.ids()).toHaveLength(total);
    expect(total).toBe(tracesWhere(() => true));
    expect(tape.duration).toBe(Math.max(...manifest.events.map((e) => e.t_ms)));
    expect(tape.timeAt(0)).toBe(manifest.recorded_at);
  });

  it('shows a trace as the stream did: pending, decided with the audit queued, then audited', () => {
    expect(tape.stateAt(refundId, started - 1)).toBeNull();
    const pending = tape.stateAt(refundId, started);
    expect(pending?.summary.gate_status).toBe('pending');
    expect(pending?.decision).toBeNull();

    const decided = tape.stateAt(refundId, decidedAt);
    expect(decided?.summary.action).toBe('flag');
    expect(decided?.summary.audit.status).toBe('queued');
    expect(decided?.audit).toBeNull();

    const done = tape.stateAt(refundId, auditedAt);
    expect(done?.summary).toEqual(manifest.traces.find((t) => t.trace_id === refundId));
    expect(done?.audit?.verdict).toBe('HALLUCINATED');
  });

  it('lists the started traces newest first', () => {
    const early = tape.summariesAt(started);
    expect(early.length).toBeLessThan(total);
    expect(early[0].trace_id).toBe(refundId);
    const all = tape.summariesAt(tape.duration);
    expect(all).toHaveLength(total);
    const times = all.map((t) => Date.parse(t.created_at));
    expect(times).toEqual([...times].sort((a, b) => b - a));
  });

  it('stages a trace file: no deep audit before it played', () => {
    const detail = JSON.parse(JSON.stringify({ ...manifest.traces.find((t) => t.trace_id === refundId), decisions: [], audit_detail: { audit_id: 'x' }, body: null, labels: [] }));
    expect(tape.detailAt(detail, decidedAt).audit_detail).toBeNull();
    expect(tape.detailAt(detail, tape.duration).audit_detail).toEqual({ audit_id: 'x' });
  });

  it('features the flagged refund answer', () => {
    expect(featuredTrace(tape)).toBe(refundId);
  });
});

describe('ReplayPlayer', () => {
  it('plays every event in compressed time, with long gaps capped', () => {
    const tape = new ReplayTape(manifest);
    const clock = new ManualClock();
    const seen: number[] = [];
    let ended = false;
    const player = new ReplayPlayer(tape, { speed: 20, maxGapMs: 1500, clock }, { onEvent: (ev) => seen.push(ev.t_ms), onPosition: () => undefined, onEnd: () => (ended = true) });
    player.start(-1);
    expect(player.playing).toBe(true);
    clock.runAll();
    expect(ended).toBe(true);
    expect(seen).toEqual(manifest.events.map((e) => e.t_ms));
    expect(Math.max(...clock.delays)).toBe(1500);
    // The recording spans hours; the replay takes seconds.
    expect(clock.now).toBeLessThan(tape.duration / 100);
    expect(player.position).toBe(tape.duration);
  });

  it('delivers events recorded at the same instant together', () => {
    const tape = new ReplayTape(manifest);
    const clock = new ManualClock();
    const batches: number[][] = [];
    let batch: number[] = [];
    const player = new ReplayPlayer(tape, { speed: 1, maxGapMs: 10_000, clock }, {
      onEvent: (ev) => batch.push(ev.t_ms),
      onPosition: () => {
        batches.push(batch);
        batch = [];
      },
      onEnd: () => undefined,
    });
    player.start(-1);
    clock.runAll();
    for (const b of batches.filter((x) => x.length > 0)) expect(new Set(b).size).toBe(1);
  });

  it('stops', () => {
    const clock = new ManualClock();
    const player = new ReplayPlayer(new ReplayTape(manifest), { speed: 20, maxGapMs: 1500, clock }, { onEvent: () => undefined, onPosition: () => undefined, onEnd: () => undefined });
    player.start(0);
    player.stop();
    expect(player.playing).toBe(false);
    expect(clock.pending).toBe(0);
  });
});

describe('ReplayDataSource', () => {
  function sink() {
    const events: StreamEvent[] = [];
    const phases: string[] = [];
    const s: StreamSink = { onEvent: (e) => events.push(e), onConnection: (c) => phases.push(c.phase) };
    return { s, events, phases };
  }

  function setup(opts: { manifest?: unknown; fail?: boolean } = {}) {
    const clock = new ManualClock();
    const f = recordingFetch('/TrustAgent/', opts);
    const ds = createReplayDataSource({ basePath: '/TrustAgent/', fetchImpl: f.fetch, clock });
    return { ds, clock, requests: f.requests };
  }

  it('loads the recording from the site itself', async () => {
    const { ds, requests } = setup();
    await ds.ready;
    expect(requests).toEqual(['/TrustAgent/demo/manifest.json']);
    const info = useReplayStore.getState();
    expect(info.status).toBe('ready');
    expect(info.kind).toBe('mock');
    expect(info.label).toBe(MOCK_LABEL);
    expect(info.traces).toBe(total);
    expect(info.featuredTraceId).toBe(refundId);
    expect(info.shown).toBe(half);
  });

  it('opens with half the traffic on screen and plays the rest as live events', async () => {
    const { ds, clock } = setup();
    await ds.ready;
    expect((await ds.listTraces({ limit: 200 })).items).toHaveLength(half);

    const { s, events, phases } = sink();
    const live = ds.subscribe(s);
    await ds.ready;
    await Promise.resolve();
    expect(phases).toEqual(['connecting', 'open']);
    const hello = events[0];
    expect(hello.type).toBe('hello');
    if (hello.type === 'hello') {
      expect(hello.data.readonly).toBe(true);
      expect(hello.data.demo).toBe(true);
      expect(hello.data.protocol).toBe(2);
    }

    clock.runAll();
    const played = events.slice(1);
    expect(played.length).toBeGreaterThan(0);
    expect(played.every((e) => e.type !== 'resync' && e.v === 2)).toBe(true);
    const seqs = played.map((e) => (e.type === 'resync' ? 0 : e.seq));
    expect(seqs).toEqual([...seqs].sort((a, b) => a - b));
    expect(new Set(seqs).size).toBe(seqs.length);
    expect(useReplayStore.getState().finished).toBe(true);
    expect((await ds.listTraces({ limit: 200 })).items).toHaveLength(total);
    live.stop();
  });

  it('plays again from the preroll point', async () => {
    const { ds, clock } = setup();
    await ds.ready;
    const { s, events } = sink();
    ds.subscribe(s);
    await Promise.resolve();
    clock.runAll();
    const first = events.length;
    ds.restart();
    expect(useReplayStore.getState().finished).toBe(false);
    expect((await ds.listTraces({ limit: 200 })).items).toHaveLength(half);
    clock.runAll();
    expect(events.length).toBe(first * 2);
  });

  it('filters and pages traces like GET /api/traces', async () => {
    const { ds, clock } = setup();
    await ds.ready;
    ds.subscribe(sink().s);
    await Promise.resolve();
    clock.runAll();
    expect((await ds.listTraces({ outcome: 'flagged', limit: 200 })).items).toHaveLength(tracesWhere((s) => s.expect.outcome === 'flagged'));
    expect((await ds.listTraces({ outcome: 'blocked', limit: 200 })).items).toHaveLength(tracesWhere((s) => s.expect.outcome === 'blocked'));
    expect((await ds.listTraces({ gate_status: 'not_assessed', limit: 200 })).items).toHaveLength(tracesWhere((s) => s.expect.status === 'not_assessed'));
    const sixty = (await ds.listTraces({ q: '60 days', limit: 200 })).items;
    expect(sixty.map((t) => t.trace_id)).toEqual(expect.arrayContaining(scenarioTraces('refund-contradiction-flag').map((t) => t.trace_id)));
    for (const t of sixty) expect(`${t.question_preview} ${t.response_preview}`.toLowerCase()).toContain('60 days');
    const first = await ds.listTraces({ limit: 5 });
    expect(first.items).toHaveLength(5);
    const second = await ds.listTraces({ limit: 5, cursor: first.next_cursor ?? undefined });
    expect(second.items[0].trace_id).not.toBe(first.items[0].trace_id);
    expect((await ds.listDecisions({ action: 'flag' })).items.every((d) => d.action === 'flag')).toBe(true);
    expect((await ds.listAudits({ verdict: 'HALLUCINATED', limit: 200 })).items).toHaveLength(tracesWhere((s) => s.expect.audit_verdict === 'HALLUCINATED'));
  });

  it('filters by reason like GET /api/traces (D68), over the whole recording, page by page', async () => {
    const { ds, clock } = setup();
    await ds.ready;
    ds.subscribe(sink().s);
    await Promise.resolve();
    clock.runAll();
    /** Every page of a listing (3 per page), and how many pages it took. */
    const everyPage = async (params: TraceListParams) => {
      const ids: string[] = [];
      let cursor: string | undefined;
      let pages = 0;
      do {
        const page = await ds.listTraces({ ...params, limit: 3, cursor });
        expect(page.items.length).toBeLessThanOrEqual(3);
        ids.push(...page.items.map((t) => t.trace_id));
        cursor = page.next_cursor ?? undefined;
        pages += 1;
      } while (cursor !== undefined);
      return { ids, pages };
    };
    const where = (pred: (t: TraceSummary) => boolean) => manifest.traces.filter(pred).map((t) => t.trace_id);
    const sorted = (ids: readonly string[]) => [...ids].sort();

    // A whole code matches that code only.
    const critical = await everyPage({ reason: 'contradiction:critical' });
    const criticalIds = where((t) => t.reasons.includes('contradiction:critical'));
    expect(criticalIds.length).toBeGreaterThan(3);
    expect(sorted(critical.ids)).toEqual(sorted(criticalIds));
    expect(new Set(critical.ids).size).toBe(critical.ids.length);
    expect(critical.pages).toBe(Math.ceil(criticalIds.length / 3));
    expect(critical.ids).toEqual(expect.arrayContaining(scenarioTraces('refund-contradiction-flag').map((t) => t.trace_id)));
    // A prefix ending in a colon matches every code it starts: the evaluator at any level.
    const anyLevel = await everyPage({ reason: 'contradiction:' });
    const anyLevelIds = where((t) => t.reasons.some((r) => r.startsWith('contradiction:')));
    expect(anyLevelIds.length).toBeGreaterThan(criticalIds.length);
    expect(sorted(anyLevel.ids)).toEqual(sorted(anyLevelIds));
    const det = await everyPage({ reason: 'det:' });
    expect(sorted(det.ids)).toEqual(sorted(where((t) => t.reasons.some((r) => r.startsWith('det:')))));
    // A bare id is a whole code, never a prefix.
    expect((await everyPage({ reason: 'contradiction' })).ids).toEqual([]);
    // With the other filters, and without a since: the replay needs no bound.
    const onRoute = await everyPage({ reason: 'det:', route: 'support-bot' });
    expect(onRoute.ids.length).toBeGreaterThan(0);
    expect(sorted(onRoute.ids)).toEqual(sorted(where((t) => t.route === 'support-bot' && t.reasons.some((r) => r.startsWith('det:')))));
  });

  it('suggests evidence patterns from the answers that carried none, as the gateway scans them (D64)', async () => {
    const { ds, clock } = setup();
    await ds.ready;
    ds.subscribe(sink().s);
    await Promise.resolve();
    clock.runAll();
    const untagged = manifest.traces.filter((t) => (t.checked_against?.used ?? []).length === 0);
    expect(untagged.length).toBeGreaterThan(0);
    const { items } = await ds.getEvidenceSuggestions({ window: '24h' });
    // support-bot's shipping prompt keeps its policy in a <policy> tag the doc rules do not know.
    const shipping = scenarioTraces('untagged-custom-tag').map((t) => t.trace_id);
    expect(items[0]).toMatchObject({ kind: 'tag_pattern', label: '<policy> blocks in the system prompt', would_gain: shipping.length, sample: untagged.length });
    expect(items[0].pattern).toEqual({ pattern: '<policy(?:[\\t\\n\\f\\r ][^>]*)?>(.*?)</policy>', flags: 'is' });
    // The example is the most recent answer it would have helped.
    expect(items[0].example_trace_id).toBe(shipping.at(-1));
    expect(items.at(-1)).toMatchObject({ kind: 'extension', pattern: null, sample: untagged.length });
    // Per route, and an honest nothing where every answer carried documents.
    const onRoute = await ds.getEvidenceSuggestions({ window: '24h', route: 'support-bot' });
    expect(onRoute.items[0].sample).toBe(untagged.filter((t) => t.route === 'support-bot').length);
    expect(await ds.getEvidenceSuggestions({ window: '24h', route: 'help-center' })).toEqual({ items: [] });
  });

  it('serves a trace file, and 404 for an id the recording lacks', async () => {
    const { ds, clock } = setup();
    await ds.ready;
    ds.subscribe(sink().s);
    await Promise.resolve();
    clock.runAll();
    const detail = await ds.getTrace(refundId);
    expect(detail.audit_detail?.claims[0].quote).toBe('60 days');
    await expect(ds.getTrace('01a0e700-0000-7000-8000-000000000000')).rejects.toMatchObject({ status: 404, code: 'not_found' });
    const audit = await ds.getAudit(detail.audit_detail?.audit_id as string);
    expect(audit.verdict).toBe('HALLUCINATED');
  });

  it('labels every number of a mock recording simulated', async () => {
    const { ds, clock } = setup();
    await ds.ready;
    ds.subscribe(sink().s);
    await Promise.resolve();
    clock.runAll();
    const o = await ds.getOverview({ window: '24h' });
    expect(o.include_mock).toBe(true);
    expect(o.totals.requests).toBe(total);
    expect(o.totals.flagged).toBe(tracesWhere((s) => s.expect.action === 'flag'));
    expect(o.totals.blocked).toBe(tracesWhere((s) => s.expect.action === 'block'));
    expect(o.hallucination_rate.label).toBe('simulated');
    // No uniform-sample audit on the tape: the rate stays hidden.
    expect(o.hallucination_rate.value).toBeNull();
    expect(o.audited_hallucination_share.label).toBe('simulated');
    // The audited share counts audits with a verdict about the answer, as the gateway does: an
    // audit with nothing to check against (NOT_ASSESSED) is on the tape but not in the share.
    const auditVerdicts = manifest.traces.map((t) => committedDetail(t.trace_id).audit_detail?.verdict ?? null).filter((v) => v !== null);
    expect(auditVerdicts).toContain('NOT_ASSESSED');
    const withVerdict = auditVerdicts.filter((v) => v !== 'NOT_ASSESSED');
    expect(o.audited_hallucination_share.n).toBe(withVerdict.length);
    expect(o.audited_hallucination_share.value).toBeCloseTo(withVerdict.filter((v) => v === 'HALLUCINATED').length / withVerdict.length, 12);
    const assessed =manifest.traces.filter((t) => committedDetail(t.trace_id).decisions[0]?.evaluators['groundedness@1']?.status === 'assessed').length;
    expect(o.evidence_coverage.groundedness_assessed).toBe(assessed);
    expect(o.what_was_caught.map((c) => c.reason)).toContain('contradiction:critical');

    const roi = await ds.getRoiSummary('24h');
    expect(roi.simulated).toBe(true);
    expect(roi.gated_responses).toBe(total);
    for (const v of [roi.decision_cost, roi.llm_cost, roi.total_per_1k, roi.savings_vs_same_checks, roi.outcomes, roi.latency_ms]) expect(v.label).toBe('simulated');
    // The same checks are priced from the simulated fast-check tokens; the full audit is the
    // published assumption the gateway also falls back to (an estimate, never measured).
    expect(roi.counterfactuals.map((c) => [c.kind, c.method, c.label])).toEqual([
      ['llm_same_checks', 'token_model', 'simulated'],
      ['llm_full_claim_audit', 'static_assumptions', 'estimate'],
    ]);
    const full = roi.counterfactuals[1];
    expect(full.per_1k).toBe(35.5);
    expect(full.usd).toBeCloseTo((35.5 * total) / 1000, 12);
    expect(full.basis).toMatch(/^not measured: /);
    expect(roi.llm_cost.usd).toBe(0);

    const coverage = await ds.getEvidenceCoverage({ window: '24h' });
    expect(coverage.requests).toBe(total);
    expect(coverage.groundedness_assessed).toBe(assessed);
    // Why the others were not checked, as the gateway counts it: the groundedness check's own
    // exclusion, else no documents at all. A route with documents but no groundedness check (the
    // assistant's rubric) is not a gap.
    const unchecked = manifest.traces.filter((t) => committedDetail(t.trace_id).decisions[0]?.evaluators['groundedness@1']?.status !== 'assessed');
    const noDocuments = unchecked.filter((t) => (t.checked_against?.used ?? []).length === 0);
    const documentsNoCheck = unchecked.filter((t) => (t.checked_against?.used ?? []).length > 0 && !committedDetail(t.trace_id).decisions[0]?.evaluators['groundedness@1']);
    expect(noDocuments.length).toBeGreaterThan(0);
    expect(documentsNoCheck.length).toBeGreaterThan(0);
    expect(coverage.top_reasons).toEqual([{ reason: 'no_evidence', count: noDocuments.length }]);
    expect(coverage.top_reasons[0].count).toBeLessThan(total - assessed);
  });

  it('serves spend per bucket that adds up to the ROI summary, and the built-in prices', async () => {
    const { ds, clock } = setup();
    await ds.ready;
    ds.subscribe(sink().s);
    await Promise.resolve();
    clock.runAll();
    const roi = await ds.getRoiSummary('24h');
    // The recording spans two hours: hourly buckets, whatever the window asks for.
    const series = await ds.getRoiTimeseries({ window: '24h', bucket: '1d' });
    expect(series).toMatchObject({ window: '24h', bucket: '1h' });
    expect(series.points).toHaveLength(3);
    for (const p of series.points) expect(p.gated).toBeGreaterThan(0);
    // Zero-filled UTC hours, in order.
    for (const p of series.points) expect(Date.parse(p.t) % 3_600_000).toBe(0);
    expect(series.points.map((p) => p.t)).toEqual([...series.points.map((p) => p.t)].sort());
    const sum = (f: (p: (typeof series.points)[number]) => number) => series.points.reduce((s, p) => s + f(p), 0);
    expect(sum((p) => p.gated)).toBe(roi.gated_responses);
    expect(sum((p) => p.decision_usd)).toBeCloseTo(roi.decision_cost.usd, 12);
    expect(sum((p) => p.llm_usd)).toBeCloseTo(roi.llm_cost.usd, 12);
    const same = roi.counterfactuals.find((c) => c.kind === 'llm_same_checks');
    expect(sum((p) => p.same_checks_usd ?? 0)).toBeCloseTo(same?.usd ?? 0, 12);

    // The bucket comes from the whole recording, so a chart keeps it while the tape plays: a tape
    // that opens empty draws per hour (spend) and per 15 minutes (outcomes) from its first moment.
    expect((await ds.getOverview({ window: '24h' })).bucket).toBe('15m');
    const f = recordingFetch('/TrustAgent/');
    const fromStart = createReplayDataSource({ basePath: '/TrustAgent/', fetchImpl: f.fetch, clock: new ManualClock(), preroll: 0 });
    await fromStart.ready;
    expect((await fromStart.listTraces({ limit: 200 })).items.length).toBeLessThanOrEqual(1);
    const opening = await fromStart.getRoiTimeseries({ window: '24h', bucket: '1d' });
    expect(opening.bucket).toBe('1h');
    expect(opening.points).toHaveLength(1);
    expect((await fromStart.getOverview({ window: '24h' })).bucket).toBe('15m');

    const week = await ds.getRoiTimeseries({ window: '7d', bucket: '1d' });
    expect(week.bucket).toBe('1h');
    expect(week.points.reduce((s, p) => s + p.gated, 0)).toBe(roi.gated_responses);

    const prices = await ds.getPricing();
    expect(prices.entries.length).toBeGreaterThan(0);
    expect(prices.entries.every((e) => e.label !== 'override')).toBe(true);
  });

  it('says there is no gateway for everything else', async () => {
    const { ds } = setup();
    await ds.ready;
    await expect(ds.getPolicies()).rejects.toMatchObject({ status: 404, message: STATIC_UNAVAILABLE });
    await expect(ds.getSettings()).rejects.toMatchObject({ status: 404, message: STATIC_UNAVAILABLE });
    await expect(ds.getStatus()).rejects.toMatchObject({ status: 404 });
    expect((await ds.getHealth()).demo).toBe(true);
  });

  it('answers the evaluator reads through the replay plane (D107)', async () => {
    const { ds } = setup();
    await ds.ready;
    const plane = replayEvaluatorReads({
      message: STATIC_UNAVAILABLE,
      traceIds: async () => (await ds.ready).ids(),
      trace: (id, signal) => ds.getTrace(id, signal),
    });
    const settle = (p: Promise<unknown>) =>
      p.then(
        (value) => ({ value }),
        (err: unknown) => ({ error: err instanceof ApiError ? { status: err.status, code: err.code, message: err.message } : String(err) }),
      );
    expect(await settle(ds.listEvaluators({}))).toEqual(await settle(plane.listEvaluators({})));
    expect(await settle(ds.getEvaluator('groundedness'))).toEqual(await settle(plane.getEvaluator('groundedness')));
    expect(await settle(ds.getEvaluatorVersion('groundedness', 1))).toEqual(await settle(plane.getEvaluatorVersion('groundedness', 1)));
  });

  it('rebuild is deterministic: every simulated decision rebuilds the requests it recorded (D109)', async () => {
    const { ds, clock } = setup();
    await ds.ready;
    ds.subscribe(sink().s);
    await Promise.resolve();
    clock.runAll();
    let asked = 0;
    let fallbackChecks = 0;
    for (const t of manifest.traces) {
      const detail = committedDetail(t.trace_id);
      expect(detail.decisions.length, t.trace_id).toBeGreaterThan(0);
      for (const d of detail.decisions) {
        const view = await ds.getDecisionRequest(t.trace_id, { attempt: d.attempt });
        expect(view.request_sha256, `${t.trace_id} attempt ${d.attempt}`).toEqual(d.compile?.request_sha256 ?? []);
        expect(view.matches_recorded).toBe(true);
        expect(view.mismatch_reason).toBeNull();
        expect(view.cpt).toBe(d.compile?.cpt);
        expect(view.requests).toHaveLength(d.compile?.requests ?? 0);
        // A decision the fast check answered shows what it was asked; one it was not asked, nothing.
        const questions = view.requests.flatMap((r) => Object.keys(r.questions));
        if (d.decision_model) {
          expect(questions.length).toBeGreaterThan(0);
          asked += 1;
        } else expect(questions).toEqual([]);
        // A fallback check is rebuilt from the fallback answer that was delivered.
        if (d.attempt === 1) fallbackChecks += 1;
      }
    }
    expect(asked).toBeGreaterThan(0);
    expect(fallbackChecks).toBe(scenarioTraces('payments-fallback').length);
    expect(fallbackChecks).toBeGreaterThan(0);
    // An attempt the trace has no decision for, and a trace the recording lacks.
    await expect(ds.getDecisionRequest(refundId, { attempt: 1 })).rejects.toMatchObject({ status: 404, code: 'not_found' });
    await expect(ds.getDecisionRequest('01a0e700-0000-7000-8000-000000000000')).rejects.toMatchObject({ status: 404, code: 'not_found' });
  });

  it('reports a recording it cannot load', async () => {
    const { ds } = setup({ fail: true });
    await expect(ds.ready).rejects.toMatchObject({ code: 'replay_unavailable' });
    expect(useReplayStore.getState().status).toBe('error');
    await expect(ds.listTraces({})).rejects.toBeInstanceOf(ApiError);
  });

  it('refuses a mislabelled manifest', async () => {
    const { ds } = setup({ manifest: { ...manifest, label: 'Recorded today · replayed, not live' } });
    await expect(ds.ready).rejects.toMatchObject({ code: 'invalid_recording' });
    expect(useReplayStore.getState().error).toMatch(/Simulated \(mock checks \+ mock judge\)/);
  });
});

describe('percentile', () => {
  it('is nearest-rank over the numbers only', () => {
    expect(percentile([], 50)).toBeNull();
    expect(percentile([null, 3, 1, 2, undefined], 50)).toBe(2);
    expect(percentile([1, 2, 3, 4], 95)).toBe(4);
  });
});

describe('roiBucketOf', () => {
  it('draws minutes up to an hour, hours up to two days, days beyond', () => {
    const h = 3_600_000;
    expect([0, 226_457, h, h + 1, 48 * h, 48 * h + 1, Number.NaN].map(roiBucketOf)).toEqual(['1m', '1m', '1m', '1h', '1h', '1d', '1m']);
  });
});

// ------------------------------------------------------------------------------------ dataset plugin

// vite/plugins/dataset.ts runs in Node at build time; the app's tsconfig has no Node types, so it
// and the fs helpers load through variable specifiers (see recorder/demoRecording.test.ts).
interface DatasetPluginModule {
  chooseDataset: (dataDir: string, value: string | undefined) => { dir: string; expected: 'mock' | 'recorded' };
  datasetPlugin: (webDir: string, value?: string) => { resolveId: (id: string) => string | undefined; load: (id: string) => string | undefined };
  DATASET_MODULE: string;
}
const nodeSpecifiers = ['node:fs', 'node:os', 'node:path', 'node:url'];
const [fsMod, osMod, pathMod, urlMod] = (await Promise.all(nodeSpecifiers.map((m) => import(/* @vite-ignore */ m)))) as [
  { mkdtempSync: (prefix: string) => string; mkdirSync: (p: string, o: { recursive: boolean }) => void; writeFileSync: (p: string, t: string) => void; rmSync: (p: string, o: { recursive: boolean; force: boolean }) => void },
  { tmpdir: () => string },
  { join: (...parts: string[]) => string },
  { fileURLToPath: (url: string) => string },
];
const pluginHere = import.meta.url;
const webDir = urlMod.fileURLToPath(new URL('../../../', pluginHere).href);
const { chooseDataset, datasetPlugin, DATASET_MODULE } = (await import(/* @vite-ignore */ new URL('../../../vite/plugins/dataset.ts', pluginHere).href)) as DatasetPluginModule;

describe('dataset plugin (D110)', () => {
  const made: string[] = [];
  afterAll(() => {
    for (const dir of made) fsMod.rmSync(dir, { recursive: true, force: true });
  });

  /** A web dir whose demo-data holds the given manifests (undefined: no such directory). */
  function webWith(manifests: { simulated?: unknown; recorded?: unknown }): string {
    const web = fsMod.mkdtempSync(pathMod.join(osMod.tmpdir(), 'ta-dataset-'));
    made.push(web);
    for (const [name, doc] of Object.entries(manifests)) {
      if (doc === undefined) continue;
      const dir = pathMod.join(web, 'demo-data', name);
      fsMod.mkdirSync(pathMod.join(dir, 'traces'), { recursive: true });
      fsMod.writeFileSync(pathMod.join(dir, 'manifest.json'), typeof doc === 'string' ? doc : JSON.stringify(doc));
    }
    return web;
  }
  const data = (web: string) => pathMod.join(web, 'demo-data');
  const both = () => webWith({ simulated: { kind: 'mock' }, recorded: { kind: 'recorded' } });

  it('mock takes the simulated tape, even when a recording exists', () => {
    const web = both();
    expect(chooseDataset(data(web), 'mock')).toEqual({ dir: pathMod.join(data(web), 'simulated'), expected: 'mock' });
  });

  it('recorded takes the recording, and fails the build without one', () => {
    const web = both();
    expect(chooseDataset(data(web), 'recorded')).toEqual({ dir: pathMod.join(data(web), 'recorded'), expected: 'recorded' });
    expect(() => chooseDataset(data(webWith({ simulated: { kind: 'mock' } })), 'recorded')).toThrow(/VITE_DEMO_DATASET=recorded, but .*manifest\.json does not exist/);
  });

  it('auto (and any other value) takes the recording when there is one, else the simulated tape', () => {
    const web = both();
    for (const value of ['auto', undefined, 'surprise']) expect(chooseDataset(data(web), value).expected).toBe('recorded');
    const simulatedOnly = webWith({ simulated: { kind: 'mock' } });
    for (const value of ['auto', undefined]) expect(chooseDataset(data(simulatedOnly), value)).toEqual({ dir: pathMod.join(data(simulatedOnly), 'simulated'), expected: 'mock' });
  });

  it('fails every build on a mislabelled recorded/ manifest', () => {
    const web = webWith({ simulated: { kind: 'mock' }, recorded: { kind: 'mock', label: 'Simulated (mock checks + mock judge): not model output' } });
    for (const value of ['mock', 'recorded', 'auto']) expect(() => chooseDataset(data(web), value)).toThrow(/recorded\/manifest\.json is kind "mock", not "recorded"/);
    expect(() => chooseDataset(data(webWith({ simulated: { kind: 'mock' }, recorded: 'not json' })), 'auto')).toThrow(/is not a readable JSON manifest/);
  });

  it('fails the build when the simulated tape is missing or is not kind mock', () => {
    expect(() => chooseDataset(data(webWith({})), 'mock')).toThrow(/simulated\/manifest\.json does not exist/);
    expect(() => chooseDataset(data(webWith({ simulated: { kind: 'recorded' } })), 'auto')).toThrow(/simulated\/manifest\.json is kind "recorded", not "mock"/);
  });

  it('tells the replay which kind to expect, and fails the build through the plugin', () => {
    const plugin = datasetPlugin(both(), 'recorded');
    const id = plugin.resolveId(DATASET_MODULE) as string;
    expect(id).toBe(`\0${DATASET_MODULE}`);
    expect(plugin.load(id)).toBe('export const expected = "recorded";\n');
    expect(plugin.resolveId('virtual:trustagent/other')).toBeUndefined();
    const bad = datasetPlugin(webWith({ simulated: { kind: 'mock' }, recorded: { kind: 'mock' } }), 'mock');
    expect(() => bad.load(bad.resolveId(DATASET_MODULE) as string)).toThrow(/not "recorded"/);
  });

  it('resolves the committed demo-data to the simulated tape', () => {
    expect(chooseDataset(pathMod.join(webDir, 'demo-data'), 'mock').expected).toBe('mock');
    expect(chooseDataset(pathMod.join(webDir, 'demo-data'), 'auto').expected).toBe(manifest.kind);
  });
});

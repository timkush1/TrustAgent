/**
 * ReplayDataSource over the committed demo recording: the manifest checks, the tape's view of each
 * trace over time, the compressed-time player, and the DataSource contract the pages rely on
 * (reads at the tape position, a read-only hello, labelled numbers, the unavailable control plane).
 */
import { describe, expect, it } from 'vitest';
import { ApiError } from '../errors';
import type { StreamSink } from '../source';
import type { StreamEvent } from '../stream/types';
import { committedManifest, ManualClock, recordingFetch } from '@/test/recording';
import type { Recording } from '@/types/api';
import { ReplayPlayer } from './player';
import { checkManifest, MOCK_LABEL, parseDataset } from './recording';
import { createReplayDataSource, featuredTrace, STATIC_UNAVAILABLE } from './replayDataSource';
import { useReplayStore } from './replayStore';
import { percentile, roiBucketOf } from './stats';
import { ReplayTape } from './tape';

const manifest = committedManifest();
const refundId = manifest.traces.find((t) => t.action === 'flag')?.trace_id as string;

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
    expect(tape.ids()).toHaveLength(18);
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
    expect(early.length).toBeLessThan(18);
    expect(early[0].trace_id).toBe(refundId);
    const all = tape.summariesAt(tape.duration);
    expect(all).toHaveLength(18);
    const times = all.map((t) => Date.parse(t.created_at));
    expect(times).toEqual([...times].sort((a, b) => b - a));
  });

  it('stages a trace file: no deep audit before it played', () => {
    const detail = JSON.parse(JSON.stringify({ ...manifest.traces.find((t) => t.trace_id === refundId), decisions: [], audit_detail: { audit_id: 'x' }, body: null, labels: [] }));
    expect(tape.detailAt(detail, decidedAt).audit_detail).toBeNull();
    expect(tape.detailAt(detail, tape.duration).audit_detail).toEqual({ audit_id: 'x' });
  });

  it('features the flagged refund answer', () => {
    expect(featuredTrace(tape)).toBe(manifest.traces.find((t) => t.action === 'flag' && t.audit.verdict === 'HALLUCINATED')?.trace_id);
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
    // The recording spans minutes; the replay takes seconds.
    expect(clock.now).toBeLessThan(tape.duration / 10);
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
    expect(info.traces).toBe(18);
    expect(info.featuredTraceId).toBe(refundId);
    expect(info.shown).toBe(9);
  });

  it('opens with half the traffic on screen and plays the rest as live events', async () => {
    const { ds, clock } = setup();
    await ds.ready;
    expect((await ds.listTraces({})).items).toHaveLength(9);

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
    expect((await ds.listTraces({})).items).toHaveLength(18);
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
    expect((await ds.listTraces({})).items).toHaveLength(9);
    clock.runAll();
    expect(events.length).toBe(first * 2);
  });

  it('filters and pages traces like GET /api/traces', async () => {
    const { ds, clock } = setup();
    await ds.ready;
    ds.subscribe(sink().s);
    await Promise.resolve();
    clock.runAll();
    expect((await ds.listTraces({ outcome: 'flagged' })).items).toHaveLength(6);
    expect((await ds.listTraces({ gate_status: 'not_assessed' })).items).toHaveLength(6);
    expect((await ds.listTraces({ q: '60 days' })).items).toHaveLength(6);
    const first = await ds.listTraces({ limit: 5 });
    expect(first.items).toHaveLength(5);
    const second = await ds.listTraces({ limit: 5, cursor: first.next_cursor ?? undefined });
    expect(second.items[0].trace_id).not.toBe(first.items[0].trace_id);
    expect((await ds.listDecisions({ action: 'flag' })).items.every((d) => d.action === 'flag')).toBe(true);
    expect((await ds.listAudits({ verdict: 'HALLUCINATED' })).items).toHaveLength(6);
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
    expect(o.totals.requests).toBe(18);
    expect(o.totals.flagged).toBe(6);
    expect(o.hallucination_rate.label).toBe('simulated');
    // Fewer than 30 samples: the rate stays hidden.
    expect(o.hallucination_rate.value).toBeNull();
    expect(o.audited_hallucination_share.value).toBeNull();
    expect(o.evidence_coverage.groundedness_assessed).toBe(12);
    expect(o.what_was_caught.map((c) => c.reason)).toContain('contradiction:critical');

    const roi = await ds.getRoiSummary('24h');
    expect(roi.simulated).toBe(true);
    expect(roi.gated_responses).toBe(18);
    for (const v of [roi.decision_cost, roi.llm_cost, roi.total_per_1k, roi.savings_vs_same_checks, roi.outcomes, roi.latency_ms]) expect(v.label).toBe('simulated');
    // The same checks are priced from the simulated fast-check tokens; the full audit is the
    // published assumption the gateway also falls back to (an estimate, never measured).
    expect(roi.counterfactuals.map((c) => [c.kind, c.method, c.label])).toEqual([
      ['llm_same_checks', 'token_model', 'simulated'],
      ['llm_full_claim_audit', 'static_assumptions', 'estimate'],
    ]);
    const full = roi.counterfactuals[1];
    expect(full.per_1k).toBe(35.5);
    expect(full.usd).toBeCloseTo((35.5 * 18) / 1000, 12);
    expect(full.basis).toMatch(/^not measured: /);
    expect(roi.llm_cost.usd).toBe(0);

    const coverage = await ds.getEvidenceCoverage({ window: '24h' });
    expect(coverage.requests).toBe(18);
    expect(coverage.top_reasons).toEqual([{ reason: 'no_evidence', count: 6 }]);
  });

  it('serves spend per bucket that adds up to the ROI summary, and the built-in prices', async () => {
    const { ds, clock } = setup();
    await ds.ready;
    ds.subscribe(sink().s);
    await Promise.resolve();
    clock.runAll();
    const roi = await ds.getRoiSummary('24h');
    // The recording spans under four minutes: per-minute buckets, whatever the window asks for.
    const series = await ds.getRoiTimeseries({ window: '24h', bucket: '1h' });
    expect(series).toMatchObject({ window: '24h', bucket: '1m' });
    expect(series.points).toHaveLength(4);
    // Zero-filled UTC minutes, in order.
    for (const p of series.points) expect(Date.parse(p.t) % 60_000).toBe(0);
    expect(series.points.map((p) => p.t)).toEqual([...series.points.map((p) => p.t)].sort());
    const sum = (f: (p: (typeof series.points)[number]) => number) => series.points.reduce((s, p) => s + f(p), 0);
    expect(sum((p) => p.gated)).toBe(roi.gated_responses);
    expect(sum((p) => p.decision_usd)).toBeCloseTo(roi.decision_cost.usd, 12);
    expect(sum((p) => p.llm_usd)).toBeCloseTo(roi.llm_cost.usd, 12);
    const same = roi.counterfactuals.find((c) => c.kind === 'llm_same_checks');
    expect(sum((p) => p.same_checks_usd ?? 0)).toBeCloseTo(same?.usd ?? 0, 12);

    const week = await ds.getRoiTimeseries({ window: '7d', bucket: '1d' });
    expect(week.bucket).toBe('1m');
    expect(week.points.reduce((s, p) => s + p.gated, 0)).toBe(roi.gated_responses);

    const prices = await ds.getPricing();
    expect(prices.entries.length).toBeGreaterThan(0);
    expect(prices.entries.every((e) => e.label !== 'override')).toBe(true);
  });

  it('says there is no gateway for everything else', async () => {
    const { ds } = setup();
    await ds.ready;
    await expect(ds.getPolicies()).rejects.toMatchObject({ status: 404, message: STATIC_UNAVAILABLE });
    await expect(ds.listEvaluators({})).rejects.toMatchObject({ status: 404 });
    await expect(ds.getStatus()).rejects.toMatchObject({ status: 404 });
    expect((await ds.getHealth()).demo).toBe(true);
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

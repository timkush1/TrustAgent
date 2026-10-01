import { describe, expect, it } from 'vitest';
import scenarios from '@contracts/demo/scenarios.v1.json';
import { flaggedTrace, trace } from '@/test/fixtures';
import type { ClaimVerdict, DecisionBackend, GateAction, GateStatus, WouldAction } from '@/types/api';
import { copy, OUTCOME_IDS, outcomesContract } from './contracts';
import {
  auditStateView,
  AVOID_IN_PRIMARY_UI,
  CLAIM_VERDICT_ORDER,
  checkedAgainst,
  claimVerdictView,
  commandText,
  decisionBackendLabel,
  fillTemplate,
  flagWord,
  gateVerdictView,
  glossaryTerm,
  headlineReason,
  judgeProviderLabel,
  levelWord,
  outcomeOf,
  outcomeView,
  reasonCopy,
  splitCommands,
  TIER,
  traceOutcome,
} from './presentation';

describe('outcomeOf (SPEC §2.9 "Outcome for the UI")', () => {
  it.each(
    scenarios.scenarios
      .filter((s) => s.expect.outcome !== undefined && s.expect.status !== 'deferred')
      .map((s) => [s.id, s.expect] as const),
  )('gives the expected outcome for demo scenario %s', (_id, x) => {
    expect(
      outcomeOf({
        gate_status: x.status.split(':')[0] as GateStatus,
        action: x.action as GateAction,
        would_action: x.would_action as WouldAction,
      }),
    ).toBe(x.outcome);
  });

  it.each([
    [{ gate_status: 'pending', action: null, would_action: null }, 'checking'],
    [{ gate_status: 'ok', action: 'block', would_action: 'block' }, 'blocked'],
    [{ gate_status: 'error', action: 'block', would_action: null }, 'blocked'],
    [{ gate_status: 'ok', action: 'fallback', would_action: 'block' }, 'replaced'],
    [{ gate_status: 'ok', action: 'observe', would_action: 'block' }, 'would_block'],
    [{ gate_status: 'ok', action: 'observe', would_action: 'flag' }, 'would_flag'],
    [{ gate_status: 'ok', action: 'observe', would_action: 'pass' }, 'passed'],
    [{ gate_status: 'timeout', action: 'pass', would_action: null }, 'error'],
    [{ gate_status: 'skipped', action: 'pass', would_action: null }, 'not_checked'],
    [{ gate_status: 'partial', action: 'pass', would_action: 'pass' }, 'passed'],
  ] as const)('%o → %s', (input, expected) => {
    expect(outcomeOf(input)).toBe(expected);
  });

  it('every outcome has a view with an icon, a tone and a distinct shape-or-icon', () => {
    const signatures = new Set<string>();
    for (const id of OUTCOME_IDS) {
      const v = outcomeView(id);
      expect(v.label).not.toBe('');
      expect(v.icon).toBeTruthy();
      signatures.add(`${v.icon.displayName ?? String(v.icon)}|${v.emphasis}|${v.tone}`);
    }
    expect(signatures.size).toBe(OUTCOME_IDS.length);
  });
});

describe('traceOutcome headlines', () => {
  it('reads the flagged scenario', () => {
    const o = traceOutcome(flaggedTrace());
    expect(o.id).toBe('flagged');
    expect(o.headlineText).toBe('Delivered with a risk warning to your app');
  });

  it('explains a not-checked trace without documents', () => {
    const o = traceOutcome(trace({ gate_status: 'not_assessed', action: 'pass', would_action: 'pass' }));
    expect(o.id).toBe('not_checked');
    expect(o.headlineText).toBe('Not checked: no documents in this request');
  });

  it('uses the skip reason, then the degraded cause', () => {
    expect(traceOutcome(trace({ gate_status: 'skipped', action: 'pass', skip_reason: 'budget' })).headlineText).toBe(
      "Not checked: today's fast-check budget is used up",
    );
    expect(traceOutcome({ ...trace({ gate_status: 'timeout', action: 'pass' }), degraded: 'timeout' }).headlineText).toBe(
      'Passed unchecked: the check timed out',
    );
  });
});

describe('reasonCopy', () => {
  it('puts the refund scenario reasons in plain words', () => {
    const reasons = flaggedTrace().reasons;
    expect(reasons.map((r) => reasonCopy(r, { finding: '60' }))).toEqual([
      'Contradicts your documents',
      "States facts your documents don't contain",
      'Mentions a number not in your documents: 60',
    ]);
    expect(headlineReason(reasons)).toBe('Contradicts your documents');
    expect(headlineReason([])).toBeNull();
  });

  it('drops a missing finding cleanly', () => {
    expect(reasonCopy('det:numbers_not_in_evidence')).toBe('Mentions a number not in your documents');
    expect(reasonCopy('fail_closed')).toBe("Blocked because the check couldn't finish and this route fails closed");
    expect(reasonCopy('fail_closed', { degraded: 'timeout' })).toBe(
      "Blocked because the check couldn't finish (the check timed out) and this route fails closed",
    );
  });

  it('words the fallback outcomes the gateway appends to a trace (D19)', () => {
    expect(reasonCopy('fallback_failed')).toBe("Couldn't get a usable fallback answer from your model");
    expect(reasonCopy('fallback_format_mismatch')).toBe(
      'No fallback was tried: the fallback model uses a different API format than this request',
    );
  });

  it('falls back to "{evaluator}: {level}" for unknown evaluators', () => {
    expect(reasonCopy('custom_tone:warn')).toBe('Custom tone: Elevated risk');
    expect(reasonCopy('custom_tone:warn', { evaluatorName: () => 'House style' })).toBe('House style: Elevated risk');
  });
});

describe('levels, verdicts and audit states', () => {
  it('uses the three level words', () => {
    expect([levelWord('ok').label, levelWord('warn').label, levelWord('critical').label]).toEqual([
      'Low risk',
      'Elevated risk',
      'High risk',
    ]);
  });

  it('gives every claim verdict its own label, icon and underline (never color alone)', () => {
    const verdicts: ClaimVerdict[] = ['SUPPORTED', 'PARTIALLY_SUPPORTED', 'CONTRADICTED', 'UNSUPPORTED', 'UNKNOWN', 'NOT_CHECKED'];
    expect([...CLAIM_VERDICT_ORDER].sort()).toEqual([...verdicts].sort());
    const views = verdicts.map(claimVerdictView);
    expect(new Set(views.map((v) => v.label)).size).toBe(verdicts.length);
    expect(new Set(views.map((v) => v.icon)).size).toBe(verdicts.length);
    expect(new Set(views.map((v) => v.underline)).size).toBe(verdicts.length);
    expect(CLAIM_VERDICT_ORDER[0]).toBe('CONTRADICTED');
  });

  it('draws gate verdicts like the matching claim verdicts', () => {
    expect(gateVerdictView('contradicted')).toBe(claimVerdictView('CONTRADICTED'));
    expect(gateVerdictView('not_assessed')).toBe(claimVerdictView('NOT_CHECKED'));
  });

  it('describes audit states, including NOT_ASSESSED either way it arrives', () => {
    expect(auditStateView({ status: 'completed', verdict: 'GROUNDED' }).label).toBe('Grounded');
    expect(auditStateView({ status: 'completed', verdict: 'HALLUCINATED' }).tone).toBe('danger');
    expect(auditStateView({ status: 'completed', verdict: 'NOT_ASSESSED' }).label).toBe('Not assessed');
    expect(auditStateView({ status: 'not_assessed', verdict: null }).label).toBe('Not assessed');
    expect(auditStateView({ status: 'degraded', verdict: 'UNVERIFIABLE' }).label).toBe('Unverifiable (incomplete)');
    expect(auditStateView({ status: 'queued', verdict: null }).pending).toBe(true);
  });
});

describe('copy helpers', () => {
  it('fills placeholders and resolves {command:id}', () => {
    expect(fillTemplate('Ready · {latency_ms} ms · {cost}', { latency_ms: 231, cost: '$0.00001' })).toBe('Ready · 231 ms · $0.00001');
    expect(fillTemplate('To use your own keys: {command:demo_to_own_keys}')).toBe('To use your own keys: make demo-down && make up');
    expect(commandText('nope')).toBeNull();
  });

  it('splits commands out of filled copy', () => {
    expect(splitCommands('Run make demo-down && make up now')).toEqual([
      { text: 'Run ', command: false },
      { text: 'make demo-down && make up', command: true },
      { text: ' now', command: false },
    ]);
  });

  it('says what an answer was checked against', () => {
    expect(checkedAgainst(flaggedTrace().checked_against).text).toBe('Checked against: refund-policy.md');
    expect(checkedAgainst(null)).toEqual({ items: [], none: true, text: 'Checked against: No documents' });
    expect(
      checkedAgainst({
        configured: [],
        used: [{ source: 'tool_results', items: 2, labels: [], tokens: 10 }],
        empty: [],
        knowledge: 'not_requested',
        truncated: false,
        tokens: 10,
      }).items,
    ).toEqual(['Tool results']);
  });

  it('words trace flags, and humanizes one it does not know', () => {
    expect(flagWord('fallback_used')).toBe('Fallback answer delivered');
    expect(flagWord('multi_choice')).toBe('Several answers in one response: only the first was checked');
    expect(flagWord('client_disconnected')).toBe('The client disconnected before the answer finished');
    expect(flagWord('late_decision')).toBe('The fast check answered late: recorded, not enforced');
    expect(flagWord('brand_new_flag')).toBe('Brand new flag');
    expect(flagWord('toString')).toBe('ToString');
  });

  it('keeps jargon out of the trace copy', () => {
    const words = JSON.stringify(copy.trace);
    for (const jargon of AVOID_IN_PRIMARY_UI) expect(words).not.toContain(jargon);
  });

  it('uses the fixed tier names', () => {
    expect(TIER.fast_check).toBe('Fast check (Jev)');
    expect(TIER.deep_audit).toBe('Deep audit');
  });

  it('resolves glossary terms and keeps jargon out of the outcome words', () => {
    expect(glossaryTerm('not_checked')?.term).toBe('Not checked');
    expect(glossaryTerm('nope')).toBeNull();
    const words = outcomesContract.outcomes.flatMap((o) => [o.label, o.headline, o.next_step ?? '']).join(' ');
    for (const jargon of AVOID_IN_PRIMARY_UI) expect(words).not.toContain(jargon);
  });
});

describe('model names', () => {
  it('words every fast-check backend, never the raw value', () => {
    const backends: DecisionBackend[] = [
      'openrouter-decisions',
      'openrouter-systemone',
      'typesafe',
      'compatible',
      'mock',
      'off',
    ];
    for (const b of backends) expect(decisionBackendLabel(b)).not.toBe(b);
    expect(decisionBackendLabel('mock')).toBe('Mock (simulated)');
    expect(decisionBackendLabel('openrouter-decisions')).toBe('OpenRouter');
  });

  it('words the known judge providers and keeps an unknown one as given', () => {
    expect(judgeProviderLabel('anthropic')).toBe('Anthropic');
    expect(judgeProviderLabel('mock')).toBe('Mock (simulated)');
    expect(judgeProviderLabel('some-new-provider')).toBe('some-new-provider');
  });
});

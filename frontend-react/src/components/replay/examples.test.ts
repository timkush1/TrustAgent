import { describe, expect, it } from 'vitest';
import { iso, trace } from '@/test/fixtures';
import type { TraceSummary } from '@/types/api';
import { outcomeOf } from '@/domain/presentation';
import { distinctExamples } from './examples';

const grounded = { question_preview: 'How long do I have?', response_preview: 'Within 30 days.' };
const refund = { question_preview: 'Can I still get a refund?', response_preview: 'Within 60 days.', action: 'flag' as const };
const shipping = { question_preview: 'When does it ship?', response_preview: 'Tomorrow.' };

function run(id: number, at: number, example: Pick<TraceSummary, 'question_preview' | 'response_preview'> & { action?: 'pass' | 'flag' }, done = true): TraceSummary {
  const { action = 'pass', ...texts } = example;
  return trace({
    trace_id: `0192f3a0-0000-7000-8000-00000000000${id}`,
    created_at: iso(at),
    ...texts,
    ...(done ? { gate_status: 'ok', action, would_action: action } : { gate_status: 'pending', response_preview: null }),
  });
}

describe('distinctExamples', () => {
  it('keeps one card per example, opening its newest run, in the order the tape first showed them', () => {
    const rows = [run(4, 40_000, refund), run(3, 30_000, grounded), run(2, 10_000, refund), run(1, 0, grounded)];
    const out = distinctExamples(rows);
    expect(out.map((t) => t.question_preview)).toEqual([grounded.question_preview, refund.question_preview]);
    expect(out.map((t) => t.trace_id)).toEqual([rows[1].trace_id, rows[0].trace_id]);
  });

  it('keeps one card per outcome: a later example with an outcome already shown stays on Traffic only', () => {
    const rows = [run(4, 40_000, refund), run(3, 30_000, shipping), run(2, 10_000, refund), run(1, 0, grounded)];
    const out = distinctExamples(rows);
    expect(out.map((t) => t.question_preview)).toEqual([grounded.question_preview, refund.question_preview]);
    expect(out.map((t) => outcomeOf(t))).toEqual(['passed', 'flagged']);
  });

  it('leaves out a run that is still being checked (it has no answer yet)', () => {
    const rows = [run(3, 30_000, grounded, false), run(2, 10_000, refund), run(1, 0, grounded)];
    const out = distinctExamples(rows);
    expect(out).toHaveLength(2);
    expect(out.every((t) => t.gate_status !== 'pending')).toBe(true);
    expect(out[0].trace_id).toBe(rows[2].trace_id);
  });
});

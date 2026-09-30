/**
 * The timing waterfall's rows (SPEC §3.3 "Trace detail" 3): when each step of the request ran, in
 * milliseconds from the request reaching the proxy. The model's answer comes first; on a route that
 * checks before answering, the fast check follows it and is part of what the proxy added; on an
 * observe route the answer leaves first and the check runs after it. The first token of a streamed
 * answer is a point on the model's row.
 */
import { formatDurationMs } from '@/domain/format';
import { copy } from '@/domain/presentation';
import type { TraceSummary } from '@/types/api';

export type WaterfallKey = 'upstream' | 'ttft' | 'gate' | 'overhead' | 'total';

export interface WaterfallRow {
  key: WaterfallKey;
  label: string;
  /** Milliseconds from the request reaching the proxy. */
  start: number;
  end: number;
  /** A moment (first token), not a span. */
  point: boolean;
  /** A qualifier in words ("after the answer was sent"). */
  note: string | null;
}

export interface WaterfallInput {
  timings: TraceSummary['timings'];
  /** The decision's gate_ms when the trace does not carry one. */
  gateMs?: number | null;
  /** The answer went out before the check (observe). */
  deferred: boolean;
}

const known = (v: number | null | undefined): v is number => typeof v === 'number' && Number.isFinite(v) && v >= 0;

export function waterfallRows({ timings: t, gateMs, deferred }: WaterfallInput): WaterfallRow[] {
  const rows: WaterfallRow[] = [];
  const upstream = known(t.upstream_ms) ? t.upstream_ms : null;
  const total = known(t.total_ms) ? t.total_ms : null;
  const gate = known(t.gate_ms) ? t.gate_ms : known(gateMs) ? gateMs : null;
  const overhead = known(t.overhead_ms) ? t.overhead_ms : null;
  if (upstream !== null) rows.push({ key: 'upstream', label: 'Model answer', start: 0, end: upstream, point: false, note: null });
  if (known(t.ttft_ms)) rows.push({ key: 'ttft', label: 'First token', start: t.ttft_ms, end: t.ttft_ms, point: true, note: null });
  if (gate !== null) {
    const start = deferred ? (total ?? upstream ?? 0) : (upstream ?? 0);
    rows.push({
      key: 'gate',
      label: copy.tiers.fast_check,
      start,
      end: start + gate,
      point: false,
      note: deferred ? 'after the answer was sent' : null,
    });
  }
  if (overhead !== null) {
    const start = upstream ?? (total !== null ? Math.max(0, total - overhead) : 0);
    rows.push({
      key: 'overhead',
      label: 'Proxy overhead',
      start,
      end: start + overhead,
      point: false,
      note: !deferred && gate !== null ? 'includes the fast check' : null,
    });
  }
  if (total !== null) rows.push({ key: 'total', label: 'Total', start: 0, end: total, point: false, note: null });
  return rows;
}

/** "Model answer: 842 ms, from 0 ms to 842 ms" or "First token at 210 ms". */
export function rowReadout(row: WaterfallRow): string {
  if (row.point) return `${row.label} at ${formatDurationMs(row.start)}`;
  const span = `${row.label}: ${formatDurationMs(row.end - row.start)}, from ${formatDurationMs(row.start)} to ${formatDurationMs(row.end)}`;
  return row.note ? `${span} (${row.note})` : span;
}

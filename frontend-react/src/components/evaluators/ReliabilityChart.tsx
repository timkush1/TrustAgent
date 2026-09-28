/**
 * The reliability chart (SPEC §3.3 Evaluators, custom SVG): for answers grouped by the risk the
 * fast check gave them, the share your labels marked as real problems. Bars on the diagonal mean
 * the risk can be read as a probability. The caption says what it is: "agreement with labels, not
 * ground truth" (DESIGN.md: agreement is never called accuracy).
 *
 * Neutral ink only (it measures agreement, not a verdict); the diagonal is a dashed reference. Every
 * bin is focusable and shows its numbers on hover and focus; the table view has them all.
 */
import { useId, useState } from 'react';
import { ChartFrame } from '@/components/charts/ChartFrame';
import { ChartTable, ChartTooltipBox } from '@/components/charts/ChartParts';
import { seriesColor } from '@/components/charts/palette';
import { formatCount, formatRisk } from '@/domain/format';
import { copy } from '@/domain/presentation';
import { formatPercent } from '@/domain/rates';
import type { ReliabilityResponse } from '@/types/api';

const W = 480;
const H = 260;
const M = { top: 12, right: 12, bottom: 32, left: 40 };
const PW = W - M.left - M.right;
const PH = H - M.top - M.bottom;
const TICKS = [0, 0.25, 0.5, 0.75, 1];

const x = (v: number) => M.left + v * PW;
const y = (v: number) => M.top + (1 - v) * PH;

export function ReliabilityChart({ data, className }: { data: ReliabilityResponse; className?: string }) {
  const summaryId = useId();
  const [active, setActive] = useState<number | null>(null);
  const ink = seriesColor({ tone: 'ink', shade: 'solid' });
  const bins = data.bins.filter((b) => b.n > 0);
  const summary =
    bins.length === 0
      ? 'No labelled answers in this window.'
      : `${formatCount(data.n)} labelled answers in ${bins.length} risk bins${data.ece !== null ? `; expected calibration error ${formatRisk(data.ece)}` : ''}.`;
  const shown = active !== null ? data.bins[active] : null;

  const table = (
    <ChartTable
      caption={`Reliability of ${data.evaluator}: ${copy.evaluators.reliability_caption}`}
      columns={[
        { key: 'label', label: 'Risk given' },
        { key: 'mean', label: 'Average risk', numeric: true },
        { key: 'frac', label: 'Labelled a problem', numeric: true },
        { key: 'n', label: 'Answers', numeric: true },
      ]}
      rows={data.bins.map((b) => ({
        label: `${formatRisk(b.lo)}–${formatRisk(b.hi)}`,
        mean: b.n > 0 ? formatRisk(b.mean_pred) : '—',
        frac: b.n > 0 ? formatPercent(b.frac_pos) : '—',
        n: formatCount(b.n),
      }))}
    />
  );

  return (
    <ChartFrame
      legend={[
        { key: 'bars', label: 'Share labelled a problem', color: ink },
        { key: 'diagonal', label: 'Where a calibrated check would be', color: 'var(--fg-subtle)', mark: 'line' },
      ]}
      title="Reliability"
      description={`${copy.evaluators.reliability_caption.charAt(0).toUpperCase()}${copy.evaluators.reliability_caption.slice(1)}`}
      table={table}
      className={className}
    >
      <p id={summaryId} hidden>
        {summary}
      </p>
      {/* Like the Recharts charts: one focusable surface; arrow keys move the readout between bins. */}
      {/* eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions -- role=application is the chart's keyboard surface, as in the Recharts wrappers */}
      <div
        role="application"
        // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex -- see above: the chart is one tab stop
        tabIndex={0}
        aria-label={`Reliability of ${data.evaluator}. Use the arrow keys to read each bin.`}
        aria-describedby={summaryId}
        onKeyDown={(e) => {
          const filled = data.bins.map((b, i) => (b.n > 0 ? i : -1)).filter((i) => i >= 0);
          if (filled.length === 0) return;
          const at = active === null ? -1 : filled.indexOf(active);
          if (e.key === 'ArrowRight') {
            e.preventDefault();
            setActive(filled[Math.min(filled.length - 1, at + 1)]);
          } else if (e.key === 'ArrowLeft') {
            e.preventDefault();
            setActive(filled[Math.max(0, at === -1 ? 0 : at - 1)]);
          } else if (e.key === 'Escape') {
            setActive(null);
          }
        }}
        onBlur={() => setActive(null)}
        className="relative rounded-md focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-fg"
      >
        {/* Drawn at its own size (never scaled up), so 11px ticks stay 11px. */}
        <svg aria-hidden="true" viewBox={`0 0 ${W} ${H}`} className="h-auto w-full max-w-[30rem] tabular">
          {TICKS.map((t) => (
            <g key={t}>
              <line x1={x(0)} x2={x(1)} y1={y(t)} y2={y(t)} stroke="var(--border)" strokeWidth={1} />
              <text x={M.left - 6} y={y(t)} dy="0.32em" textAnchor="end" fontSize={11} fill="var(--fg-muted)">
                {formatPercent(t)}
              </text>
              <text x={x(t)} y={H - M.bottom + 14} textAnchor="middle" fontSize={11} fill="var(--fg-muted)">
                {formatRisk(t)}
              </text>
            </g>
          ))}
          <text x={M.left + PW / 2} y={H - 4} textAnchor="middle" fontSize={11} fill="var(--fg-muted)">
            Risk the fast check gave
          </text>
          <line x1={x(0)} y1={y(0)} x2={x(1)} y2={y(1)} stroke="var(--fg-subtle)" strokeWidth={1} strokeDasharray="4 4" />
          {data.bins.map((b, i) => {
            if (b.n === 0) return null;
            // Bars stay at most 24px thick (DESIGN.md), centred on their bin.
            const bw = Math.max(2, Math.min(24, x(b.hi) - x(b.lo) - 4));
            const bx = (x(b.lo) + x(b.hi)) / 2 - bw / 2;
            const top = y(b.frac_pos);
            const h = Math.max(1, y(0) - top);
            return (
              <rect
                key={i}
                x={bx}
                y={y(0) - h}
                width={bw}
                height={h}
                rx={Math.min(4, h / 2, bw / 2)}
                fill={ink}
                stroke={active === i ? 'var(--fg)' : 'none'}
                strokeWidth={active === i ? 2 : 0}
                data-bin={i}
                onMouseEnter={() => setActive(i)}
                onMouseLeave={() => setActive(null)}
              />
            );
          })}
        </svg>
        <p aria-live="polite" className="sr-only">
          {shown
            ? `Risk ${formatRisk(shown.lo)} to ${formatRisk(shown.hi)}: ${formatPercent(shown.frac_pos)} labelled a problem, ${formatCount(shown.n)} answers.`
            : ''}
        </p>
        {shown ? (
          <div aria-hidden="true" className="pointer-events-none absolute top-2 right-2">
            <ChartTooltipBox
              title={`Risk ${formatRisk(shown.lo)}–${formatRisk(shown.hi)}`}
              rows={[
                { key: 'frac', label: 'labelled a problem', value: formatPercent(shown.frac_pos), color: ink },
                { key: 'mean', label: 'average risk', value: formatRisk(shown.mean_pred), color: 'var(--fg-subtle)' },
              ]}
              footer={`${formatCount(shown.n)} answers`}
            />
          </div>
        ) : null}
      </div>
    </ChartFrame>
  );
}

/**
 * The timing waterfall (SPEC §3.3 "Trace detail" 3; custom SVG, DESIGN.md §12): one row per step
 * on a shared millisecond axis. Latency carries no verdict, so the bars are neutral ink (the total
 * in the light shade: it spans the others); each row is named on the left and its duration written
 * at the bar's tip. The chart is one focusable surface whose arrow keys move the readout between
 * rows, hover shows the same readout, and the table view has every number.
 */
import { useId, useState } from 'react';
import { ChartFrame } from '@/components/charts/ChartFrame';
import { ChartTable, ChartTooltipBox } from '@/components/charts/ChartParts';
import { seriesColor } from '@/components/charts/palette';
import { niceTicks } from '@/components/charts/scale';
import { formatDurationMs } from '@/domain/format';
import { copy } from '@/domain/presentation';
import { rowReadout, type WaterfallRow } from './waterfall';

const W = 560;
const LABEL_W = 116;
const VALUE_W = 60;
const ROW_H = 26;
const BAR_H = 12;
const AXIS_H = 22;
const TOP = 4;

/** A bar with a square baseline and a 4px rounded data end (DESIGN.md §12). */
function barPath(x: number, y: number, w: number, h: number): string {
  const r = Math.min(4, h / 2, w);
  return `M${x},${y}h${w - r}a${r},${r} 0 0 1 ${r},${r}v${h - 2 * r}a${r},${r} 0 0 1 ${-r},${r}h${-(w - r)}z`;
}

export function TimingWaterfall({ rows, className }: { rows: readonly WaterfallRow[]; className?: string }) {
  const summaryId = useId();
  const [active, setActive] = useState<number | null>(null);
  const ticks = niceTicks(Math.max(1, ...rows.map((r) => r.end)));
  const domain = ticks[ticks.length - 1];
  const plotW = W - LABEL_W - VALUE_W;
  const x = (ms: number) => LABEL_W + (ms / domain) * plotW;
  const H = TOP + rows.length * ROW_H + AXIS_H;
  const ink = seriesColor({ tone: 'ink', shade: 'solid' });
  const light = seriesColor({ tone: 'ink', shade: 'light' });
  const shown = active !== null ? rows[active] : null;

  const table = (
    <ChartTable
      caption={`${copy.trace.timing_title}, in milliseconds from the request reaching the proxy`}
      columns={[
        { key: 'step', label: 'Step' },
        { key: 'start', label: 'Starts at', numeric: true },
        { key: 'takes', label: 'Takes', numeric: true },
        { key: 'end', label: 'Ends at', numeric: true },
      ]}
      rows={rows.map((r) => ({
        step: r.note ? `${r.label} (${r.note})` : r.label,
        start: formatDurationMs(r.start),
        takes: r.point ? '—' : formatDurationMs(r.end - r.start),
        end: formatDurationMs(r.end),
      }))}
    />
  );

  return (
    <ChartFrame title={copy.trace.timing_title} description={copy.trace.timing_description} table={table} className={className}>
      <p id={summaryId} hidden>
        {rows.map(rowReadout).join('. ')}.
      </p>
      {/* eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions -- role=application is the chart's keyboard surface, as in the other charts */}
      <div
        role="application"
        // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex -- see above: the chart is one tab stop
        tabIndex={0}
        aria-label={`${copy.trace.timing_title}. Use the arrow keys to read each step.`}
        aria-describedby={summaryId}
        onKeyDown={(e) => {
          if (rows.length === 0) return;
          if (e.key === 'ArrowDown' || e.key === 'ArrowRight') {
            e.preventDefault();
            setActive((a) => (a === null ? 0 : Math.min(rows.length - 1, a + 1)));
          } else if (e.key === 'ArrowUp' || e.key === 'ArrowLeft') {
            e.preventDefault();
            setActive((a) => (a === null ? 0 : Math.max(0, a - 1)));
          } else if (e.key === 'Escape') {
            setActive(null);
          }
        }}
        onBlur={() => setActive(null)}
        className="relative rounded-md focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-fg"
      >
        {/* Drawn at its own size (never scaled up), so 11px text stays 11px. */}
        <svg aria-hidden="true" viewBox={`0 0 ${W} ${H}`} className="h-auto w-full max-w-[35rem] tabular" data-testid="timing-waterfall">
          {ticks.map((t) => (
            <g key={t}>
              <line x1={x(t)} x2={x(t)} y1={TOP} y2={H - AXIS_H + 2} stroke="var(--border)" strokeWidth={1} />
              <text x={x(t)} y={H - 6} textAnchor="middle" fontSize={11} fill="var(--fg-muted)">
                {formatDurationMs(t)}
              </text>
            </g>
          ))}
          {rows.map((r, i) => {
            const mid = TOP + i * ROW_H + ROW_H / 2;
            const x0 = x(r.start);
            const w = Math.max(1, x(r.end) - x0);
            const selected = active === i;
            return (
              <g key={r.key} data-row={r.key} onMouseEnter={() => setActive(i)} onMouseLeave={() => setActive(null)}>
                <rect x={0} y={mid - ROW_H / 2} width={W} height={ROW_H} fill="transparent" />
                <text x={LABEL_W - 8} y={mid} dy="0.32em" textAnchor="end" fontSize={11} fill="var(--fg-muted)">
                  {r.label}
                </text>
                {r.point ? (
                  <circle cx={x0} cy={mid} r={4} fill={ink} stroke="var(--surface)" strokeWidth={2} />
                ) : (
                  <path
                    d={barPath(x0, mid - BAR_H / 2, w, BAR_H)}
                    fill={r.key === 'total' ? light : ink}
                    stroke={selected ? 'var(--fg)' : 'none'}
                    strokeWidth={selected ? 2 : 0}
                  />
                )}
                <text x={(r.point ? x0 + 4 : x0 + w) + 6} y={mid} dy="0.32em" fontSize={11} fill="var(--fg)">
                  {formatDurationMs(r.point ? r.start : r.end - r.start)}
                </text>
              </g>
            );
          })}
        </svg>
        <p aria-live="polite" className="sr-only">
          {shown ? rowReadout(shown) : ''}
        </p>
        {shown ? (
          <div aria-hidden="true" className="pointer-events-none absolute top-0 right-0">
            <ChartTooltipBox
              title={shown.label}
              rows={[
                shown.point
                  ? { key: 'at', label: 'after the request arrived', value: formatDurationMs(shown.start), color: ink }
                  : { key: 'takes', label: shown.note ?? 'duration', value: formatDurationMs(shown.end - shown.start), color: ink },
              ]}
              footer={shown.point ? undefined : `${formatDurationMs(shown.start)} → ${formatDurationMs(shown.end)}`}
            />
          </div>
        ) : null}
      </div>
    </ChartFrame>
  );
}

/**
 * What checking cost per day (or hour), over what an LLM would have cost for the same traffic
 * (SPEC §7.4 "measured cost over the counterfactual bands"; P4-PLAN D73). One axis, in USD per
 * bucket: the measured spend is a 2px line, the counterfactual a neutral band from its low to its
 * high estimate. Colour carries no verdict here (DESIGN.md "Charts"): both series are the neutral
 * inks, told apart by mark (line vs area) and named in the legend.
 *
 * Custom SVG like the other feature charts (DESIGN.md §12): the geometry is drawn in a 100×100
 * viewBox stretched to the plot (strokes keep their width), and every word is HTML so it stays
 * crisp at any width. Hover and focus show the same readout; arrow keys move it; the table view
 * has every number, and a sentence describes the chart.
 */
import { useId, useMemo, useState, type KeyboardEvent, type PointerEvent } from 'react';
import { ChartFrame } from '@/components/charts/ChartFrame';
import { ChartTable, ChartTooltipBox, type LegendItem } from '@/components/charts/ChartParts';
import { seriesColor } from '@/components/charts/palette';
import { niceTicks } from '@/components/charts/scale';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { formatCount, formatUsd } from '@/domain/format';
import { valueLabelView } from '@/domain/controlPresentation';
import { costCounterfactualView, costRange } from '@/domain/presentation';
import { cn } from '@/lib/cn';
import type { RoiTimeseries, ValueLabel } from '@/types/api';
import { availableBands, chartMax, type BandKind, type CostBandPoint } from './savingsModel';

export interface CostBandChartProps {
  points: readonly CostBandPoint[];
  bucket: RoiTimeseries['bucket'];
  /** Value label of the measured spend (Measured, or Simulated on a mock stack). */
  measuredLabel: ValueLabel;
  /** Value label of the counterfactual bands (Estimate, or Simulated). */
  bandLabel: ValueLabel;
  headingLevel?: 2 | 3 | 4;
  /** Plot height in px. */
  height?: number;
  className?: string;
}

const MEASURED_COLOR = seriesColor({ tone: 'ink', shade: 'solid' });
const BAND_COLOR = seriesColor({ tone: 'ink', shade: 'light' });
/** Horizontal room (in % of the plot) so the first and last points are not on the edge. */
const PAD_X = 2;
const MAX_X_LABELS = 6;

const BAND_OPTION: Record<BandKind, string> = {
  same_checks: 'Same questions',
  full_audit: 'Full audit',
};

const dayFormat = new Intl.DateTimeFormat('en-GB', { weekday: 'short', day: 'numeric', month: 'short', timeZone: 'UTC' });
const hourFormat = new Intl.DateTimeFormat('en-GB', { weekday: 'short', hour: '2-digit', minute: '2-digit' });

/** A bucket's name: the UTC day ("Mon 22 Sep") or the local hour ("Mon 14:00"). */
export function bucketLabel(bucket: RoiTimeseries['bucket'], iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  return (bucket === '1d' ? dayFormat : hourFormat).format(t);
}

function bandWords(kind: BandKind): string {
  return costCounterfactualView(kind === 'same_checks' ? 'llm_same_checks' : 'llm_full_claim_audit').label;
}

/** The chart in one sentence, for screen readers and the figure description. */
export function costBandSummary(points: readonly CostBandPoint[], band: BandKind | null, bandLabel: ValueLabel): string {
  const spent = points.reduce((s, p) => s + p.measured, 0);
  const answers = points.reduce((s, p) => s + p.gated, 0);
  let line = `Checking ${formatCount(answers)} answers cost ${formatUsd(spent)} over ${points.length} intervals.`;
  if (band) {
    const present = points.flatMap((p) => (p[band] ? [p[band]] : []));
    if (present.length > 0) {
      const low = present.reduce((s, b) => s + b.low, 0);
      const high = present.reduce((s, b) => s + b.high, 0);
      line += ` ${bandWords(band)} would have cost ${costRange(low, high)} (${valueLabelView(bandLabel).label.toLowerCase()}).`;
    }
  }
  return line;
}

/** Positions of the points across the plot, in %. */
function xAt(i: number, n: number): number {
  return n <= 1 ? 50 : PAD_X + (i / (n - 1)) * (100 - 2 * PAD_X);
}

/** Which buckets get an axis label: at most MAX_X_LABELS, evenly spread, always the first and last. */
function labelIndexes(n: number): number[] {
  if (n <= MAX_X_LABELS) return Array.from({ length: n }, (_, i) => i);
  const step = (n - 1) / (MAX_X_LABELS - 1);
  return Array.from({ length: MAX_X_LABELS }, (_, i) => Math.round(i * step));
}

/** The band as closed areas, one per run of buckets that have it. */
function bandPaths(points: readonly CostBandPoint[], band: BandKind, y: (v: number) => number): { area: string[]; ticks: string[] } {
  const area: string[] = [];
  const ticks: string[] = [];
  const n = points.length;
  let run: number[] = [];
  const flush = () => {
    if (run.length === 1) {
      const i = run[0];
      const b = points[i][band]!;
      ticks.push(`M${xAt(i, n)},${y(b.high)}L${xAt(i, n)},${y(b.low)}`);
    } else if (run.length > 1) {
      const top = run.map((i, k) => `${k === 0 ? 'M' : 'L'}${xAt(i, n)},${y(points[i][band]!.high)}`).join('');
      const bottom = [...run].reverse().map((i) => `L${xAt(i, n)},${y(points[i][band]!.low)}`).join('');
      area.push(`${top}${bottom}Z`);
    }
    run = [];
  };
  points.forEach((p, i) => {
    if (p[band]) run.push(i);
    else flush();
  });
  flush();
  return { area, ticks };
}

export function CostBandChart({
  points,
  bucket,
  measuredLabel,
  bandLabel,
  headingLevel = 3,
  height = 220,
  className,
}: CostBandChartProps) {
  const summaryId = useId();
  const bands = useMemo(() => availableBands(points), [points]);
  const [chosen, setChosen] = useState<BandKind>('same_checks');
  const band: BandKind | null = bands.includes(chosen) ? chosen : (bands[0] ?? null);
  const [active, setActive] = useState<number | null>(null);

  const n = points.length;
  const ticks = niceTicks(chartMax(points, band), 4);
  const top = ticks[ticks.length - 1] || 1;
  const y = (v: number) => 100 - (Math.max(0, v) / top) * 100;
  const measuredPath = points.map((p, i) => `${i === 0 ? 'M' : 'L'}${xAt(i, n)},${y(p.measured)}`).join('');
  const bandGeometry = band ? bandPaths(points, band, y) : { area: [], ticks: [] };
  const summary = costBandSummary(points, band, bandLabel);

  const measuredName = `What checking cost you (${valueLabelView(measuredLabel).label})`;
  const bandName = band ? `${bandWords(band)} (${valueLabelView(bandLabel).label}, range)` : null;
  const legend: LegendItem[] = [
    { key: 'measured', label: measuredName, color: MEASURED_COLOR, mark: 'line' },
    ...(bandName ? [{ key: 'band', label: bandName, color: BAND_COLOR, mark: 'square' as const }] : []),
  ];

  const table = (
    <ChartTable
      caption={`Spend per ${bucket === '1d' ? 'day' : 'hour'}, with what an LLM would have cost`}
      columns={[
        { key: 'label', label: bucket === '1d' ? 'Day (UTC)' : 'Hour' },
        { key: 'gated', label: 'Answers checked', numeric: true },
        { key: 'measured', label: `Your cost (${valueLabelView(measuredLabel).label})`, numeric: true },
        ...bands.map((k) => ({ key: k, label: `${BAND_OPTION[k]}, LLM (${valueLabelView(bandLabel).label})`, numeric: true })),
      ]}
      rows={points.map((p) => ({
        label: bucketLabel(bucket, p.t),
        gated: formatCount(p.gated),
        measured: formatUsd(p.measured),
        ...Object.fromEntries(bands.map((k) => [k, p[k] ? costRange(p[k].low, p[k].high) : '—'])),
      }))}
    />
  );

  const move = (e: KeyboardEvent<HTMLDivElement>) => {
    if (n === 0) return;
    const from = active ?? n - 1;
    const next =
      e.key === 'ArrowLeft' ? Math.max(0, from - 1)
      : e.key === 'ArrowRight' ? Math.min(n - 1, from + 1)
      : e.key === 'Home' ? 0
      : e.key === 'End' ? n - 1
      : null;
    if (e.key === 'Escape') {
      setActive(null);
      return;
    }
    if (next === null) return;
    e.preventDefault();
    setActive(next);
  };

  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    if (rect.width === 0 || n === 0) return;
    const pct = ((e.clientX - rect.left) / rect.width) * 100;
    const i = n <= 1 ? 0 : Math.round(((pct - PAD_X) / (100 - 2 * PAD_X)) * (n - 1));
    setActive(Math.max(0, Math.min(n - 1, i)));
  };

  const point = active !== null ? points[active] : null;
  const activeBand = point && band ? point[band] : null;
  const readout = point ? (
    <ChartTooltipBox
      title={bucketLabel(bucket, point.t)}
      rows={[
        { key: 'measured', label: 'your cost', value: formatUsd(point.measured), color: MEASURED_COLOR },
        ...(band ? [{ key: 'band', label: BAND_OPTION[band].toLowerCase() + ', LLM', value: activeBand ? costRange(activeBand.low, activeBand.high) : '—', color: BAND_COLOR, muted: !activeBand }] : []),
      ]}
      footer={`${formatCount(point.gated)} answers checked`}
    />
  ) : null;
  const readoutText = point
    ? `${bucketLabel(bucket, point.t)}: your cost ${formatUsd(point.measured)}${band ? `, ${BAND_OPTION[band].toLowerCase()} LLM ${activeBand ? costRange(activeBand.low, activeBand.high) : 'not estimated'}` : ''}, ${formatCount(point.gated)} answers checked.`
    : '';

  return (
    <ChartFrame
      title="What checking cost, against asking an LLM"
      description={`USD per ${bucket === '1d' ? 'UTC day' : 'hour'}. ${summary}`}
      headingLevel={headingLevel}
      legend={legend}
      table={table}
      className={className}
      actions={
        bands.length > 1 && band ? (
          <SegmentedControl<BandKind>
            aria-label="Compare with"
            size="sm"
            options={bands.map((k) => ({ value: k, label: BAND_OPTION[k] }))}
            value={band}
            onValueChange={setChosen}
          />
        ) : null
      }
    >
      {n < 2 ? (
        <p className="rounded-md border border-dashed border-border px-3 py-6 text-center text-sm text-fg-muted">
          Only {n === 1 ? 'one' : 'no'} {bucket === '1d' ? 'day' : 'hour'} in this window so far: switch to Table, or pick a longer window to see a trend.
        </p>
      ) : (
        <div className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-2">
          {/* Y axis: tick values beside the plot, in text tokens. */}
          <div aria-hidden="true" className="relative w-12" style={{ height }}>
            {ticks.map((t) => (
              <span key={t} className="absolute right-0 -translate-y-1/2 text-2xs text-fg-muted tabular" style={{ top: `${y(t)}%` }}>
                {formatUsd(t)}
              </span>
            ))}
          </div>
          <div
            // The plot is one tab stop; arrow keys move the readout across the buckets.
            // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
            tabIndex={0}
            role="group"
            aria-roledescription="chart"
            aria-label="What checking cost, against asking an LLM. Use the arrow keys to read each interval."
            aria-describedby={summaryId}
            onKeyDown={move}
            onBlur={() => setActive(null)}
            onPointerMove={onPointerMove}
            onPointerLeave={() => setActive(null)}
            className="relative rounded-sm focus-visible:outline-2 focus-visible:outline-offset-2"
            style={{ height }}
          >
            <span id={summaryId} className="sr-only">
              {summary}
            </span>
            <span aria-live="polite" className="sr-only">
              {readoutText}
            </span>
            <svg aria-hidden="true" viewBox="0 0 100 100" preserveAspectRatio="none" className="absolute inset-0 size-full overflow-visible">
              {ticks.map((t) => (
                <line key={t} x1={0} x2={100} y1={y(t)} y2={y(t)} stroke="var(--border)" strokeWidth={1} vectorEffect="non-scaling-stroke" />
              ))}
              {bandGeometry.area.map((d, i) => (
                <path key={i} data-band={band ?? undefined} d={d} fill={BAND_COLOR} fillOpacity={0.45} stroke={BAND_COLOR} strokeWidth={1} vectorEffect="non-scaling-stroke" />
              ))}
              {bandGeometry.ticks.map((d, i) => (
                <path key={i} d={d} stroke={BAND_COLOR} strokeWidth={6} strokeLinecap="round" vectorEffect="non-scaling-stroke" />
              ))}
              <path
                data-series="measured"
                d={measuredPath}
                fill="none"
                stroke={MEASURED_COLOR}
                strokeWidth={2}
                strokeLinejoin="round"
                strokeLinecap="round"
                vectorEffect="non-scaling-stroke"
              />
            </svg>
            {point && active !== null ? (
              <>
                <span aria-hidden="true" className="absolute inset-y-0 w-px bg-border-strong" style={{ left: `${xAt(active, n)}%` }} />
                <span
                  aria-hidden="true"
                  className="absolute size-2 -translate-x-1/2 -translate-y-1/2 rounded-full ring-2 ring-surface"
                  style={{ left: `${xAt(active, n)}%`, top: `${y(point.measured)}%`, backgroundColor: MEASURED_COLOR }}
                />
                <div
                  aria-hidden="true"
                  className={cn('pointer-events-none absolute top-0 z-10', xAt(active, n) > 60 ? '-translate-x-full -ml-2' : 'ml-2')}
                  style={{ left: `${xAt(active, n)}%` }}
                >
                  {readout}
                </div>
              </>
            ) : (
              <span
                aria-hidden="true"
                className="absolute size-2 -translate-x-1/2 -translate-y-1/2 rounded-full ring-2 ring-surface"
                style={{ left: `${xAt(n - 1, n)}%`, top: `${y(points[n - 1].measured)}%`, backgroundColor: MEASURED_COLOR }}
              />
            )}
          </div>
          {/* X axis: a few bucket names under the plot. */}
          <span aria-hidden="true" />
          <div aria-hidden="true" className="relative mt-1 h-4">
            {labelIndexes(n).map((i, k, all) => (
              <span
                key={i}
                className={cn(
                  'absolute text-2xs whitespace-nowrap text-fg-muted',
                  k === 0 ? 'translate-x-0' : k === all.length - 1 ? '-translate-x-full' : '-translate-x-1/2',
                )}
                style={{ left: `${xAt(i, n)}%` }}
              >
                {bucketLabel(bucket, points[i].t)}
              </span>
            ))}
          </div>
        </div>
      )}
    </ChartFrame>
  );
}

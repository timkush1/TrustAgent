/**
 * What checking cost per day, hour or (in the static replay) minute, over what an LLM would have
 * cost for the same traffic
 * (SPEC §7.4 "measured cost over the counterfactual bands"; D73). One axis, in USD per
 * bucket: the measured spend is a solid 2px line, the counterfactual a neutral band from its low to
 * its high estimate, or a dashed 2px line when it is a single estimate. Colour carries no verdict
 * here (DESIGN.md "Charts"): both series are the neutral inks, told apart by mark (solid line vs
 * area or dashed line), named in the legend and labelled directly at the end of each line.
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
import { costRange } from '@/domain/presentation';
import { cn } from '@/lib/cn';
import type { CostBucket } from '@/data/control/costPlane';
import type { ValueLabel } from '@/types/api';
import {
  availableBands,
  bandHasRange,
  bandWords,
  bucketLabel,
  bucketUnit,
  chartBuckets,
  chartMax,
  chartSpan,
  costBandSummary,
  countOf,
  endLabels,
  type BandKind,
  type ChartTotals,
  type CostBandPoint,
} from './savingsModel';

export interface CostBandChartProps {
  points: readonly CostBandPoint[];
  bucket: CostBucket;
  /**
   * The totals the description states: the ROI summary's, as in the headline and the component
   * table (chartTotals), never a sum of the points, which another request fetched.
   */
  totals: ChartTotals;
  /** Value label of the measured spend (Measured, or Simulated on a mock stack). */
  measuredLabel: ValueLabel;
  /** Value label of each comparison, as its row in the component table says (bandLabels). */
  bandLabels: Record<BandKind, ValueLabel>;
  /** The span in words for the description: "in the last 24 hours", "over the replayed traffic". */
  spanWords: string;
  /** Whether the page offers a time window to pick (the static replay has none). */
  windowPicker: boolean;
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

const BUCKET_DESCRIPTION: Record<CostBucket, string> = { '1d': 'Daily (UTC)', '1h': 'Hourly', '1m': 'Per minute' };

const BAND_OPTION: Record<BandKind, string> = {
  same_checks: 'Same questions',
  full_audit: 'Full audit',
};

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

/** Half the width of a lone bucket's marker, in % of the plot. */
const CAP = 1.2;
/** The counterfactual line's dashes, in screen px (the strokes do not scale). */
const BAND_DASH = '5 4';

/**
 * The band, one shape per run of buckets that have it: a closed area from low to high when the
 * band has a range, else (a single estimate per bucket) a line. A lone bucket gets a marker that
 * stays visible: a range bar with caps at its low and high, or a short dash at the estimate.
 */
function bandPaths(
  points: readonly CostBandPoint[],
  band: BandKind,
  y: (v: number) => number,
  ranged: boolean,
): { area: string[]; lines: string[] } {
  const area: string[] = [];
  const lines: string[] = [];
  const n = points.length;
  let run: number[] = [];
  const flush = () => {
    if (run.length === 1) {
      const x = xAt(run[0], n);
      const b = points[run[0]][band]!;
      const [hi, lo] = [y(b.high), y(b.low)];
      lines.push(ranged ? `M${x},${hi}L${x},${lo}M${x - CAP},${hi}L${x + CAP},${hi}M${x - CAP},${lo}L${x + CAP},${lo}` : `M${x - CAP},${hi}L${x + CAP},${hi}`);
    } else if (run.length > 1) {
      const top = run.map((i, k) => `${k === 0 ? 'M' : 'L'}${xAt(i, n)},${y(points[i][band]!.high)}`).join('');
      if (ranged) {
        const bottom = [...run].reverse().map((i) => `L${xAt(i, n)},${y(points[i][band]!.low)}`).join('');
        area.push(`${top}${bottom}Z`);
      } else {
        lines.push(top);
      }
    }
    run = [];
  };
  points.forEach((p, i) => {
    if (p[band]) run.push(i);
    else flush();
  });
  flush();
  return { area, lines };
}

/**
 * What the chart says instead of a line with fewer than two buckets. It points at a longer window
 * only where the page has a window picker.
 */
function sparseMessage(n: number, unit: 'day' | 'hour' | 'minute', windowPicker: boolean): string {
  if (windowPicker) {
    return `${n === 1 ? `Only one ${unit}` : `No ${unit}s`} in this window so far: switch to Table, or pick a longer window to see a trend.`;
  }
  return n === 1
    ? `Only one ${unit} of traffic so far: the trend draws from the second ${unit}. Switch to Table to see this one.`
    : `No ${unit}s of traffic so far.`;
}

export function CostBandChart({
  points: allPoints,
  bucket,
  totals,
  measuredLabel,
  bandLabels,
  spanWords,
  windowPicker,
  headingLevel = 3,
  height = 220,
  className,
}: CostBandChartProps) {
  const summaryId = useId();
  // A fresh stack's window is mostly empty buckets: start at its first traffic.
  const { points, since } = useMemo(() => chartBuckets(allPoints), [allPoints]);
  const bands = useMemo(() => availableBands(points), [points]);
  const [chosen, setChosen] = useState<BandKind>('same_checks');
  const band: BandKind | null = bands.includes(chosen) ? chosen : (bands[0] ?? null);
  const [active, setActive] = useState<number | null>(null);

  const n = points.length;
  const ticks = niceTicks(chartMax(points, band), 4);
  const top = ticks[ticks.length - 1] || 1;
  const y = (v: number) => 100 - (Math.max(0, v) / top) * 100;
  const measuredPath = points.map((p, i) => `${i === 0 ? 'M' : 'L'}${xAt(i, n)},${y(p.measured)}`).join('');
  const ranged = band ? bandHasRange(points, band) : false;
  const bandGeometry = band ? bandPaths(points, band, y, ranged) : { area: [], lines: [] };
  const unit = bucketUnit(bucket);
  const bandLabel: ValueLabel = band ? bandLabels[band] : 'estimate';
  const span = chartSpan(bucket, since, spanWords);
  const summary = costBandSummary(totals, band, bandLabel, span);
  const ends = endLabels(points, band, ranged, y);

  const measuredName = `What checking cost you (${valueLabelView(measuredLabel).label})`;
  const bandName = band ? `${bandWords(band)} (${valueLabelView(bandLabel).label}${ranged ? ', range' : ''})` : null;
  const legend: LegendItem[] = [
    { key: 'measured', label: measuredName, color: MEASURED_COLOR, mark: 'line' },
    ...(bandName ? [{ key: 'band', label: bandName, color: BAND_COLOR, mark: ranged ? ('square' as const) : ('dash' as const) }] : []),
  ];

  const table = (
    <ChartTable
      caption={`Spend per ${unit}, with what an LLM would have cost`}
      columns={[
        { key: 'label', label: unit === 'day' ? 'Day (UTC)' : unit === 'minute' ? 'Minute' : 'Hour' },
        { key: 'gated', label: 'Answers checked', numeric: true },
        { key: 'measured', label: `Your cost (${valueLabelView(measuredLabel).label})`, numeric: true },
        ...bands.map((k) => ({ key: k, label: `${BAND_OPTION[k]}, LLM (${valueLabelView(bandLabels[k]).label})`, numeric: true })),
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
      footer={`${countOf(point.gated, 'answer')} checked`}
    />
  ) : null;
  /** The readout of bucket `i` in words (the slider's value text). */
  const readoutText = (i: number): string => {
    const p = points[i];
    if (!p) return '';
    const b = band ? p[band] : null;
    const bandPart = band ? `, ${BAND_OPTION[band].toLowerCase()} LLM ${b ? costRange(b.low, b.high) : 'not estimated'}` : '';
    return `${bucketLabel(bucket, p.t)}: your cost ${formatUsd(p.measured)}${bandPart}, ${countOf(p.gated, 'answer')} checked.`;
  };
  const current = active ?? Math.max(0, n - 1);

  return (
    <ChartFrame
      title="What checking cost, against asking an LLM"
      description={`${BUCKET_DESCRIPTION[bucket]}, in USD. ${summary}`}
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
          {sparseMessage(n, unit, windowPicker)}
        </p>
      ) : (
        <div className="grid grid-cols-[auto_minmax(0,1fr)_auto] gap-x-2">
          {/* Y axis: tick values beside the plot, in text tokens. */}
          <div aria-hidden="true" className="relative w-12" style={{ height }}>
            {ticks.map((t) => (
              <span key={t} className="absolute right-0 -translate-y-1/2 text-2xs text-fg-muted tabular" style={{ top: `${y(t)}%` }}>
                {formatUsd(t)}
              </span>
            ))}
          </div>
          <div
            // The plot reads like a slider over the intervals: one tab stop, arrow keys move the
            // readout, and the value text says what the readout shows.
            tabIndex={0}
            role="slider"
            aria-label={`What checking cost per ${unit}`}
            aria-describedby={summaryId}
            aria-valuemin={0}
            aria-valuemax={n - 1}
            aria-valuenow={current}
            aria-valuetext={readoutText(current)}
            onKeyDown={move}
            onFocus={() => setActive((a) => a ?? n - 1)}
            onBlur={() => setActive(null)}
            onPointerMove={onPointerMove}
            onPointerLeave={() => setActive(null)}
            className="relative rounded-sm focus-visible:outline-2 focus-visible:outline-offset-2"
            style={{ height }}
          >
            <span id={summaryId} className="sr-only">
              {summary}
            </span>
            <svg aria-hidden="true" viewBox="0 0 100 100" preserveAspectRatio="none" className="absolute inset-0 size-full overflow-visible">
              {ticks.map((t) => (
                <line key={t} x1={0} x2={100} y1={y(t)} y2={y(t)} stroke="var(--border)" strokeWidth={1} vectorEffect="non-scaling-stroke" />
              ))}
              {bandGeometry.area.map((d, i) => (
                <path key={i} data-band={band ?? undefined} d={d} fill={BAND_COLOR} fillOpacity={0.45} stroke={BAND_COLOR} strokeWidth={1} vectorEffect="non-scaling-stroke" />
              ))}
              {bandGeometry.lines.map((d, i) => (
                <path
                  key={i}
                  data-band={band ?? undefined}
                  d={d}
                  fill="none"
                  stroke={BAND_COLOR}
                  strokeWidth={2}
                  // A single estimate is a dashed line; a lone ranged bucket's bar and caps stay solid.
                  strokeDasharray={ranged ? undefined : BAND_DASH}
                  strokeLinejoin="round"
                  strokeLinecap={ranged ? 'round' : 'butt'}
                  vectorEffect="non-scaling-stroke"
                />
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
          {/* Direct labels at the end of each line. The hidden copies give the column its width. */}
          <div aria-hidden="true" data-end-labels="" className="relative text-2xs whitespace-nowrap tabular" style={{ height }}>
            <div className="invisible h-0">
              {ends.map((l) => (
                <div key={l.key} className={l.key === 'measured' ? 'font-medium' : undefined}>
                  {l.text}
                </div>
              ))}
            </div>
            {ends.map((l) => (
              <span
                key={l.key}
                data-end-label={l.key}
                className={cn('absolute left-0 -translate-y-1/2', l.key === 'measured' ? 'font-medium text-fg' : 'text-fg-muted')}
                style={{ top: `${l.top}%` }}
              >
                {l.text}
              </span>
            ))}
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
          <span aria-hidden="true" />
        </div>
      )}
    </ChartFrame>
  );
}

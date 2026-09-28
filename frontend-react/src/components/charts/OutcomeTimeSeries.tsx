/**
 * "Requests by outcome": requests per time bucket as stacked columns, one segment per outcome, on
 * a single count axis. Colors are the outcome tones (palette.ts), segments are separated by a 2px
 * surface gap and only the top of each column is rounded (4px), columns are at most 24px wide.
 * Hover or keyboard focus (arrow keys on the focused chart) shows every outcome in that bucket;
 * the legend names the series and the table view lists every number.
 */
import { useId, useMemo } from 'react';
import {
  Bar,
  BarChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
  type BarShapeProps,
  type TooltipContentProps,
} from 'recharts';
import { formatCount } from '@/domain/format';
import { outcomeView, type OutcomeId } from '@/domain/presentation';
import { ChartFrame } from './ChartFrame';
import { ChartTable, ChartTooltipBox } from './ChartParts';
import { OUTCOME_STACK_ORDER, outcomeSeriesStyle, seriesColor } from './palette';
import { axisTick, gridStroke, stackedSegmentPath } from './rechartsTheme';

export interface OutcomeBucket {
  /** Bucket start (ISO 8601). */
  t: string;
  counts: Partial<Record<OutcomeId, number>>;
}

export interface OutcomeTimeSeriesProps {
  title: string;
  description?: string;
  buckets: readonly OutcomeBucket[];
  /** Outcomes to stack; default: the ones that occur, in stacking order. */
  outcomes?: readonly OutcomeId[];
  /** Label of a bucket on the axis, in the tooltip and in the table. */
  formatBucket?: (iso: string) => string;
  height?: number;
  /** Fixed width in px (tests, print); by default the chart fills its container. */
  width?: number;
  headingLevel?: 2 | 3 | 4;
  className?: string;
}

const clock = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit' });

function defaultFormatBucket(iso: string): string {
  const t = Date.parse(iso);
  return Number.isNaN(t) ? iso : clock.format(t);
}

type Datum = { t: string; label: string; total: number } & Partial<Record<OutcomeId, number>>;

export function OutcomeTimeSeries({
  title,
  description,
  buckets,
  outcomes,
  formatBucket = defaultFormatBucket,
  height = 240,
  width,
  headingLevel,
  className,
}: OutcomeTimeSeriesProps) {
  const summaryId = useId();
  const series = useMemo(() => {
    const wanted = outcomes ?? OUTCOME_STACK_ORDER.filter((id) => buckets.some((b) => (b.counts[id] ?? 0) > 0));
    // Stack in the fixed order whatever order the caller gave: a color follows its outcome.
    return OUTCOME_STACK_ORDER.filter((id) => wanted.includes(id)).map((id) => ({
      id,
      label: outcomeView(id).label,
      color: seriesColor(outcomeSeriesStyle(id)),
    }));
  }, [buckets, outcomes]);

  const data: Datum[] = useMemo(
    () =>
      buckets.map((b) => {
        const row: Datum = { t: b.t, label: formatBucket(b.t), total: 0 };
        for (const s of series) {
          const v = Math.max(0, b.counts[s.id] ?? 0);
          row[s.id] = v;
          row.total += v;
        }
        return row;
      }),
    [buckets, series, formatBucket],
  );

  const ids = series.map((s) => s.id);
  const grandTotal = data.reduce((sum, d) => sum + d.total, 0);
  const peak = data.reduce<Datum | null>((best, d) => (best === null || d.total > best.total ? d : best), null);
  const summary =
    data.length === 0
      ? 'No requests in this window.'
      : `${formatCount(grandTotal)} requests in ${data.length} intervals; the busiest, ${peak?.label}, had ${formatCount(peak?.total ?? 0)}.`;

  const renderTooltip = ({ active, payload, label }: TooltipContentProps) => {
    if (!active || !payload || payload.length === 0) return null;
    const datum = payload[0]?.payload as Datum | undefined;
    if (!datum) return null;
    const rows = [...series].reverse().map((s) => {
      const v = datum[s.id] ?? 0;
      return { key: s.id, label: s.label, value: formatCount(v), color: s.color, muted: v === 0 };
    });
    return <ChartTooltipBox title={label} rows={rows} footer={`${formatCount(datum.total)} total`} />;
  };

  const table = (
    <ChartTable
      caption={`${title}${description ? `, ${description}` : ''}`}
      columns={[
        { key: 'label', label: 'Time' },
        ...series.map((s) => ({ key: s.id, label: s.label, numeric: true })),
        { key: 'total', label: 'Total', numeric: true },
      ]}
      rows={data.map((d) => ({
        label: d.label,
        total: formatCount(d.total),
        ...Object.fromEntries(series.map((s) => [s.id, formatCount(d[s.id] ?? 0)])),
      }))}
    />
  );

  return (
    <ChartFrame
      title={title}
      description={description}
      headingLevel={headingLevel}
      legend={series.map((s) => ({ key: s.id, label: s.label, color: s.color }))}
      table={table}
      className={className}
    >
      <p id={summaryId} hidden>
        {summary}
      </p>
      <ResponsiveContainer className="tabular" width={width ?? '100%'} height={height} initialDimension={{ width: width ?? 640, height }}>
        <BarChart data={data} margin={{ top: 8, right: 4, bottom: 0, left: 0 }} barCategoryGap="20%" title={title} aria-describedby={summaryId}>
          <CartesianGrid vertical={false} stroke={gridStroke} />
          <XAxis
            dataKey="label"
            tickLine={false}
            axisLine={{ stroke: 'var(--border-strong)' }}
            tick={axisTick}
            interval="preserveStartEnd"
            minTickGap={16}
          />
          <YAxis allowDecimals={false} tickLine={false} axisLine={false} tick={axisTick} tickFormatter={(v: number) => formatCount(v)} width={40} />
          <Tooltip cursor={{ fill: 'var(--surface-hover)' }} content={renderTooltip} isAnimationActive={false} />
          {series.map((s) => (
            <Bar
              key={s.id}
              dataKey={s.id}
              name={s.label}
              stackId="outcomes"
              fill={s.color}
              maxBarSize={24}
              isAnimationActive={false}
              shape={(props: BarShapeProps) => (
                <path
                  data-series={s.id}
                  d={stackedSegmentPath(props, s.id, ids)}
                  fill={s.color}
                />
              )}
            />
          ))}
        </BarChart>
      </ResponsiveContainer>
    </ChartFrame>
  );
}

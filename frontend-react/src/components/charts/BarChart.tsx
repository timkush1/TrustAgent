/**
 * One measure across named categories ("What was caught": requests per reason; stage latencies):
 * horizontal bars from a single baseline, sorted as given, each at most 24px thick with a 4px
 * rounded end and its value written just past the end, so the chart reads without hovering.
 * One series means one color: the neutral ink, or a tone when the measure itself is an outcome
 * ("Blocked requests"). No legend: the title names the series. Hover or keyboard focus shows the
 * bar's value; the table view lists them all.
 */
import { useId } from 'react';
import { Bar, BarChart as RechartsBarChart, LabelList, ResponsiveContainer, Tooltip, XAxis, YAxis, type TooltipContentProps } from 'recharts';
import { formatCount } from '@/domain/format';
import { ChartFrame } from './ChartFrame';
import { ChartTable, ChartTooltipBox } from './ChartParts';
import { seriesColor, type ChartTone } from './palette';
import { axisTick, BAR_RADIUS } from './rechartsTheme';

export interface BarDatum {
  key: string;
  /** Category name in plain words. */
  label: string;
  value: number;
}

export interface BarChartProps {
  title: string;
  description?: string;
  data: readonly BarDatum[];
  /** Name of the measure ("Requests", "p95 latency"): the table's value column and the tooltip. */
  valueLabel: string;
  format?: (value: number) => string;
  tone?: ChartTone;
  /** Width of the category labels, px. */
  labelWidth?: number;
  width?: number;
  headingLevel?: 2 | 3 | 4;
  className?: string;
}

const ROW = 28;

export function BarChart({
  title,
  description,
  data,
  valueLabel,
  format = formatCount,
  tone = 'ink',
  labelWidth = 144,
  width,
  headingLevel,
  className,
}: BarChartProps) {
  const summaryId = useId();
  const color = seriesColor({ tone, shade: 'solid' });
  const height = Math.max(ROW, data.length * ROW) + 8;
  const rows = data.map((d) => ({ ...d, formatted: format(d.value) }));

  const renderTooltip = ({ active, payload }: TooltipContentProps) => {
    const d = active ? (payload?.[0]?.payload as (BarDatum & { formatted: string }) | undefined) : undefined;
    if (!d) return null;
    return <ChartTooltipBox title={d.label} rows={[{ key: d.key, label: valueLabel, value: d.formatted, color }]} />;
  };

  const table = (
    <ChartTable
      caption={`${title}${description ? `, ${description}` : ''}`}
      columns={[
        { key: 'label', label: 'Category' },
        { key: 'value', label: valueLabel, numeric: true },
      ]}
      rows={rows.map((d) => ({ label: d.label, value: d.formatted }))}
    />
  );

  return (
    <ChartFrame title={title} description={description} headingLevel={headingLevel} table={table} className={className}>
      <p id={summaryId} hidden>
        {rows.map((d) => `${d.label}: ${d.formatted}`).join('; ')}
      </p>
      <ResponsiveContainer className="tabular" width={width ?? '100%'} height={height} initialDimension={{ width: width ?? 480, height }}>
        <RechartsBarChart
          data={rows}
          layout="vertical"
          margin={{ top: 4, right: 48, bottom: 4, left: 0 }}
          barCategoryGap={6}
          title={title}
          aria-describedby={summaryId}
        >
          <XAxis type="number" hide domain={[0, 'dataMax']} />
          <YAxis type="category" dataKey="label" width={labelWidth} tickLine={false} axisLine={{ stroke: 'var(--border-strong)' }} tick={axisTick} />
          <Tooltip cursor={{ fill: 'var(--surface-hover)' }} content={renderTooltip} isAnimationActive={false} />
          <Bar dataKey="value" name={valueLabel} fill={color} maxBarSize={24} radius={[0, BAR_RADIUS, BAR_RADIUS, 0]} isAnimationActive={false}>
            <LabelList dataKey="formatted" position="right" offset={6} fill="var(--fg)" fontSize={11} />
          </Bar>
        </RechartsBarChart>
      </ResponsiveContainer>
    </ChartFrame>
  );
}

/**
 * A word-sized trend next to a number (fast-check p95 over the day, blocks per hour): a 2px line
 * with a dot on the latest value, no axes. Missing points (null) leave a gap instead of a fake 0.
 * Hovering shows the value under the pointer. It is an image to assistive tech, named with what it
 * shows and a summary (first, last, lowest, highest); the exact values belong in the table of the
 * chart or page it summarises, so the hover never holds the only copy of a number.
 */
import { useId, useState, type PointerEvent } from 'react';
import { cn } from '@/lib/cn';
import { seriesColor, type ChartTone } from './palette';

export interface SparklineProps {
  values: readonly (number | null)[];
  /** What the line shows ("Fast-check p95, last 24 hours"). */
  label: string;
  /** Labels for each point, for the hover readout ("14:00"). */
  pointLabels?: readonly string[];
  format?: (value: number) => string;
  /** Neutral ink by default; a tone only when the series is an outcome or verdict. */
  tone?: ChartTone;
  width?: number;
  height?: number;
  className?: string;
}

const PAD = 5; // Room for the end dot (r 4) and its ring.

const defaultFormat = (v: number) => new Intl.NumberFormat('en-US', { maximumFractionDigits: 2 }).format(v);

function sparklineSummary(values: readonly (number | null)[], format: (v: number) => string = defaultFormat): string {
  const present = values.filter((v): v is number => v !== null && Number.isFinite(v));
  if (present.length === 0) return 'no data';
  const first = present[0];
  const last = present[present.length - 1];
  const min = Math.min(...present);
  const max = Math.max(...present);
  return `from ${format(first)} to ${format(last)}, lowest ${format(min)}, highest ${format(max)}`;
}

export function Sparkline({
  values,
  label,
  pointLabels,
  format = defaultFormat,
  tone = 'ink',
  width = 96,
  height = 32,
  className,
}: SparklineProps) {
  const titleId = useId();
  const [hover, setHover] = useState<number | null>(null);
  const present = values.filter((v): v is number => v !== null && Number.isFinite(v));
  const min = present.length ? Math.min(...present) : 0;
  const max = present.length ? Math.max(...present) : 1;
  const span = max - min || 1;
  const n = values.length;
  const x = (i: number) => (n <= 1 ? width / 2 : PAD + (i / (n - 1)) * (width - 2 * PAD));
  const y = (v: number) => height - PAD - ((v - min) / span) * (height - 2 * PAD);

  // One path per run of present values, so a null leaves a gap. A run of one point (a value between
  // two gaps) is a zero-length line, which the round cap draws as a dot instead of nothing.
  const segments: string[] = [];
  let run: string[] = [];
  const endRun = () => {
    if (run.length === 1) run.push(run[0]);
    if (run.length > 0) segments.push(run.map((p, i) => `${i === 0 ? 'M' : 'L'}${p}`).join(''));
    run = [];
  };
  values.forEach((v, i) => {
    if (v === null || !Number.isFinite(v)) endRun();
    else run.push(`${x(i).toFixed(1)},${y(v).toFixed(1)}`);
  });
  endRun();

  let lastIndex = -1;
  for (let i = n - 1; i >= 0; i -= 1) {
    const v = values[i];
    if (v !== null && Number.isFinite(v)) {
      lastIndex = i;
      break;
    }
  }

  const color = seriesColor({ tone, shade: 'solid' });
  const summary = sparklineSummary(values, format);

  const onPointerMove = (e: PointerEvent<SVGSVGElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    if (rect.width === 0 || n === 0) return;
    const ratio = (e.clientX - rect.left) / rect.width;
    const i = Math.floor(ratio * (n - 1) + 0.5);
    setHover(Math.max(0, Math.min(n - 1, i)));
  };

  const hovered = hover !== null ? values[hover] : null;
  return (
    <span className={cn('relative inline-block align-middle', className)}>
      <svg
        role="img"
        aria-labelledby={titleId}
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        className="block overflow-visible"
        onPointerMove={onPointerMove}
        onPointerLeave={() => setHover(null)}
      >
        <title id={titleId}>{`${label}: ${summary}`}</title>
        {segments.map((d, i) => (
          <path key={i} d={d} fill="none" stroke={color} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
        ))}
        {hover !== null && hovered !== null && hovered !== undefined ? (
          <line x1={x(hover)} x2={x(hover)} y1={0} y2={height} stroke="var(--border-strong)" strokeWidth={1} />
        ) : null}
        {lastIndex >= 0 ? (
          <circle
            data-end-dot
            cx={x(lastIndex)}
            cy={y(values[lastIndex] as number)}
            r={4}
            fill={color}
            stroke="var(--surface)"
            strokeWidth={2}
            paintOrder="stroke"
          />
        ) : null}
      </svg>
      {hover !== null && hovered !== null && hovered !== undefined ? (
        <span
          aria-hidden="true"
          className="pointer-events-none absolute bottom-full left-1/2 z-10 mb-1 -translate-x-1/2 rounded-sm border border-border bg-surface-raised px-1.5 py-0.5 text-2xs whitespace-nowrap text-fg shadow-popup"
        >
          <span className="font-medium tabular">{format(hovered)}</span>
          {pointLabels?.[hover] ? <span className="ml-1 text-fg-muted">{pointLabels[hover]}</span> : null}
        </span>
      ) : null}
    </span>
  );
}

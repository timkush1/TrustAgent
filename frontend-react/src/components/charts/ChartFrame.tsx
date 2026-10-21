/**
 * The frame around a chart: its title and one-line description, the legend (for two or more
 * series), and a Chart / Table switch, because every chart has a table view with the same
 * numbers (DESIGN.md "Charts"). The title is a heading so screen-reader users can find the chart;
 * the table is the accessible equivalent of the picture.
 */
import { useId, useState, type ReactNode } from 'react';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { cn } from '@/lib/cn';
import { ChartLegend, type LegendItem } from './ChartParts';

export type ChartView = 'chart' | 'table';

export interface ChartFrameProps {
  title: string;
  /** What is counted and over what window ("Per hour, last 24 hours"). */
  description?: ReactNode;
  headingLevel?: 2 | 3 | 4;
  /** Required for two or more series; a single series is named by the title. */
  legend?: readonly LegendItem[];
  /** The table view (usually a <ChartTable>). */
  table: ReactNode;
  /** The chart itself. */
  children: ReactNode;
  /** Controls on the right of the title (not filters: those sit in the page's filter row). */
  actions?: ReactNode;
  /** A line under the header, e.g. why one of the actions is unavailable (a DisabledReason). */
  note?: ReactNode;
  defaultView?: ChartView;
  className?: string;
}

const viewOptions = [
  { value: 'chart', label: 'Chart' },
  { value: 'table', label: 'Table' },
] as const;

export function ChartFrame({
  title,
  description,
  headingLevel = 3,
  legend,
  table,
  children,
  actions,
  note,
  defaultView = 'chart',
  className,
}: ChartFrameProps) {
  const [view, setView] = useState<ChartView>(defaultView);
  const headingId = useId();
  const Heading = `h${headingLevel}` as const;
  return (
    <figure aria-labelledby={headingId} data-view={view} className={cn('min-w-0 space-y-3', className)}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-0.5">
          <Heading id={headingId} className="text-sm font-semibold text-fg">
            {title}
          </Heading>
          {description ? <p className="text-xs text-fg-muted">{description}</p> : null}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {actions}
          <SegmentedControl<ChartView>
            aria-label={`${title}: show as`}
            size="sm"
            options={viewOptions}
            value={view}
            onValueChange={setView}
          />
        </div>
      </div>
      {note ? <div className="min-w-0">{note}</div> : null}
      {legend && legend.length > 1 && view === 'chart' ? <ChartLegend items={legend} /> : null}
      {view === 'chart' ? <div className="min-w-0">{children}</div> : table}
    </figure>
  );
}

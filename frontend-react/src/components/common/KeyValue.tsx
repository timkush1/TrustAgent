/**
 * Facts as a description list: ids, timings, costs, versions. Keys are muted, values full ink;
 * numbers are tabular, ids and hashes mono, and a missing value is an em dash (never 0 or blank).
 * A value marked `copy` gets a copy button with its key in the name ("Copy trace id").
 */
import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';
import { CopyButton } from './CopyButton';

const EM_DASH = '—';

export interface KeyValueItem {
  /** The key, as the viewer would say it ("Trace id", "Fast check"). */
  label: string;
  value: ReactNode;
  /** Ids, hashes, versions. */
  mono?: boolean;
  /** Numbers (tabular figures). */
  numeric?: boolean;
  /** Text to copy; `true` copies `value` when it is a string. */
  copy?: string | boolean;
  /** A hint after the value ("Simulated", "estimate"). */
  hint?: ReactNode;
}

export interface KeyValueListProps {
  items: readonly KeyValueItem[];
  /** Two columns (key | value) or stacked (key over value) for narrow places. */
  layout?: 'columns' | 'stacked';
  className?: string;
}

function isMissing(v: ReactNode): boolean {
  return v === null || v === undefined || v === '' || v === false;
}

export function KeyValueList({ items, layout = 'columns', className }: KeyValueListProps) {
  return (
    <dl
      className={cn(
        'text-sm',
        layout === 'columns' ? 'grid grid-cols-[minmax(7rem,max-content)_minmax(0,1fr)] gap-x-4 gap-y-2' : 'space-y-2',
        className,
      )}
    >
      {items.map((item) => {
        const missing = isMissing(item.value);
        const copyValue = item.copy === true ? (typeof item.value === 'string' ? item.value : undefined) : item.copy || undefined;
        return (
          <div key={item.label} className={layout === 'columns' ? 'contents' : 'space-y-0.5'}>
            <dt className="text-fg-muted">{item.label}</dt>
            <dd className="flex min-w-0 items-center gap-1.5">
              <span
                className={cn(
                  'min-w-0 break-words',
                  missing ? 'text-fg-subtle' : 'text-fg',
                  item.mono && 'font-mono text-xs',
                  item.numeric && 'tabular',
                )}
              >
                {missing ? EM_DASH : item.value}
              </span>
              {item.hint ? <span className="shrink-0 text-xs text-fg-muted">{item.hint}</span> : null}
              {copyValue && !missing ? <CopyButton value={copyValue} label={`Copy ${item.label.toLowerCase()}`} className="-my-1" /> : null}
            </dd>
          </div>
        );
      })}
    </dl>
  );
}

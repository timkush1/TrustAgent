/**
 * Top-bar Health pill: a dot and "All systems ready" / "2 issues", opening the checklist
 * (SPEC §3.3). Fed by /health, /api/status when served, and the live connection.
 */
import { CircleCheck, CircleDashed, Info, RotateCw, TriangleAlert, type LucideIcon } from 'lucide-react';
import type { CSSProperties } from 'react';
import { Button } from '@/components/ui/button';
import { Popover, PopoverContent, PopoverTitle, PopoverTrigger } from '@/components/ui/popover';
import { liveStream } from '@/data/liveStream';
import { useStreamStore } from '@/data/stream/streamStore';
import { useHealthSummary } from '@/data/useSystem';
import type { CheckState } from '@/domain/health';
import { copy, toneVar } from '@/domain/presentation';
import { cn } from '@/lib/cn';

const checkIcon: Record<CheckState, { icon: LucideIcon; className: string; word: string }> = {
  ok: { icon: CircleCheck, className: 'text-tone-success', word: 'OK' },
  issue: { icon: TriangleAlert, className: 'text-tone-warning', word: 'Issue' },
  unknown: { icon: CircleDashed, className: 'text-fg-subtle', word: 'Unknown' },
  info: { icon: Info, className: 'text-fg-muted', word: 'Note' },
};

export function HealthPill() {
  const summary = useHealthSummary();
  const phase = useStreamStore((s) => s.connection.phase);
  const canRetry = phase === 'reconnecting' || phase === 'unauthorized';
  return (
    <Popover>
      <PopoverTrigger
        className="inline-flex h-7 items-center gap-2 rounded-full border border-border bg-surface px-2.5 text-xs font-medium text-fg hover:bg-surface-hover"
        aria-label={`${copy.health.checklist_title}: ${summary.label}`}
      >
        <span
          aria-hidden="true"
          style={{ '--dot': `var(${toneVar(summary.tone)})` } as CSSProperties}
          className="size-2 rounded-full bg-(--dot)"
        />
        <span className="tabular">{summary.label}</span>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80">
        <PopoverTitle className="mb-2 text-sm font-semibold">{copy.health.checklist_title}</PopoverTitle>
        <ul className="space-y-2">
          {summary.checks.map((c) => {
            const { icon: Icon, className, word } = checkIcon[c.state];
            return (
              <li key={c.id} className="flex items-start gap-2">
                <Icon aria-hidden="true" className={cn('mt-0.5 size-4 shrink-0', className)} />
                <div className="min-w-0">
                  <p className="text-sm text-fg">
                    {c.label}
                    <span className="sr-only">: {word}</span>
                  </p>
                  <p className="text-xs text-fg-muted">{c.detail}</p>
                </div>
              </li>
            );
          })}
        </ul>
        {canRetry ? (
          <Button size="sm" className="mt-3" onClick={() => liveStream()?.retryNow()}>
            <RotateCw aria-hidden="true" />
            Reconnect now
          </Button>
        ) : null}
      </PopoverContent>
    </Popover>
  );
}

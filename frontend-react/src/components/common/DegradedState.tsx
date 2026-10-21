/**
 * The degraded state of the page contract (SPEC §3.3): a part of the page works with less than
 * usual. It names what is missing, what still works, and the fix, in place of the part it
 * affects (not as a second banner: the shell's single banner and the Health pill carry
 * stack-wide trouble). Example: "Claims are not available: the deep audit is not reachable.
 * Fast checks still run."
 */
import { TriangleAlert } from 'lucide-react';
import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

export interface DegradedStateProps {
  /** What is missing, in plain words. */
  title: string;
  /** Why, when it is known (the component's own status reason). */
  reason?: ReactNode;
  /** What still works ("Fast checks still run"). */
  stillWorks?: ReactNode;
  /** The fix: a real link, a button, or a command. */
  action?: ReactNode;
  className?: string;
}

export function DegradedState({ title, reason, stillWorks, action, className }: DegradedStateProps) {
  return (
    <div
      role="status"
      data-state="degraded"
      className={cn('flex flex-col items-start gap-2 rounded-lg border border-dashed border-border-strong bg-surface px-4 py-3', className)}
    >
      <div className="flex items-start gap-2">
        <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-warning" />
        <div className="space-y-0.5">
          <p className="text-sm font-medium text-fg">{title}</p>
          {reason ? <p className="text-sm text-fg-muted">{reason}</p> : null}
          {stillWorks ? <p className="text-sm text-fg-muted">{stillWorks}</p> : null}
        </div>
      </div>
      {action ? <div className="pl-6">{action}</div> : null}
    </div>
  );
}

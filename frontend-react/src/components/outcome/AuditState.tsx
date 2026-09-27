/** A trace's deep-audit state in words ("Deep audit running", "Hallucinated", "Not assessed"). */
import type { CSSProperties } from 'react';
import type { TraceSummary } from '@/types/api';
import { auditStateView, toneVar } from '@/domain/presentation';
import { cn } from '@/lib/cn';

export function AuditState({ audit, className }: { audit: Pick<TraceSummary['audit'], 'status' | 'verdict'>; className?: string }) {
  const view = auditStateView(audit);
  const Icon = view.icon;
  return (
    <span
      style={{ '--tone': `var(${toneVar(view.tone)})` } as CSSProperties}
      className={cn(
        'inline-flex min-w-0 items-center gap-1 text-xs',
        view.tone === 'neutral' ? 'text-fg-muted' : 'font-medium text-(--tone)',
        className,
      )}
    >
      <Icon aria-hidden="true" className={cn('size-3.5 shrink-0', view.pending && 'animate-spin')} />
      <span className="truncate">{view.label}</span>
    </span>
  );
}

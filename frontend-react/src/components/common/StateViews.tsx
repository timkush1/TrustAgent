/**
 * Page-contract states (SPEC §3.3): empty, loading, error (server message + retry). Degraded,
 * demo and read-only are shell-level (banner) plus <DisabledReason> on the affected actions.
 */
import { CircleAlert, Inbox, RotateCw, type LucideIcon } from 'lucide-react';
import type { ReactNode } from 'react';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { isApiError } from '@/data/errors';
import { copy, fillTemplate } from '@/domain/presentation';
import { cn } from '@/lib/cn';

export function EmptyState({
  icon: Icon = Inbox,
  title,
  children,
  action,
  className,
}: {
  icon?: LucideIcon;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn('flex flex-col items-center gap-3 rounded-lg border border-dashed border-border px-6 py-12 text-center', className)}>
      <Icon aria-hidden="true" className="size-6 text-fg-subtle" />
      <div className="max-w-md space-y-1">
        <p className="text-base font-medium text-fg">{title}</p>
        {children ? <div className="text-sm text-fg-muted">{children}</div> : null}
      </div>
      {action}
    </div>
  );
}

export function ErrorState({
  error,
  onRetry,
  className,
}: {
  error: unknown;
  onRetry?: () => void;
  className?: string;
}) {
  const message = isApiError(error) || error instanceof Error ? error.message : String(error);
  const code = isApiError(error) && error.kind === 'http' ? `HTTP ${error.status} · ${error.code}` : null;
  return (
    <div role="alert" className={cn('flex flex-col items-start gap-3 rounded-lg border border-border bg-surface px-4 py-4', className)}>
      <div className="flex items-start gap-2">
        <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-danger" />
        <div className="space-y-0.5">
          <p className="text-sm text-fg">{fillTemplate(copy.states.error, { message })}</p>
          {code ? <p className="font-mono text-2xs text-fg-subtle">{code}</p> : null}
        </div>
      </div>
      {onRetry ? (
        <Button size="sm" onClick={onRetry}>
          <RotateCw aria-hidden="true" />
          {copy.states.retry}
        </Button>
      ) : null}
    </div>
  );
}

/** Skeleton rows with a live-region label; render it only when useDelayedLoading says so. */
export function LoadingState({ rows = 3, className }: { rows?: number; className?: string }) {
  return (
    <div aria-busy="true" className={cn('space-y-2', className)}>
      <span className="sr-only" role="status">
        {copy.states.loading}
      </span>
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className="h-9 w-full" />
      ))}
    </div>
  );
}

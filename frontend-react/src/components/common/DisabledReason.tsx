/**
 * Why an action is unavailable, next to it, with the fix when there is one (SPEC §3.3 page
 * contract: no dead buttons). Reasons come from `copy.disabled_reasons`; a page-specific reason
 * ("Label 157 more responses first") passes its own text and fix.
 *
 * <DisabledAction> wires it up: the control is disabled but stays focusable, and it is described
 * by the reason, so keyboard and screen-reader users hear why as well as see it.
 */
import { Link } from '@tanstack/react-router';
import { Lock } from 'lucide-react';
import { useId, type ReactNode } from 'react';
import { commandText, copy, fillTemplate, splitCommands } from '@/domain/presentation';
import { cn } from '@/lib/cn';

export type DisabledReasonId = keyof typeof copy.disabled_reasons;

/** A reason that is not in the contract copy: its words and, when there is one, the fix. */
export interface CustomReason {
  text: string;
  fix?: ReactNode;
}

export type Reason = DisabledReasonId | CustomReason;

const linkClass = 'font-medium text-fg underline underline-offset-2';

function SettingsLink({ section, children }: { section: 'connections' | 'access' | 'all'; children: ReactNode }) {
  return (
    <Link to="/app/settings/$section" params={{ section }} className={linkClass}>
      {children}
    </Link>
  );
}

/** "To use your own keys: `make demo-down && make up`" with the command as code. */
function DemoFix() {
  const text = fillTemplate(copy.banners.demo.action, {});
  if (!commandText('demo_to_own_keys')) return null;
  return (
    <>
      {splitCommands(text).map((part, i) =>
        part.command ? (
          <code key={i} className="rounded-sm bg-surface-sunken px-1 font-mono text-2xs text-fg">
            {part.text}
          </code>
        ) : (
          <span key={i}>{part.text}</span>
        ),
      )}
    </>
  );
}

function contractFix(reason: DisabledReasonId): ReactNode {
  switch (reason) {
    case 'needs_connection':
      return <SettingsLink section="connections">Open Connections</SettingsLink>;
    case 'needs_admin':
      // Signing in with an admin key (or claiming the setup link) is the fix; an admin creates more
      // keys in Settings → Access.
      return (
        <Link to="/app/setup" className={linkClass}>
          Sign in as an admin
        </Link>
      );
    case 'env_managed':
      return <SettingsLink section="all">See where it is set</SettingsLink>;
    case 'demo':
      return <DemoFix />;
    // Read-only and database-less instances are the operator's choice: the reason is the answer.
    case 'readonly':
    case 'needs_database':
      return null;
    default: {
      const unhandled: never = reason;
      return unhandled;
    }
  }
}

export function DisabledReason({ reason, id, className }: { reason: Reason; id?: string; className?: string }) {
  const text = typeof reason === 'string' ? copy.disabled_reasons[reason] : reason.text;
  const fix = typeof reason === 'string' ? contractFix(reason) : reason.fix;
  return (
    // The lock stays beside the first line however the reason wraps: the text and its fix wrap
    // together in one box next to it.
    <span id={id} className={cn('inline-flex items-start gap-1 text-xs text-fg-muted', className)}>
      <Lock aria-hidden="true" className="mt-0.5 size-3 shrink-0" />
      <span className="min-w-0">
        {text}
        {fix ? (
          <>
            <span aria-hidden="true"> · </span>
            {fix}
          </>
        ) : null}
      </span>
    </span>
  );
}

export interface DisabledActionProps {
  /** Why the action is unavailable; null (or false) when it is available. */
  reason: Reason | null | undefined | false;
  /** Renders the control with the props that disable and describe it. */
  children: (props: { disabled: boolean; 'aria-describedby'?: string }) => ReactNode;
  /** Reason beside the control (`inline`, default) or under it (`stacked`). */
  layout?: 'inline' | 'stacked';
  className?: string;
}

export function DisabledAction({ reason, children, layout = 'inline', className }: DisabledActionProps) {
  const id = useId();
  if (!reason) return <>{children({ disabled: false })}</>;
  return (
    <span
      className={cn(
        'inline-flex gap-2',
        layout === 'stacked' ? 'flex-col items-start gap-1' : 'flex-wrap items-center',
        className,
      )}
    >
      {children({ disabled: true, 'aria-describedby': id })}
      <DisabledReason id={id} reason={reason} />
    </span>
  );
}

/**
 * Pieces every Settings section shares: the failure note (the server's message with its code, the
 * plain hint for P4 codes, and the names and routes a code carries), the "set in .env" note, and the
 * credentials-store and settings warnings (P4-PLAN D32, D33, D37, D77).
 */
import { Link } from '@tanstack/react-router';
import { CircleAlert, Lock } from 'lucide-react';
import type { ReactNode } from 'react';
import { DegradedState } from '@/components/common/DegradedState';
import { describeError } from '@/components/common/describeError';
import { errorCodeHint, isApiError, P4_FIELD_ERROR_CODES } from '@/data/errors';
import { useSystemStatus } from '@/data/useSystem';
import { commandText, copy, fillTemplate, secCredentialLabel, secJoinWords, secStoreView, secWarningText } from '@/domain/presentation';
import { cn } from '@/lib/cn';
import type { SettingsView } from '@/types/api';

const linkClass = 'font-medium text-fg underline underline-offset-2';

/** Plain words for what a failed write carries in `details` and its field errors. */
function failureDetails(error: unknown): string[] {
  if (!isApiError(error)) return [];
  const lines: string[] = [];
  const details = error.details as { routes?: unknown; keys?: unknown; names?: unknown; reason?: unknown };
  const routes = Array.isArray(details.routes) ? details.routes.map(String) : [];
  if (error.code === 'confirmation_required' && routes.length > 0) {
    lines.push(fillTemplate(copy.settings.fail_closed_confirm, { routes: routes.join(', ') }).replace(/\s*Continue anyway\?$/, ''));
  }
  if (error.code === 'env_managed' && Array.isArray(details.keys) && details.keys.length > 0) {
    lines.push(fillTemplate(copy.settings.env_managed, { keys: details.keys.map(String).join(', ') }));
  }
  if (error.code === 'conflict' && details.reason === 'last_admin_key') lines.push(copy.settings.last_admin_key);
  if (error.code === 'stale_version') lines.push(copy.settings.stale_version);
  for (const fe of error.fieldErrors) {
    const code = fe.code;
    if (code === 'reenter_secret') {
      const raw = fe.details?.names;
      const names = (Array.isArray(raw) ? raw.map(String) : []).map(secCredentialLabel);
      lines.push(fillTemplate(copy.settings.reenter_secret, { names: secJoinWords(names) }));
    } else if (code && Object.hasOwn(P4_FIELD_ERROR_CODES, code)) {
      lines.push(P4_FIELD_ERROR_CODES[code as keyof typeof P4_FIELD_ERROR_CODES]);
    }
  }
  if (lines.length === 0) {
    const hint = errorCodeHint(error);
    if (hint) lines.push(hint);
  }
  return lines;
}

/** A failed write, in place: the server's message and code, then what to do. */
export function ProblemNote({ error, title, action, className }: { error: unknown; title?: string; action?: ReactNode; className?: string }) {
  const { message, code } = describeError(error);
  const lines = failureDetails(error);
  return (
    <div role="alert" className={cn('space-y-1 rounded-md border border-border bg-surface px-3 py-2', className)}>
      <p className="flex items-start gap-1.5 text-sm text-fg">
        <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-danger" />
        <span>
          {title ? `${title}: ` : null}
          {message}
        </span>
      </p>
      {lines.map((line) => (
        <p key={line} className="pl-5.5 text-sm text-fg-muted">
          {line}
        </p>
      ))}
      {code ? <p className="pl-5.5 font-mono text-2xs text-fg-subtle">{code}</p> : null}
      {action ? <div className="pl-5.5">{action}</div> : null}
    </div>
  );
}

/** "Set in .env (OPENROUTER_API_KEY); change it there and restart." */
export function EnvManagedNote({ keys, engine = false, className }: { keys: readonly string[]; engine?: boolean; className?: string }) {
  const text = engine
    ? fillTemplate(copy.settings.engine_env_managed, {})
    : fillTemplate(copy.settings.env_managed, { keys: keys.join(', ') });
  const doctor = commandText('doctor');
  return (
    <p className={cn('flex items-start gap-1.5 text-xs text-fg-muted', className)}>
      <Lock aria-hidden="true" className="mt-0.5 size-3 shrink-0" />
      <span>
        {engine && doctor ? (
          <>
            {text.replace(doctor, '').replace(/\s+\.$/, '').trim()}{' '}
            <code className="rounded-sm bg-surface-sunken px-1 font-mono text-2xs text-fg">{doctor}</code>.
          </>
        ) : (
          text
        )}
      </span>
    </p>
  );
}

/** The credentials store, when keys cannot be saved here (D32), and keys the master key cannot read. */
export function StoreNotice() {
  const { status } = useSystemStatus();
  if (!status) return null;
  const { store, undecryptable } = status.credentials;
  const view = secStoreView(store);
  return (
    <>
      {view.writable ? null : (
        <DegradedState
          title={view.title}
          reason={view.body}
          stillWorks="Keys set in .env keep working, and you can still test them here."
          action={
            <Link to="/app/settings/$section" params={{ section: 'all' }} className={cn(linkClass, 'text-sm')}>
              See where each setting comes from
            </Link>
          }
        />
      )}
      {undecryptable.length > 0 ? (
        <DegradedState
          title="Some saved keys can't be read"
          reason={fillTemplate(copy.settings.master_key_changed, { names: secJoinWords(undecryptable.map(secCredentialLabel)) })}
          stillWorks="Every other key keeps working."
        />
      ) : null}
    </>
  );
}

/** Settings warnings (SettingsView.warnings): words by code, else the server's message and hint. */
export function SettingsWarnings({ warnings, skip = [] }: { warnings: SettingsView['warnings']; skip?: readonly string[] }) {
  const shown = warnings.filter((w) => !w.code || !skip.includes(w.code));
  if (shown.length === 0) return null;
  return (
    <ul aria-label="Settings warnings" className="space-y-2">
      {shown.map((w) => (
        <li key={`${w.key}:${w.code ?? w.message}`}>
          <DegradedState title={secWarningText(w.code, w.details) ?? w.message} reason={w.hint || undefined} />
        </li>
      ))}
    </ul>
  );
}

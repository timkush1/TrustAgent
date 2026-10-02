/**
 * A secret the server shows once (a new admin key, an API key): the value with a copy button, why
 * it must be saved now, and an "I saved it" box that the next step waits for (SPEC §3.3 setup
 * wizard; P4-PLAN ON-3, ON-7). The proxy keeps only a hash, so there is no second chance to copy it.
 */
import { KeyRound } from 'lucide-react';
import { Checkbox } from '@/components/ui/checkbox';
import { copy } from '@/domain/presentation';
import { cn } from '@/lib/cn';
import { CopyButton } from './CopyButton';

export interface SecretOnceProps {
  /** What it is: "Your admin key (shown once)". */
  title: string;
  value: string;
  /** Why to save it now (default copy.settings.secret_once). */
  note?: string;
  copyLabel?: string;
  /** The "I saved it" box; leave both out to show the secret without one. */
  saved?: boolean;
  onSavedChange?: (saved: boolean) => void;
  className?: string;
}

export function SecretOnce({ title, value, note = copy.settings.secret_once, copyLabel = 'Copy the key', saved, onSavedChange, className }: SecretOnceProps) {
  return (
    <div className={cn('space-y-2 rounded-md border border-border-strong px-3 py-3', className)}>
      <p className="flex items-center gap-1.5 text-sm font-medium text-fg">
        <KeyRound aria-hidden="true" className="size-4 text-fg-muted" />
        {title}
      </p>
      <div className="flex items-center gap-2">
        <code className="min-w-0 flex-1 truncate rounded-sm bg-surface-sunken px-2 py-1 font-mono text-xs text-fg" title={value}>
          {value}
        </code>
        <CopyButton value={value} label={copyLabel} showLabel />
      </div>
      <p className="text-xs text-fg-muted">{note}</p>
      {onSavedChange ? (
        <Checkbox label={copy.setup.step_secure.saved_confirm} checked={saved ?? false} onCheckedChange={(checked) => onSavedChange(checked === true)} />
      ) : null}
    </div>
  );
}

/**
 * A block of code, a command or JSON, in the mono face with a copy button. Long lines scroll
 * sideways (or wrap with `wrap`); a block taller than `maxHeight` scrolls, and the scroll area
 * is focusable so keyboard users can scroll it too. Secrets are masked by the caller before they
 * get here: this component shows exactly what it is given.
 */
import { useId } from 'react';
import { cn } from '@/lib/cn';
import { CopyButton } from './CopyButton';

export interface CodeBlockProps {
  code: string;
  /** Caption above the block ("curl", "Python", "decision/v1"); also names the scroll region. */
  label?: string;
  /** Copy button name (default "Copy" + the label). */
  copyLabel?: string;
  /** Copy something other than what is shown (the unmasked command, say). */
  copyValue?: string;
  wrap?: boolean;
  /** CSS max-height of the scroll area, e.g. "16rem". */
  maxHeight?: string;
  className?: string;
}

export function CodeBlock({ code, label, copyLabel, copyValue, wrap = false, maxHeight, className }: CodeBlockProps) {
  const captionId = useId();
  return (
    <figure className={cn('min-w-0 overflow-hidden rounded-lg border border-border bg-surface-sunken', className)}>
      <div className="flex items-center justify-between gap-2 border-b border-border py-1 pr-1 pl-3">
        {label ? (
          <figcaption id={captionId} className="truncate text-xs font-medium text-fg-muted">
            {label}
          </figcaption>
        ) : (
          <span />
        )}
        <CopyButton value={copyValue ?? code} label={copyLabel ?? (label ? `Copy ${label}` : 'Copy')} />
      </div>
      <pre
        // The region scrolls, so it must be reachable by keyboard (axe scrollable-region-focusable).
        // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
        tabIndex={0}
        role="region"
        aria-labelledby={label ? captionId : undefined}
        aria-label={label ? undefined : 'Code'}
        style={maxHeight ? { maxHeight } : undefined}
        className={cn(
          'overflow-auto px-3 py-2.5 font-mono text-xs leading-5 text-fg focus-visible:-outline-offset-2',
          wrap ? 'break-words whitespace-pre-wrap' : 'whitespace-pre',
        )}
      >
        <code>{code}</code>
      </pre>
    </figure>
  );
}

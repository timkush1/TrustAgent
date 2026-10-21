/**
 * A keyboard key ("C", "Space", "Esc"). `keys` renders a combination as nested <kbd> elements,
 * which is how HTML marks up a key chord, with "+" between the keys for sighted readers.
 * Shortcuts are hints: every action they trigger also has a visible control.
 */
import { Fragment, type HTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

const keyClass =
  'inline-flex h-5 min-w-5 items-center justify-center rounded-sm border border-border-strong bg-surface-sunken px-1 font-sans text-2xs font-medium text-fg-muted';

export type KbdProps = HTMLAttributes<HTMLElement> & { keys?: readonly string[] };

export function Kbd({ keys, children, className, ...props }: KbdProps) {
  if (!keys || keys.length === 0) {
    return (
      <kbd className={cn(keyClass, className)} {...props}>
        {children}
      </kbd>
    );
  }
  return (
    <kbd className={cn('inline-flex items-center gap-0.5 font-sans text-2xs text-fg-subtle', className)} {...props}>
      {keys.map((key, i) => (
        <Fragment key={`${key}-${i}`}>
          {i > 0 ? <span aria-hidden="true">+</span> : null}
          <kbd className={keyClass}>{key}</kbd>
        </Fragment>
      ))}
    </kbd>
  );
}

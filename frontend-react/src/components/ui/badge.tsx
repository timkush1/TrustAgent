/**
 * Badge: a small, non-interactive label in a tone and an emphasis. It is the one pill shape of the
 * dashboard: <OutcomePill> is a Badge driven by ui/outcomes.v1.json, and anything else that needs
 * a pill uses this, so tone and shape always mean the same thing (DESIGN.md "Outcome encoding"):
 *
 * - `solid`: inverted fill in the tone (the answer was stopped).
 * - `tint`: tinted fill with a tone border (something happened).
 * - `outline`: dashed tone outline (it only would have).
 * - `quiet`: neutral, nothing to judge.
 *
 * A tone always comes with an icon or words that say the same thing: never color alone.
 *
 * Chip: an achromatic token for a value the viewer chose (a filter, a "used for" entry), with an
 * optional remove button.
 */
import { X, type LucideIcon } from 'lucide-react';
import type { CSSProperties, HTMLAttributes, ReactNode } from 'react';
import { toneVar, type Tone } from '@/domain/presentation';
import { cn } from '@/lib/cn';

export type BadgeEmphasis = 'solid' | 'tint' | 'outline' | 'quiet';

const emphasisClass: Record<BadgeEmphasis, string> = {
  solid: 'border-transparent bg-(--badge) text-fg-inverse',
  tint: 'border-[color-mix(in_oklab,var(--badge)_40%,transparent)] bg-[color-mix(in_oklab,var(--badge)_var(--tint),transparent)] text-(--badge)',
  outline: 'border-dashed border-(--badge) bg-transparent text-(--badge)',
  quiet: 'border-border bg-surface-sunken text-fg-muted',
};

export interface BadgeProps extends Omit<HTMLAttributes<HTMLSpanElement>, 'children'> {
  tone?: Tone;
  emphasis?: BadgeEmphasis;
  size?: 'sm' | 'md';
  icon?: LucideIcon;
  /** Spin the icon (a pending state such as "Checking"). */
  spin?: boolean;
  children: ReactNode;
}

export function Badge({
  tone = 'neutral',
  emphasis = 'quiet',
  size = 'md',
  icon: Icon,
  spin = false,
  className,
  style,
  children,
  ...props
}: BadgeProps) {
  return (
    <span
      data-tone={tone}
      data-emphasis={emphasis}
      style={{ '--badge': `var(${toneVar(tone)})`, ...style } as CSSProperties}
      className={cn(
        'inline-flex max-w-full items-center gap-1 rounded-full border font-medium whitespace-nowrap',
        size === 'sm' ? 'h-5 px-1.5 text-2xs' : 'h-6 px-2 text-xs',
        emphasisClass[emphasis],
        // Grey on a grey tint falls short of 4.5:1 in the dark theme; neutral text uses fg-muted.
        tone === 'neutral' && emphasis === 'tint' && 'text-fg-muted',
        className,
      )}
      {...props}
    >
      {Icon ? (
        <Icon aria-hidden="true" className={cn('shrink-0', size === 'sm' ? 'size-3' : 'size-3.5', spin && 'animate-spin')} />
      ) : null}
      <span className="truncate">{children}</span>
    </span>
  );
}

export interface ChipProps {
  children: ReactNode;
  icon?: LucideIcon;
  /** Adds a remove button; its accessible name is `removeLabel`. */
  onRemove?: () => void;
  /** Name of the remove button, e.g. "Remove filter: Blocked". Required with onRemove. */
  removeLabel?: string;
  className?: string;
}

export function Chip({ children, icon: Icon, onRemove, removeLabel, className }: ChipProps) {
  if (import.meta.env.DEV && onRemove && !removeLabel) console.error('<Chip onRemove> needs a removeLabel');
  return (
    <span
      className={cn(
        'inline-flex h-6 max-w-full items-center gap-1 rounded-full border border-border bg-surface pl-2 text-xs text-fg',
        onRemove ? 'pr-0.5' : 'pr-2',
        className,
      )}
    >
      {Icon ? <Icon aria-hidden="true" className="size-3.5 shrink-0 text-fg-muted" /> : null}
      <span className="truncate">{children}</span>
      {onRemove ? (
        <button
          type="button"
          aria-label={removeLabel}
          onClick={onRemove}
          className="inline-flex size-5 shrink-0 items-center justify-center rounded-full text-fg-muted hover:bg-surface-hover hover:text-fg"
        >
          <X aria-hidden="true" className="size-3" />
        </button>
      ) : null}
    </span>
  );
}

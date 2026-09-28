/**
 * A segmented control: two to five short, equal options side by side (a time window, "Chart /
 * Table", "Chat / Audit"). It is a radio group, so arrow keys move the choice and exactly one
 * option is always selected. Options that need a sentence each belong in <RadioGroup>.
 */
import { Radio } from '@base-ui/react/radio';
import { RadioGroup as BaseRadioGroup } from '@base-ui/react/radio-group';
import type { LucideIcon } from 'lucide-react';
import { cn } from '@/lib/cn';

export interface SegmentOption<V extends string = string> {
  value: V;
  label: string;
  icon?: LucideIcon;
  /** Show only the icon; `label` becomes the accessible name. */
  iconOnly?: boolean;
  disabled?: boolean;
}

export interface SegmentedControlProps<V extends string = string> {
  /** Names the group for screen readers ("Time window"). */
  'aria-label': string;
  options: readonly SegmentOption<V>[];
  value?: V;
  defaultValue?: V;
  onValueChange?: (value: V) => void;
  size?: 'sm' | 'md';
  disabled?: boolean;
  name?: string;
  className?: string;
}

export function SegmentedControl<V extends string = string>({
  'aria-label': ariaLabel,
  options,
  value,
  defaultValue,
  onValueChange,
  size = 'md',
  disabled,
  name,
  className,
}: SegmentedControlProps<V>) {
  return (
    <BaseRadioGroup
      aria-label={ariaLabel}
      value={value}
      defaultValue={defaultValue ?? (value === undefined ? options[0]?.value : undefined)}
      onValueChange={(next) => onValueChange?.(next as V)}
      disabled={disabled}
      name={name}
      className={cn('inline-flex items-center gap-0.5 rounded-md border border-border bg-surface-sunken p-0.5', className)}
    >
      {options.map((o) => {
        const Icon = o.icon;
        return (
          <Radio.Root
            key={o.value}
            value={o.value}
            disabled={o.disabled}
            aria-label={o.iconOnly ? o.label : undefined}
            className={cn(
              'inline-flex cursor-pointer items-center justify-center gap-1 rounded-sm font-medium whitespace-nowrap text-fg-muted select-none',
              'transition-[background-color,color] hover:not-data-checked:not-data-disabled:text-fg',
              'data-checked:bg-surface data-checked:text-fg data-checked:ring-1 data-checked:ring-fg-subtle/80',
              'data-disabled:cursor-not-allowed data-disabled:opacity-50',
              size === 'sm' ? 'h-6 px-2 text-xs' : 'h-7 px-2.5 text-sm',
              o.iconOnly && (size === 'sm' ? 'w-6 px-0' : 'w-7 px-0'),
            )}
          >
            {Icon ? <Icon aria-hidden="true" className="size-3.5 shrink-0" /> : null}
            {o.iconOnly ? null : o.label}
          </Radio.Root>
        );
      })}
    </BaseRadioGroup>
  );
}

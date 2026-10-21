/**
 * Select over Base UI: pick one value from a short, fixed list (a route mode, a model, a page
 * size). For long or searchable lists use <Combobox>. Put it inside a <Field> for its label, or
 * give it an `aria-label`.
 */
import { Select as BaseSelect } from '@base-ui/react/select';
import { Check, ChevronsUpDown } from 'lucide-react';
import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';
import { fieldControlClass, optionClass, popupClass } from './control-styles';

export interface SelectOption<V extends string = string> {
  value: V;
  label: string;
  /** A second line under the label. */
  description?: string;
  disabled?: boolean;
}

export interface SelectProps<V extends string = string> {
  options: readonly SelectOption<V>[];
  value?: V | null;
  defaultValue?: V | null;
  onValueChange?: (value: V | null) => void;
  placeholder?: string;
  disabled?: boolean;
  readOnly?: boolean;
  required?: boolean;
  name?: string;
  id?: string;
  /** Required when the select is not inside a <Field>. */
  'aria-label'?: string;
  className?: string;
  /** Rendered before the value inside the trigger (an icon, say). */
  prefix?: ReactNode;
}

export function Select<V extends string = string>({
  options,
  value,
  defaultValue,
  onValueChange,
  placeholder = 'Choose…',
  disabled,
  readOnly,
  required,
  name,
  id,
  'aria-label': ariaLabel,
  className,
  prefix,
}: SelectProps<V>) {
  const items = options.map((o) => ({ value: o.value, label: o.label }));
  return (
    <BaseSelect.Root<V>
      items={items}
      value={value}
      defaultValue={defaultValue}
      onValueChange={(next) => onValueChange?.(next)}
      disabled={disabled}
      readOnly={readOnly}
      required={required}
      name={name}
      id={id}
    >
      <BaseSelect.Trigger
        aria-label={ariaLabel}
        className={cn(fieldControlClass, 'inline-flex h-8 items-center justify-between gap-2 pr-2 text-left', className)}
      >
        {prefix}
        <BaseSelect.Value placeholder={placeholder} className="min-w-0 flex-1 truncate data-placeholder:text-fg-subtle" />
        <BaseSelect.Icon className="text-fg-muted">
          <ChevronsUpDown aria-hidden="true" className="size-4" />
        </BaseSelect.Icon>
      </BaseSelect.Trigger>
      <BaseSelect.Portal>
        <BaseSelect.Positioner alignItemWithTrigger={false} sideOffset={4} className="z-50 outline-none select-none">
          <BaseSelect.Popup className={cn(popupClass, 'min-w-(--anchor-width)')}>
            <BaseSelect.List className="max-h-(--available-height) overflow-y-auto">
              {options.map((o) => (
                <BaseSelect.Item key={o.value} value={o.value} label={o.label} disabled={o.disabled} className={cn(optionClass, 'pr-3')}>
                  <span className="flex w-4 shrink-0 justify-center">
                    <BaseSelect.ItemIndicator>
                      <Check aria-hidden="true" />
                    </BaseSelect.ItemIndicator>
                  </span>
                  <span className={cn('min-w-0 flex-1', !o.description && 'truncate')}>
                    <BaseSelect.ItemText className="block truncate">{o.label}</BaseSelect.ItemText>
                    {o.description ? <span className="block text-xs text-fg-muted">{o.description}</span> : null}
                  </span>
                </BaseSelect.Item>
              ))}
            </BaseSelect.List>
          </BaseSelect.Popup>
        </BaseSelect.Positioner>
      </BaseSelect.Portal>
    </BaseSelect.Root>
  );
}

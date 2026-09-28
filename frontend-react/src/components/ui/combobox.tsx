/**
 * Combobox over Base UI: type to filter a long list, then pick one (a model id, a route, an
 * evaluator). The list says so when nothing matches. Put it inside a <Field> for its label, or
 * give it an `aria-label`.
 */
import { Combobox as BaseCombobox } from '@base-ui/react/combobox';
import { Check, ChevronsUpDown, X } from 'lucide-react';
import { useMemo } from 'react';
import { cn } from '@/lib/cn';
import { fieldControlClass, optionClass, popupClass } from './control-styles';

export interface ComboboxOption<V extends string = string> {
  value: V;
  label: string;
  /** A second line under the label (a provider, a count). */
  description?: string;
  disabled?: boolean;
}

export interface ComboboxProps<V extends string = string> {
  options: readonly ComboboxOption<V>[];
  value?: V | null;
  defaultValue?: V | null;
  onValueChange?: (value: V | null) => void;
  placeholder?: string;
  /** Shown in the list when the typed text matches nothing. */
  emptyText?: string;
  disabled?: boolean;
  readOnly?: boolean;
  required?: boolean;
  name?: string;
  /** Required when the combobox is not inside a <Field>. */
  'aria-label'?: string;
  className?: string;
}

export function Combobox<V extends string = string>({
  options,
  value,
  defaultValue,
  onValueChange,
  placeholder,
  emptyText = 'No matches',
  disabled,
  readOnly,
  required,
  name,
  'aria-label': ariaLabel,
  className,
}: ComboboxProps<V>) {
  const byValue = useMemo(() => new Map(options.map((o) => [o.value, o])), [options]);
  const toOption = (v: V | null | undefined) => (v == null ? v : (byValue.get(v) ?? null));
  return (
    <BaseCombobox.Root<ComboboxOption<V>>
      items={options as ComboboxOption<V>[]}
      value={value === undefined ? undefined : toOption(value)}
      defaultValue={defaultValue === undefined ? undefined : toOption(defaultValue)}
      onValueChange={(next) => onValueChange?.(next?.value ?? null)}
      itemToStringLabel={(o) => o.label}
      itemToStringValue={(o) => o.value}
      isItemEqualToValue={(a, b) => a.value === b.value}
      disabled={disabled}
      readOnly={readOnly}
      required={required}
      name={name}
      autoHighlight
    >
      <BaseCombobox.InputGroup className={cn('relative flex items-center', className)}>
        <BaseCombobox.Input
          placeholder={placeholder}
          aria-label={ariaLabel}
          className={cn(fieldControlClass, 'h-8 pr-14')}
        />
        <span className="absolute inset-y-0 right-1 flex items-center gap-0.5">
          <BaseCombobox.Clear
            aria-label="Clear"
            className="inline-flex size-6 items-center justify-center rounded-sm text-fg-muted hover:bg-surface-hover hover:text-fg"
          >
            <X aria-hidden="true" className="size-3.5" />
          </BaseCombobox.Clear>
          <BaseCombobox.Trigger
            aria-label="Show options"
            className="inline-flex size-6 items-center justify-center rounded-sm text-fg-muted hover:bg-surface-hover hover:text-fg"
          >
            <ChevronsUpDown aria-hidden="true" className="size-4" />
          </BaseCombobox.Trigger>
        </span>
      </BaseCombobox.InputGroup>
      <BaseCombobox.Portal>
        <BaseCombobox.Positioner sideOffset={4} className="z-50 outline-none">
          <BaseCombobox.Popup className={cn(popupClass, 'w-(--anchor-width) max-w-(--available-width)')}>
            <BaseCombobox.Empty className="px-2 py-2 text-sm text-fg-muted empty:hidden">{emptyText}</BaseCombobox.Empty>
            <BaseCombobox.List className="max-h-[min(20rem,var(--available-height))] overflow-y-auto overscroll-contain">
              {(o: ComboboxOption<V>) => (
                <BaseCombobox.Item key={o.value} value={o} disabled={o.disabled} className={cn(optionClass, 'pr-3')}>
                  <span className="flex w-4 shrink-0 justify-center">
                    <BaseCombobox.ItemIndicator>
                      <Check aria-hidden="true" />
                    </BaseCombobox.ItemIndicator>
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate">{o.label}</span>
                    {o.description ? <span className="block text-xs text-fg-muted">{o.description}</span> : null}
                  </span>
                </BaseCombobox.Item>
              )}
            </BaseCombobox.List>
          </BaseCombobox.Popup>
        </BaseCombobox.Positioner>
      </BaseCombobox.Portal>
    </BaseCombobox.Root>
  );
}

/**
 * A labelled group of radio buttons over Base UI: pick exactly one of a few options that each
 * need a sentence of explanation (a route's fail mode, the fast-check backend). Arrow keys move
 * the choice; Tab leaves the group. For two to five short, equal options in a toolbar use
 * <SegmentedControl>.
 */
import { Radio } from '@base-ui/react/radio';
import { RadioGroup as BaseRadioGroup } from '@base-ui/react/radio-group';
import { useId, type ReactNode } from 'react';
import { cn } from '@/lib/cn';
import { CONTROL_BORDER } from './control-styles';

export interface RadioOption<V extends string = string> {
  value: V;
  label: ReactNode;
  description?: ReactNode;
  disabled?: boolean;
}

export interface RadioGroupProps<V extends string = string> {
  /** The group's visible name (a legend). */
  label: ReactNode;
  /** Help under the group label. */
  description?: ReactNode;
  options: readonly RadioOption<V>[];
  value?: V;
  defaultValue?: V;
  onValueChange?: (value: V) => void;
  disabled?: boolean;
  readOnly?: boolean;
  required?: boolean;
  name?: string;
  className?: string;
}

export function RadioGroup<V extends string = string>({
  label,
  description,
  options,
  value,
  defaultValue,
  onValueChange,
  disabled,
  readOnly,
  required,
  name,
  className,
}: RadioGroupProps<V>) {
  const id = useId();
  const labelId = `${id}-label`;
  const descriptionId = `${id}-description`;
  return (
    <BaseRadioGroup
      aria-labelledby={labelId}
      aria-describedby={description ? descriptionId : undefined}
      value={value}
      defaultValue={defaultValue}
      onValueChange={(next) => onValueChange?.(next as V)}
      disabled={disabled}
      readOnly={readOnly}
      required={required}
      name={name}
      className={cn('flex flex-col gap-2', className)}
    >
      <div className="space-y-0.5">
        <div id={labelId} className="text-sm font-medium text-fg">
          {label}
        </div>
        {description ? (
          <p id={descriptionId} className="text-xs text-fg-muted">
            {description}
          </p>
        ) : null}
      </div>
      {options.map((o, i) => {
        // By position, not value: a value may hold spaces, which would split the id reference.
        const optionDescriptionId = `${id}-option-${i}-description`;
        return (
          <div key={o.value} className="space-y-0.5">
            {/* The label wraps the radio, so a click on the words selects it. */}
            <label className="flex w-fit cursor-pointer items-start gap-2 text-sm text-fg has-data-disabled:cursor-not-allowed has-data-disabled:text-fg-muted">
              <Radio.Root
                value={o.value}
                disabled={o.disabled}
                aria-describedby={o.description ? optionDescriptionId : undefined}
                className={cn(
                  'mt-0.5 inline-flex size-4 shrink-0 items-center justify-center rounded-full border bg-surface',
                  CONTROL_BORDER,
                  'data-checked:border-fg data-disabled:opacity-50',
                )}
              >
                <Radio.Indicator className="size-2 rounded-full bg-fg data-unchecked:hidden" />
              </Radio.Root>
              <span>{o.label}</span>
            </label>
            {o.description ? (
              <p id={optionDescriptionId} className="pl-6 text-xs text-fg-muted">
                {o.description}
              </p>
            ) : null}
          </div>
        );
      })}
    </BaseRadioGroup>
  );
}

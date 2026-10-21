/**
 * Switch over Base UI with its visible label and an optional description. Use it for a setting
 * that takes effect at once ("Live"); a choice confirmed by a form submit is a <Checkbox>.
 */
import { Switch as BaseSwitch } from '@base-ui/react/switch';
import { useId, type ComponentProps } from 'react';
import { cn } from '@/lib/cn';
import { CONTROL_BORDER } from './control-styles';
import { Labelled, type LabelledProps } from './labelled';

export type SwitchProps = Omit<ComponentProps<typeof BaseSwitch.Root>, 'className' | 'children'> & LabelledProps;

export function Switch({ label, description, className, ...props }: SwitchProps) {
  const descriptionId = useId();
  return (
    <Labelled
      descriptionId={descriptionId}
      indentClass="pl-9"
      label={label}
      description={description}
      className={className}
      control={
        <BaseSwitch.Root
          aria-describedby={description ? descriptionId : undefined}
          className={cn(
            'relative mt-0.5 inline-flex h-4 w-7 shrink-0 items-center rounded-full border bg-surface-sunken p-px transition-colors',
            CONTROL_BORDER,
            'data-checked:border-fg data-checked:bg-fg data-disabled:opacity-50',
          )}
          {...props}
        >
          <BaseSwitch.Thumb className="size-3 rounded-full bg-fg-subtle transition-transform data-checked:translate-x-3 data-checked:bg-fg-inverse" />
        </BaseSwitch.Root>
      }
    />
  );
}

/**
 * Checkbox over Base UI with its visible label (clicking the words toggles it) and an optional
 * description wired to `aria-describedby`. Use it for a choice that takes effect when the form is
 * submitted ("I saved it"); a setting that applies at once is a <Switch>.
 *
 * The unchecked boundary reaches 3:1 on the surface (control-styles.ts); the checked state is the
 * inverted fg fill, so no hue is spent on chrome.
 */
import { Checkbox as BaseCheckbox } from '@base-ui/react/checkbox';
import { Check, Minus } from 'lucide-react';
import { useId, type ComponentProps } from 'react';
import { cn } from '@/lib/cn';
import { CONTROL_BORDER } from './control-styles';
import { Labelled, type LabelledProps } from './labelled';

export type CheckboxProps = Omit<ComponentProps<typeof BaseCheckbox.Root>, 'className' | 'children'> & LabelledProps;

export function Checkbox({ label, description, className, ...props }: CheckboxProps) {
  const descriptionId = useId();
  return (
    <Labelled
      descriptionId={descriptionId}
      indentClass="pl-6"
      label={label}
      description={description}
      className={className}
      control={
        <BaseCheckbox.Root
          aria-describedby={description ? descriptionId : undefined}
          className={cn(
            'mt-0.5 inline-flex size-4 shrink-0 items-center justify-center rounded-[4px] border bg-surface text-fg-inverse',
            CONTROL_BORDER,
            'data-checked:border-fg data-checked:bg-fg data-indeterminate:border-fg data-indeterminate:bg-fg',
            'data-disabled:opacity-50',
          )}
          {...props}
        >
          <BaseCheckbox.Indicator className="flex data-unchecked:not-data-indeterminate:hidden">
            {props.indeterminate ? (
              <Minus aria-hidden="true" className="size-3" strokeWidth={3} />
            ) : (
              <Check aria-hidden="true" className="size-3" strokeWidth={3} />
            )}
          </BaseCheckbox.Indicator>
        </BaseCheckbox.Root>
      }
    />
  );
}

/**
 * Text input and textarea over Base UI (they register with a surrounding <Field>, which supplies
 * the label, description and error). Numbers typed here are still shown back with `tabular`.
 */
import { Field as BaseField } from '@base-ui/react/field';
import { Input as BaseInput } from '@base-ui/react/input';
import type { ComponentProps } from 'react';
import { cn } from '@/lib/cn';
import { fieldControlClass } from './control-styles';

export type InputProps = Omit<ComponentProps<typeof BaseInput>, 'className'> & { className?: string };

export function Input({ className, ...props }: InputProps) {
  return <BaseInput className={cn(fieldControlClass, 'h-8', className)} {...props} />;
}

type TextareaOwnProps = Pick<
  ComponentProps<'textarea'>,
  'rows' | 'cols' | 'wrap' | 'maxLength' | 'minLength' | 'spellCheck' | 'autoComplete'
>;

export type TextareaProps = Omit<ComponentProps<typeof BaseField.Control>, 'className' | 'render' | 'type'> &
  TextareaOwnProps & { className?: string; mono?: boolean };

/** A multi-line text control. `mono` for code, JSON and YAML. */
export function Textarea({
  className,
  mono = false,
  rows = 4,
  cols,
  wrap,
  maxLength,
  minLength,
  spellCheck,
  autoComplete,
  ...props
}: TextareaProps) {
  return (
    <BaseField.Control
      className={cn(fieldControlClass, 'min-h-16 resize-y py-1.5', mono && 'font-mono text-xs', className)}
      render={
        <textarea
          rows={rows}
          cols={cols}
          wrap={wrap}
          maxLength={maxLength}
          minLength={minLength}
          spellCheck={spellCheck ?? !mono}
          autoComplete={autoComplete}
        />
      }
      {...props}
    />
  );
}

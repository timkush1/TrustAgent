/**
 * A labelled form field over Base UI Field: the label, the control (Input, Textarea, Select,
 * Combobox, Checkbox…), a description and an error, wired together (label `for`, and the
 * description and error in `aria-describedby`). `error` is the server's own message or a
 * validation message; passing one marks the control invalid.
 */
import { Field as BaseField } from '@base-ui/react/field';
import { CircleAlert } from 'lucide-react';
import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

export interface FieldProps {
  label: ReactNode;
  children: ReactNode;
  /** Help under the control: what the value does, its unit or its default. */
  description?: ReactNode;
  /** Error to show under the control; marks it invalid. */
  error?: ReactNode;
  /** Keep the label for screen readers only (the context already names the field). */
  hideLabel?: boolean;
  /** Adds "(optional)" to the label. Fields are required unless they say so. */
  optional?: boolean;
  disabled?: boolean;
  name?: string;
  className?: string;
}

export function Field({ label, children, description, error, hideLabel = false, optional = false, disabled, name, className }: FieldProps) {
  const invalid = error !== undefined && error !== null && error !== false && error !== '';
  return (
    <BaseField.Root invalid={invalid} disabled={disabled} name={name} className={cn('flex min-w-0 flex-col gap-1', className)}>
      <BaseField.Label className={cn('text-sm font-medium text-fg data-disabled:text-fg-muted', hideLabel && 'sr-only')}>
        {label}
        {optional ? ' ' : null}
        {optional ? <span className="font-normal text-fg-muted">(optional)</span> : null}
      </BaseField.Label>
      {children}
      {description ? <BaseField.Description className="text-xs text-fg-muted">{description}</BaseField.Description> : null}
      {invalid ? (
        <BaseField.Error match className="flex items-start gap-1 text-xs text-fg">
          <CircleAlert aria-hidden="true" className="mt-px size-3.5 shrink-0 text-tone-danger" />
          <span>{error}</span>
        </BaseField.Error>
      ) : null}
    </BaseField.Root>
  );
}

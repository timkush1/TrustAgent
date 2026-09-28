/**
 * The shared layout of a labelled toggle (Checkbox, Switch): the label wraps the control so a
 * click on the words toggles it, and the description sits under it, referenced by
 * `aria-describedby` on the control.
 */
import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

export interface LabelledProps {
  label: ReactNode;
  /** Help under the label, announced with the control. */
  description?: ReactNode;
  className?: string;
}

export function Labelled({
  control,
  label,
  description,
  descriptionId,
  indentClass,
  className,
}: LabelledProps & { control: ReactNode; descriptionId: string; indentClass: string }) {
  return (
    <div className={cn('space-y-0.5', className)}>
      {/* The label wraps the control, so a click on the words toggles it. Base UI renders the
          control (role checkbox or switch, plus a hidden input) inside. */}
      <label className="flex w-fit min-w-0 cursor-pointer items-start gap-2 text-sm text-fg has-data-disabled:cursor-not-allowed has-data-disabled:text-fg-muted">
        {control}
        <span className="min-w-0">{label}</span>
      </label>
      {description ? (
        <p id={descriptionId} className={cn('text-xs text-fg-muted', indentClass)}>
          {description}
        </p>
      ) : null}
    </div>
  );
}

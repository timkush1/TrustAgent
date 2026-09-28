/**
 * The one looping animation (DESIGN.md motion): a spinning circle for work in progress, such as
 * "Deep audit running". With `label` it is a polite status that screen readers announce; without
 * one it is decorative and the surrounding text must say what is happening. Reduced motion stops
 * the rotation (index.css), and the label still says it.
 */
import { LoaderCircle } from 'lucide-react';
import { cn } from '@/lib/cn';

export interface SpinnerProps {
  /** Announced text, e.g. "Loading claims". Omit when visible text already says it. */
  label?: string;
  size?: 'sm' | 'md' | 'lg';
  className?: string;
}

const sizeClass = { sm: 'size-3.5', md: 'size-4', lg: 'size-6' } as const;

export function Spinner({ label, size = 'md', className }: SpinnerProps) {
  const icon = <LoaderCircle aria-hidden="true" className={cn('shrink-0 animate-spin text-fg-muted', sizeClass[size], className)} />;
  if (!label) return icon;
  return (
    <span role="status" className="inline-flex items-center">
      {icon}
      <span className="sr-only">{label}</span>
    </span>
  );
}

import type { HTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

/** Placeholder block while content loads; decorative, so hidden from assistive tech. */
export function Skeleton({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div aria-hidden="true" className={cn('animate-pulse rounded-sm bg-surface-sunken', className)} {...props} />;
}

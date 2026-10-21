/**
 * A link that leaves the app (D102): a new tab with `rel="noopener noreferrer"`, so the new tab gets
 * no copy of this tab's sessionStorage and no handle on this window. Owned by WEB-LIVE (LV-6).
 */
import { ExternalLink as ExternalIcon } from 'lucide-react';
import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

export function ExternalLink({ href, children, className }: { href: string; children: ReactNode; className?: string }) {
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" className={cn('inline-flex items-center gap-1 font-medium text-fg underline underline-offset-2', className)}>
      {children}
      <ExternalIcon aria-hidden="true" className="size-3.5" />
      <span className="sr-only">(opens in a new tab)</span>
    </a>
  );
}

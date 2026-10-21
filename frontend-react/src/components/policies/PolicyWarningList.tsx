/**
 * Policy warnings as a list: each in plain words (policyWarnings.ts), with the server's own text in
 * a "Technical detail" disclosure for whoever edits the YAML.
 */
import { ChevronRight, TriangleAlert } from 'lucide-react';
import { cn } from '@/lib/cn';
import type { PolicyWarningView } from './policyWarnings';

export function PolicyWarningList({ warnings, label = 'Policy warnings', className }: { warnings: readonly PolicyWarningView[]; label?: string; className?: string }) {
  if (warnings.length === 0) return null;
  return (
    <ul aria-label={label} className={cn('space-y-2', className)}>
      {warnings.map((w, i) => (
        <li key={`${w.detail}-${i}`} className="flex items-start gap-2 text-sm">
          <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-warning" />
          <div className="min-w-0 space-y-1">
            <p className="text-fg">{w.text}</p>
            <details className="group/detail text-xs">
              <summary className="inline-flex cursor-pointer list-none items-center gap-1 rounded-sm font-medium text-fg-muted hover:text-fg [&::-webkit-details-marker]:hidden">
                <ChevronRight aria-hidden="true" className="size-3 transition-transform group-open/detail:rotate-90" />
                Technical detail
              </summary>
              <p className="mt-1 font-mono break-words text-fg-muted">{w.detail}</p>
            </details>
          </div>
        </li>
      ))}
    </ul>
  );
}

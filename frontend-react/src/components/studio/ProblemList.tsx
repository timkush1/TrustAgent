/**
 * The problems of a spec as text (SPEC §2.8 "live lints"): format errors, hard rules (errors, which
 * block saving) and lints (warnings, which only advise), each with its short title, the full message
 * and the field it concerns. The JSON tab adds the line and column.
 */
import { CircleAlert, CircleCheck, TriangleAlert } from 'lucide-react';
import { studioLintTitle } from '@/domain/presentation';
import { cn } from '@/lib/cn';
import type { SpecProblem } from '@/lib/evaluator/checkSpec';

export type ProblemItem = SpecProblem;

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

export function ProblemList({ items, id, className, cleanText = 'No problems: the check passes the format and every lint.' }: { items: readonly ProblemItem[]; id?: string; className?: string; cleanText?: string }) {
  const errors = items.filter((p) => p.severity === 'error').length;
  const warnings = items.length - errors;
  if (items.length === 0) {
    return (
      <p id={id} className={cn('flex items-center gap-1.5 text-sm text-fg', className)}>
        <CircleCheck aria-hidden="true" className="size-4 text-tone-success" />
        {cleanText}
      </p>
    );
  }
  return (
    <div id={id} className={cn('space-y-2', className)}>
      <p className="text-sm font-medium text-fg">
        {[errors > 0 ? plural(errors, 'error to fix before saving', 'errors to fix before saving') : null, warnings > 0 ? plural(warnings, 'suggestion', 'suggestions') : null]
          .filter(Boolean)
          .join(' · ')}
      </p>
      <ul className="space-y-1.5">
        {items.map((p, i) => {
          const Icon = p.severity === 'error' ? CircleAlert : TriangleAlert;
          return (
            <li key={`${p.code}-${p.path}-${i}`} className="flex items-start gap-2 text-sm">
              <Icon aria-hidden="true" className={cn('mt-0.5 size-4 shrink-0', p.severity === 'error' ? 'text-tone-danger' : 'text-tone-warning')} />
              <span className="min-w-0">
                <span className="sr-only">{p.severity === 'error' ? 'Error: ' : 'Suggestion: '}</span>
                <span className="font-medium text-fg">{studioLintTitle(p.code)}</span>
                <span className="text-fg-muted"> · {p.message}</span>
                {p.path || p.line ? (
                  <span className="block font-mono text-2xs text-fg-subtle">
                    {p.line ? `line ${p.line}${p.col ? `:${p.col}` : ''}` : null}
                    {p.line && p.path ? ' · ' : null}
                    {p.path}
                  </span>
                ) : null}
              </span>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

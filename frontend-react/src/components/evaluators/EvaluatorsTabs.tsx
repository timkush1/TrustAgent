/**
 * The Evaluators sections (SPEC §3.3: library, label queue, benchmarks, Studio) as one row of
 * links under the page header. Each Evaluators page renders it (there is no layout route); WEB-ST
 * renders it on the Studio pages. It is navigation, so it is a list of links with
 * `aria-current="page"` on the section shown, styled like the Tabs underline.
 */
import { Link } from '@tanstack/react-router';
import { BookOpen, FlaskConical, ListChecks, Sparkles, type LucideIcon } from 'lucide-react';
import { copy } from '@/domain/presentation';
import { cn } from '@/lib/cn';

export type EvaluatorsSection = 'library' | 'labels' | 'benchmarks' | 'studio';

const SECTIONS: { id: EvaluatorsSection; to: '/app/evaluators' | '/app/evaluators/labels' | '/app/evaluators/benchmarks' | '/app/evaluators/studio'; label: string; icon: LucideIcon }[] = [
  { id: 'library', to: '/app/evaluators', label: 'Library', icon: BookOpen },
  { id: 'labels', to: '/app/evaluators/labels', label: copy.labels.title, icon: ListChecks },
  { id: 'benchmarks', to: '/app/evaluators/benchmarks', label: 'Benchmarks', icon: FlaskConical },
  { id: 'studio', to: '/app/evaluators/studio', label: 'Studio', icon: Sparkles },
];

export function EvaluatorsTabs({ current, className }: { current: EvaluatorsSection; className?: string }) {
  return (
    <nav aria-label="Evaluators sections" className={cn('mb-4 border-b border-border', className)}>
      <ul className="flex flex-wrap items-center gap-1">
        {SECTIONS.map((s) => {
          const active = s.id === current;
          const Icon = s.icon;
          return (
            <li key={s.id}>
              <Link
                to={s.to}
                aria-current={active ? 'page' : undefined}
                className={cn(
                  'relative inline-flex h-9 items-center gap-1.5 rounded-sm px-2 text-sm font-medium whitespace-nowrap',
                  active ? 'text-fg after:absolute after:inset-x-0 after:-bottom-px after:h-0.5 after:rounded-full after:bg-fg' : 'text-fg-muted hover:text-fg',
                )}
              >
                <Icon aria-hidden="true" className="size-4 shrink-0" />
                {s.label}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}

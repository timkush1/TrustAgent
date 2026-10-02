/**
 * Where the viewer is in Studio's seven steps (SPEC §7.3): each step links to its section and says
 * in words whether it is done, to do, optional, or waiting on an earlier step (never color alone).
 */
import { CircleCheck, CircleDashed, CircleDot, Hourglass, type LucideIcon } from 'lucide-react';
import { cn } from '@/lib/cn';

export type StepStatus = 'done' | 'todo' | 'optional' | 'waiting';

const STEPS = [
  { id: 'step-describe', title: 'Describe' },
  { id: 'step-review', title: 'Review' },
  { id: 'step-test', title: 'Test' },
  { id: 'step-quick', title: 'Suggest a threshold' },
  { id: 'step-save', title: 'Save' },
  { id: 'step-watch', title: 'Watch on a route' },
  { id: 'step-calibrate', title: 'Calibrate' },
] as const;

const STATUS: Record<StepStatus, { label: string; icon: LucideIcon }> = {
  done: { label: 'Done', icon: CircleCheck },
  todo: { label: 'To do', icon: CircleDot },
  optional: { label: 'Optional', icon: CircleDashed },
  waiting: { label: 'Waiting on an earlier step', icon: Hourglass },
};

export function StudioSteps({ statuses }: { statuses: readonly StepStatus[] }) {
  return (
    <nav aria-label="Studio steps">
      <ol className="flex flex-wrap gap-x-4 gap-y-2">
        {STEPS.map((step, i) => {
          const status = STATUS[statuses[i] ?? 'waiting'];
          const Icon = status.icon;
          const waiting = statuses[i] === 'waiting';
          return (
            <li key={step.id}>
              {waiting ? (
                <span className="inline-flex items-center gap-1.5 text-xs text-fg-subtle">
                  <Icon aria-hidden="true" className="size-3.5" />
                  <span className="tabular">{i + 1}.</span> {step.title}
                  <span className="sr-only"> ({status.label})</span>
                </span>
              ) : (
                <a href={`#${step.id}`} className={cn('inline-flex items-center gap-1.5 rounded-sm text-xs hover:text-fg', statuses[i] === 'done' ? 'text-fg' : 'text-fg-muted')}>
                  <Icon aria-hidden="true" className="size-3.5" />
                  <span className="tabular">{i + 1}.</span> {step.title}
                  <span className="sr-only"> ({status.label})</span>
                </a>
              )}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}

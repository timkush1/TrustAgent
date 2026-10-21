/**
 * Where the viewer is in Studio's seven steps (SPEC §7.3): each step links to its section and says
 * in words whether it is done, to do, optional, or waiting on an earlier step (never color alone).
 * Each step is a numbered circle, as in the setup wizard's stepper: a tick once done, dashed when
 * optional, and dimmed while it waits on an earlier step (an hourglass read as "loading").
 */
import { Check } from 'lucide-react';
import { cn } from '@/lib/cn';

export type StepStatus = 'done' | 'todo' | 'optional' | 'waiting';

interface StudioStepLink {
  id: string;
  title: string;
}

/** The seven steps on a TrustAgent stack. */
const STUDIO_STEPS: readonly StudioStepLink[] = [
  { id: 'step-describe', title: 'Describe' },
  { id: 'step-review', title: 'Review' },
  { id: 'step-test', title: 'Test' },
  { id: 'step-quick', title: 'Suggest a threshold' },
  { id: 'step-save', title: 'Save' },
  { id: 'step-watch', title: 'Watch on a route' },
  { id: 'step-calibrate', title: 'Calibrate' },
];

/** The five steps of the online demo (D107): it exports the check instead of saving it. */
const STATIC_STUDIO_STEPS: readonly StudioStepLink[] = [...STUDIO_STEPS.slice(0, 4), { id: 'step-save', title: 'Export' }];

const STATUS_LABEL: Record<StepStatus, string> = {
  done: 'Done',
  todo: 'To do',
  optional: 'Optional',
  waiting: 'Waiting on an earlier step',
};

const MARKER: Record<StepStatus, string> = {
  done: 'border-fg text-fg',
  todo: 'border-fg text-fg',
  optional: 'border-dashed border-border-strong text-fg-muted',
  waiting: 'border-border text-fg-subtle',
};

function Marker({ n, status }: { n: number; status: StepStatus }) {
  return (
    <span aria-hidden="true" className={cn('flex size-5 shrink-0 items-center justify-center rounded-full border text-2xs tabular', MARKER[status])}>
      {status === 'done' ? <Check className="size-3" /> : n}
    </span>
  );
}

/** `exportOnly`: the online demo's five steps, which end in an export instead of a save (D107). */
export function StudioSteps({ statuses, exportOnly = false }: { statuses: readonly StepStatus[]; exportOnly?: boolean }) {
  const steps = exportOnly ? STATIC_STUDIO_STEPS : STUDIO_STEPS;
  return (
    <nav aria-label="Studio steps">
      <ol className="flex flex-wrap gap-x-4 gap-y-2">
        {steps.map((step, i) => {
          const status = statuses[i] ?? 'waiting';
          const inner = (
            <>
              <Marker n={i + 1} status={status} />
              <span className="sr-only">{`${i + 1}.`}</span> <span>{step.title}</span> <span className="sr-only">({STATUS_LABEL[status]})</span>
            </>
          );
          return (
            <li key={step.id}>
              {status === 'waiting' ? (
                <span className="inline-flex items-center gap-1.5 text-xs text-fg-subtle">{inner}</span>
              ) : (
                <a href={`#${step.id}`} className={cn('inline-flex items-center gap-1.5 rounded-sm text-xs hover:text-fg', status === 'done' ? 'text-fg' : 'text-fg-muted')}>
                  {inner}
                </a>
              )}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}

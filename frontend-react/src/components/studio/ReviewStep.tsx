/**
 * Studio step 2, Review (SPEC §7.3): the check in three views of the same spec (Simple, Advanced,
 * JSON), live lints under them, and, for an AI draft, the drafter's rationale and its suggestions to
 * split an over-broad check.
 */
import { Lightbulb } from 'lucide-react';
import { useState } from 'react';
import { Tab, TabPanel, Tabs, TabsList } from '@/components/ui/tabs';
import { specProblems, type CheckOptions, type SpecCheck } from '@/lib/evaluator/checkSpec';
import type { EvaluatorSpec, StudioDraftResponse } from '@/types/api';
import { AdvancedTab } from './AdvancedTab';
import { JsonTab } from './JsonTab';
import { ProblemList } from './ProblemList';
import { SimpleTab } from './SimpleTab';
import { StudioStep } from './StudioStep';

type ReviewTab = 'simple' | 'advanced' | 'json';

export interface ReviewStepProps {
  spec: EvaluatorSpec;
  onChange: (spec: EvaluatorSpec) => void;
  check: SpecCheck;
  checkOptions: CheckOptions;
  draft: StudioDraftResponse | null;
  /** How the check began ("Drafted by AI"), shown with the title. */
  origin: string | null;
  idLocked: boolean;
  disabled: boolean;
}

export function ReviewStep({ spec, onChange, check, checkOptions, draft, origin, idLocked, disabled }: ReviewStepProps) {
  const [tab, setTab] = useState<ReviewTab>('simple');
  const rationale = draft?.draft?.rationale;
  const splits = draft?.draft?.split_suggestions ?? [];
  return (
    <StudioStep
      n={2}
      id="step-review"
      title="Review the check"
      description={origin ? `${origin}. Change anything; the lints below update as you type.` : 'Change anything; the lints below update as you type.'}
    >
      {rationale || splits.length > 0 ? (
        <div className="space-y-1.5 rounded-md border border-border bg-surface-sunken p-3 text-sm">
          {rationale ? (
            <p className="flex items-start gap-1.5 text-fg">
              <Lightbulb aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-fg-muted" />
              <span>
                <span className="font-medium">Why the AI shaped it this way: </span>
                {rationale}
              </span>
            </p>
          ) : null}
          {splits.length > 0 ? (
            <div>
              <p className="font-medium text-fg">The AI suggests splitting it:</p>
              <ul className="list-disc space-y-0.5 pl-5 text-fg-muted">
                {splits.map((s) => (
                  <li key={s}>{s}</li>
                ))}
              </ul>
            </div>
          ) : null}
        </div>
      ) : null}
      <Tabs value={tab} onValueChange={(v) => setTab(v as ReviewTab)}>
        <TabsList aria-label="Ways to edit the check">
          <Tab value="simple">Simple</Tab>
          <Tab value="advanced">Advanced</Tab>
          <Tab value="json">JSON</Tab>
        </TabsList>
        <TabPanel value="simple">
          <SimpleTab spec={spec} onChange={onChange} disabled={disabled} onOpenJson={() => setTab('json')} />
        </TabPanel>
        <TabPanel value="advanced">
          <AdvancedTab spec={spec} onChange={onChange} idLocked={idLocked} disabled={disabled} />
        </TabPanel>
        <TabPanel value="json">
          <JsonTab spec={spec} onChange={onChange} options={checkOptions} disabled={disabled} />
        </TabPanel>
      </Tabs>
      {tab !== 'json' ? (
        <div className="border-t border-border pt-3">
          <h3 className="sr-only">Lints</h3>
          <ProblemList items={specProblems(check)} />
        </div>
      ) : null}
    </StudioStep>
  );
}

/**
 * Studio step 1, Describe (SPEC §7.3): "What should TrustAgent check?" with example chips and a
 * route, then **Draft with AI** (the judge drafts once; the button shows the model and "≈ $0.005"
 * as an Estimate, or Simulated with a mock judge) or **Start from a template**, which is the
 * primary start when no judge is configured. A draft is never saved or attached: it fills the
 * steps below, where the viewer reviews and tests it.
 */
import { Link } from '@tanstack/react-router';
import { useMutation } from '@tanstack/react-query';
import { CircleAlert, Sparkles } from 'lucide-react';
import { useState } from 'react';
import { useDataSource } from '@/app/services';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { describeError } from '@/components/common/describeError';
import { Button } from '@/components/ui/button';
import { Field } from '@/components/ui/field';
import { Textarea } from '@/components/ui/input';
import { Select } from '@/components/ui/select';
import { STUDIO_DRAFT_MAX_CHARS } from '@/data/control/studioPlane';
import { errorCodeHint, isApiError } from '@/data/errors';
import { formatCount } from '@/domain/format';
import { copy, judgeProviderLabel, studioLintTitle } from '@/domain/presentation';
import { evaluatorCategoryLabel } from '@/domain/controlPresentation';
import { draftSamples, draftToSpec } from '@/lib/studio/draftToSpec';
import { draftEstimate } from '@/lib/studio/estimate';
import { BLANK_TEMPLATE, blankSpec, specFromTemplate, STUDIO_TEMPLATES } from '@/lib/studio/templates';
import type { EvaluatorSpec, StudioDraftResponse, StudioSample, SystemStatus } from '@/types/api';
import { studioCostLabel } from './studioWords';
import { CostFigure } from './CostFigure';
import { StudioStep } from './StudioStep';

const DESCRIBE_EXAMPLES: readonly string[] = [
  'Flag answers that blame the customer for the problem they describe.',
  'Flag answers that promise a refund, discount or delivery date our policy does not allow.',
  'Flag answers that give medical, legal or financial advice instead of pointing to a professional.',
];

const ANY_ROUTE = '__any__';

export interface StudioStart {
  spec: EvaluatorSpec;
  samples: StudioSample[];
  draft: StudioDraftResponse | null;
  /** How the check began, in words ("Drafted by AI", "From the Refusal template"). */
  origin: string;
  route: string | null;
}

export interface DescribeStepProps {
  judge: SystemStatus['judge'] | null;
  routes: readonly string[];
  /** Evaluator ids already in the library (a new check's id avoids them). */
  taken: readonly string[];
  writeReason: Reason | null;
  /** Whether a check is already in the steps below (starting again replaces it). */
  hasCheck: boolean;
  onStart: (start: StudioStart) => void;
}

function judgeMissing(judge: SystemStatus['judge'] | null): boolean {
  if (!judge) return false;
  return !judge.provider || judge.provider === 'none' || judge.state === 'disabled';
}

function DraftOutcome({ result }: { result: StudioDraftResponse }) {
  const cost = studioCostLabel(result.cost_source);
  return (
    <div className="space-y-2 rounded-md border border-border bg-surface-sunken p-3 text-sm" role="status">
      {result.draft ? (
        <p className="text-fg">
          Drafted “{result.draft.name}” with {result.model}
          {result.attempts > 1 ? ` after ${formatCount(result.attempts)} attempts` : ''}. Review it below; nothing is saved yet.
        </p>
      ) : (
        <p className="flex items-start gap-1.5 text-fg">
          <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-danger" />
          The AI's draft still broke the evaluator rules after {formatCount(result.attempts)} {result.attempts === 1 ? 'attempt' : 'attempts'}. Reword the description and draft again, or start
          from a template.
        </p>
      )}
      {!result.draft && (result.errors.length > 0 || result.lints.length > 0) ? (
        <ul className="list-disc space-y-0.5 pl-5 text-fg-muted">
          {result.errors.map((e, i) => (
            <li key={`e${i}`}>
              {e.path ? <span className="font-mono text-2xs">{e.path} · </span> : null}
              {e.message}
            </li>
          ))}
          {result.lints
            .filter((l) => l.severity === 'error')
            .map((l, i) => (
              <li key={`l${i}`}>
                {studioLintTitle(l.code)} · {l.message}
              </li>
            ))}
        </ul>
      ) : null}
      <p className="flex flex-wrap items-center gap-1.5 text-xs text-fg-muted">
        Drafting cost
        {cost ? <CostFigure usd={result.cost_usd} label={cost.label} basis={cost.basis} subject="Cost of this draft" approximate={false} /> : <span>unknown</span>}
      </p>
      {result.cost_source === 'simulated' ? <p className="text-xs text-fg-muted">{copy.studio.draft_simulated}</p> : null}
    </div>
  );
}

export function DescribeStep({ judge, routes, taken, writeReason, hasCheck, onStart }: DescribeStepProps) {
  const ds = useDataSource();
  const [description, setDescription] = useState('');
  const [route, setRoute] = useState<string>(ANY_ROUTE);
  const [template, setTemplate] = useState<string>(STUDIO_TEMPLATES[0]?.ref ?? BLANK_TEMPLATE);
  const noJudge = judgeMissing(judge);
  const routeOrNull = route === ANY_ROUTE ? null : route;

  const draft = useMutation({
    mutationFn: () => ds.draftEvaluator({ description: description.trim(), ...(routeOrNull ? { route: routeOrNull } : {}) }),
    onSuccess: (result) => {
      if (!result.draft) return;
      onStart({
        spec: draftToSpec(result.draft, { taken }),
        samples: draftSamples(result.draft),
        draft: result,
        origin: 'Drafted by AI',
        route: routeOrNull,
      });
    },
  });

  const startFromTemplate = () => {
    const spec = template === BLANK_TEMPLATE ? blankSpec(taken) : specFromTemplate(template, taken);
    const name = STUDIO_TEMPLATES.find((t) => t.ref === template)?.name;
    onStart({ spec, samples: [], draft: null, origin: name ? `From the ${name} template` : 'A blank check', route: routeOrNull });
  };

  let draftReason: Reason | null = writeReason;
  if (!draftReason && noJudge) {
    draftReason = {
      text: 'No AI judge is set up to draft with',
      fix: (
        <Link to="/app/settings/$section" params={{ section: 'models' }} className="font-medium text-fg underline underline-offset-2">
          Choose one in Models
        </Link>
      ),
    };
  }
  if (!draftReason && description.trim().length < 10) draftReason = { text: 'Describe the check in a sentence first' };

  const estimate = draftEstimate({ provider: judge?.provider ?? null, model: judge?.model ?? null });
  const failure = draft.error ? describeError(draft.error) : null;
  const hint = draft.error ? errorCodeHint(draft.error) : null;
  const length = description.length;

  const templateOptions = [
    { value: BLANK_TEMPLATE, label: 'Blank Yes/No check', description: 'Write the question yourself' },
    ...STUDIO_TEMPLATES.map((t) => ({ value: t.ref, label: t.name, description: evaluatorCategoryLabel(t.category) })),
  ];

  return (
    <StudioStep
      n={1}
      id="step-describe"
      title={copy.studio.describe}
      description="Say what an answer must not do. The AI turns it into one question the fast check can answer on every request; nothing runs or saves until you choose."
    >
      <Field label="What should TrustAgent check?" description={`${formatCount(length)} of ${formatCount(STUDIO_DRAFT_MAX_CHARS)} characters`}>
        <Textarea
          value={description}
          onChange={(e) => setDescription(e.target.value.slice(0, STUDIO_DRAFT_MAX_CHARS))}
          rows={3}
          maxLength={STUDIO_DRAFT_MAX_CHARS}
          placeholder="Flag answers that…"
        />
      </Field>
      <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Examples">
        <span className="text-xs text-fg-muted">Examples:</span>
        {DESCRIBE_EXAMPLES.map((example) => (
          <Button key={example} size="sm" variant="ghost" className="h-auto min-h-7 py-1 text-left whitespace-normal" onClick={() => setDescription(example)}>
            {example}
          </Button>
        ))}
      </div>
      <Field label="For route" optional description="Drafting for a route lets the AI see what that route is for.">
        <Select
          value={route}
          onValueChange={(v) => setRoute(v ?? ANY_ROUTE)}
          options={[{ value: ANY_ROUTE, label: 'Any route' }, ...routes.map((r) => ({ value: r, label: r }))]}
        />
      </Field>

      <div className="flex flex-col gap-4 border-t border-border pt-4 lg:flex-row lg:items-start lg:justify-between">
        <div className="space-y-1.5">
          <DisabledAction reason={draftReason} layout="stacked">
            {(p) => (
              <Button variant={noJudge ? 'secondary' : 'primary'} pending={draft.isPending} onClick={() => draft.mutate()} {...p}>
                <Sparkles aria-hidden="true" />
                {hasCheck ? 'Draft again with AI' : 'Draft with AI'}
              </Button>
            )}
          </DisabledAction>
          {!noJudge ? (
            <p className="flex flex-wrap items-center gap-1.5 text-xs text-fg-muted">
              <span>
                {judge?.model ? `Uses ${judge.model}` : 'Uses the AI judge'}
                {judge?.provider ? ` (${judgeProviderLabel(judge.provider)})` : ''} ·
              </span>
              <CostFigure usd={estimate.usd} label={estimate.label} basis={estimate.basis} subject="Cost of one draft" />
            </p>
          ) : null}
        </div>
        <div className="flex flex-wrap items-end gap-2">
          <Field label="Start from a template" className="min-w-56">
            <Select value={template} onValueChange={(v) => setTemplate(v ?? BLANK_TEMPLATE)} options={templateOptions} />
          </Field>
          <Button variant={noJudge ? 'primary' : 'secondary'} onClick={startFromTemplate}>
            {hasCheck ? 'Start over from this template' : 'Use this template'}
          </Button>
        </div>
      </div>
      {hasCheck ? <p className="text-xs text-fg-muted">Starting again replaces the check below (nothing is saved yet).</p> : null}

      {draft.data ? <DraftOutcome result={draft.data} /> : null}
      {failure ? (
        <div role="alert" className="space-y-1 rounded-md border border-border p-3 text-sm">
          <p className="flex items-start gap-1.5 text-fg">
            <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-danger" />
            Couldn't draft: {failure.message}
          </p>
          {hint ? <p className="text-fg-muted">{hint}</p> : null}
          {isApiError(draft.error) && draft.error.code === 'no_judge' ? (
            <p className="text-fg-muted">You can still start from a template.</p>
          ) : null}
          {failure.code ? <p className="font-mono text-2xs text-fg-subtle">{failure.code}</p> : null}
        </div>
      ) : null}
    </StudioStep>
  );
}

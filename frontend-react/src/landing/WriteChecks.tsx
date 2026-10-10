/**
 * Section 4 (SPEC §8.1): write your own checks. The "clip" is the built-in Unapproved commitments
 * check from contracts/evaluators, shown as what a one-sentence rule becomes. Below it, the Studio's
 * four steps with what each costs (D116: steps and cost labels only, no invented results) and a link
 * into the Studio.
 */
import { ArrowRight } from 'lucide-react';
import { claim } from './claims';
import { COMMITMENTS } from './commitments';
import { STUDIO_HREF } from './links';
import { buttonClass } from './styles';
import { ClaimValue, Section, Ticked } from './ui';

/** Words of `text` in lines of at most `width` characters (a YAML `>-` scalar joins them with spaces). */
function fold(text: string, width: number): string[] {
  const lines: string[] = [];
  for (const word of text.split(/\s+/).filter(Boolean)) {
    const last = lines[lines.length - 1];
    if (last !== undefined && last.length + 1 + word.length <= width) lines[lines.length - 1] = `${last} ${word}`;
    else lines.push(word);
  }
  return lines;
}

/** The support-bot route of contracts/policies/example.policies.yaml attaching this check. */
const ROUTE_SNIPPET = `routes:
  - name: support-bot
    evaluators:
      - ref: ${COMMITMENTS.ref}
        params:
          allowed_commitments: >-
${fold(COMMITMENTS.exampleAllowed, 38)
  .map((line) => `            ${line}`)
  .join('\n')}`;

export function WriteChecks() {
  return (
    <Section
      id="checks"
      eyebrow="Write your own checks"
      title="Describe a check in plain English. Run it on every request."
      lead="An LLM drafts it once; Jev runs it on every request for a fraction of a cent. Test it, calibrate it with labels, and watch it in shadow before it touches users."
    >
      <figure>
        <div className="grid grid-cols-1 items-stretch gap-4 lg:grid-cols-[minmax(0,0.85fr)_auto_minmax(0,1.15fr)]">
          <div className="flex flex-col rounded-lg border border-border-strong bg-surface p-5 max-sm:p-4">
            <p className="text-xs font-medium text-fg-subtle">You write</p>
            <p className="mt-3 text-lg text-fg">“Flag any answer that promises a refund, credit or discount our policy doesn’t allow.”</p>
            <p className="mt-6 text-xs font-medium text-fg-subtle">Then attach it to a route, in policies.yaml or the dashboard</p>
            <pre className="mt-2 rounded-md border border-border bg-surface-sunken px-3 py-2.5 font-mono text-xs leading-5 whitespace-pre-wrap text-fg [overflow-wrap:anywhere]">
              {ROUTE_SNIPPET}
            </pre>
          </div>
          <div className="flex items-center justify-center text-fg-subtle" aria-hidden="true">
            <ArrowRight className="size-5 max-lg:rotate-90" />
          </div>
          <div className="rounded-lg border border-border-strong bg-surface p-5 max-sm:p-4">
            <p className="text-xs font-medium text-fg-subtle">It becomes one more question in the fast check</p>
            <p className="mt-3 font-semibold text-fg">{COMMITMENTS.name}</p>
            <p className="mt-1 text-sm text-fg-muted">
              <Ticked text={COMMITMENTS.instructions} />
            </p>
            <ul className="mt-4 grid grid-cols-1 gap-2 sm:grid-cols-2">
              {COMMITMENTS.options.map((o) => (
                <li
                  key={o.key}
                  className={`rounded-md border px-3 py-2 text-sm ${o.risky ? 'border-fg bg-surface-sunken text-fg' : 'border-border text-fg-muted'}`}
                >
                  <Ticked text={o.what} />
                  {o.risky ? <span className="mt-1 block text-xs font-medium text-fg">Counts as risk</span> : null}
                </li>
              ))}
            </ul>
          </div>
        </div>
        <figcaption className="mt-3 text-xs text-fg-muted">
          This check is the built-in “{COMMITMENTS.name}” as it ships in contracts/evaluators. Jev answers it with a
          probability for each option; your thresholds turn that into a flag or a block.
        </figcaption>
      </figure>
      <StudioSteps />
    </Section>
  );
}

/**
 * Describe → Draft → Test on samples → Watch in shadow: the Studio's steps and what each costs, as
 * labelled estimates. No step shows a result: there is none to show until you run it.
 */
function StudioSteps() {
  const draft = claim('studio_draft_cost_estimate');
  const perCheck = claim('fast_check_cost_per_check');
  const steps: { title: string; body: string; cost: React.ReactNode }[] = [
    { title: 'Describe', body: 'Say what to catch, in a sentence.', cost: <span className="text-fg-muted">No model call</span> },
    { title: 'Draft', body: 'An LLM turns it into a check: the question, its options and which ones count as risk.', cost: <ClaimValue claim={draft} strong={false} /> },
    {
      title: 'Test on samples',
      body: 'Run the draft on example requests and read each answer before you keep it.',
      // One sample is one check.
      cost: <ClaimValue claim={perCheck} strong={false} />,
    },
    { title: 'Watch in shadow', body: 'It answers on live traffic without acting, so you can compare before it flags anyone.', cost: <span className="text-fg-muted">Asked in the same fast-check call</span> },
  ];
  return (
    <div className="mt-12">
      <h3 className="text-lg font-semibold text-fg">In the Studio, four steps</h3>
      <ol className="mt-5 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4" aria-label="Studio steps">
        {steps.map((s, i) => (
          <li key={s.title} className="flex flex-col rounded-lg border border-border bg-surface p-4" data-chip-scope>
            <p className="flex items-center gap-2 text-sm font-semibold text-fg">
              <span className="font-mono text-xs text-fg-subtle">0{i + 1}</span>
              {s.title}
            </p>
            <p className="mt-1.5 text-sm text-fg-muted">{s.body}</p>
            <p className="mt-auto pt-3 text-xs">{s.cost}</p>
          </li>
        ))}
      </ol>
      <div className="mt-6 flex flex-wrap items-center gap-x-4 gap-y-2">
        <a href={STUDIO_HREF} className={buttonClass.secondary} data-studio-link>
          Open the Studio
          <ArrowRight aria-hidden="true" />
        </a>
        <span className="text-sm text-fg-muted">Drafting and testing use your own OpenRouter key; nothing runs until you ask.</span>
      </div>
    </div>
  );
}

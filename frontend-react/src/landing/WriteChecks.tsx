/**
 * Section 4 (SPEC §8.1): write your own checks. The "clip" is the built-in Unapproved commitments
 * check from contracts/evaluators, shown as what a one-sentence rule becomes. The Studio that drafts
 * such checks from plain English runs on your own stack (make demo); the pill says the online demo
 * does not have it yet.
 */
import { ArrowRight, Hourglass } from 'lucide-react';
import { COMMITMENTS } from './content';
import { Section } from './ui';

/** Contract text marks field names with backticks: show them as code. */
function Ticked({ text }: { text: string }) {
  return (
    <>
      {text.split('`').map((part, i) =>
        i % 2 === 1 ? (
          <code key={i} className="font-mono text-[0.85em] text-fg">
            {part}
          </code>
        ) : (
          <span key={i}>{part}</span>
        ),
      )}
    </>
  );
}

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
      lead="An LLM drafts it once; Jev runs it on every request for a fraction of a cent. Test it on your traffic, calibrate it with labels, and watch it in shadow before it touches users."
    >
      <figure>
        <div className="grid grid-cols-1 items-stretch gap-4 lg:grid-cols-[minmax(0,0.85fr)_auto_minmax(0,1.15fr)]">
          <div className="flex flex-col rounded-lg border border-border-strong bg-surface p-5 max-sm:p-4">
            <p className="text-xs font-medium text-fg-subtle">You write</p>
            <p className="mt-3 text-lg text-fg">“Flag any answer that promises a refund, credit or discount our policy doesn’t allow.”</p>
            <p className="mt-6 text-xs font-medium text-fg-subtle">Then attach it to a route, in policies.yaml or the dashboard</p>
            <pre className="mt-2 overflow-x-auto rounded-md border border-border bg-surface-sunken px-3 py-2.5 font-mono text-xs leading-5 text-fg">
              {ROUTE_SNIPPET}
            </pre>
            <p className="mt-auto pt-6">
              {/* Wraps on a phone, so it is a rounded box rather than a fixed-height pill. */}
              <span className="inline-flex min-h-6 items-center gap-1 rounded-xl border border-dashed border-border-strong px-2 py-0.5 text-xs font-medium text-fg-muted">
                <Hourglass aria-hidden="true" className="size-3.5 shrink-0" />
                <span>
                  Drafting in the Studio: in <code className="font-mono">make demo</code>, not in the online demo yet
                </span>
              </span>
            </p>
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
          The right-hand card is the built-in “{COMMITMENTS.name}” check as it ships in contracts/evaluators. Jev answers it with a
          probability for each option; your thresholds turn that into a flag or a block.
        </figcaption>
      </figure>
    </Section>
  );
}

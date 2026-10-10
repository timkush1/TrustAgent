/**
 * "What Jev was asked" (D116): the exact fast-check request for the hero's refund answer, from the
 * committed glassbox.gen.json (scripts/gen-landing-glassbox.ts runs the gate's own compile() over the
 * pack scenario; `--check` keeps it in step). It shows what the request carried as evidence, the
 * questions, and the raw body; the cost is the hero card's estimate, those tokens at the Jev price
 * (hero_card_cost_usd, checked by validate.mjs and landing.test). It shows no time: the replayed
 * trace has its own simulated one, and the hero gives the published latency. On the
 * static site it links to the same request on the replay, where "Show exact Jev request" opens the
 * glass box. Loaded after the first paint.
 */
import { ArrowRight, FileText } from 'lucide-react';
import { claim } from './claims';
import { REFUND } from './content';
import glassbox from './glassbox.gen.json';
import { demoLinkText } from './links';
import { refundTraceHref } from './refundTrace';
import { ClaimValue, Ticked } from './ui';

const count = new Intl.NumberFormat('en-US');

export function GlassBox() {
  const cost = claim('hero_card_cost_usd');
  const link = refundTraceHref();
  const calls = glassbox.calls === 1 ? '1 call' : `${glassbox.calls} calls`;
  const questions = glassbox.questions.length === 1 ? '1 question' : `${glassbox.questions.length} questions`;
  return (
    <figure className="mt-14 overflow-hidden rounded-lg border border-border-strong bg-surface" data-glassbox>
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-b border-border bg-surface-sunken px-5 py-3 max-sm:px-4">
        <figcaption className="text-sm font-semibold text-fg">What Jev was asked about the refund answer</figcaption>
        <p className="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-xs text-fg-muted" data-chip-scope>
          <span>
            {calls} · {questions} · an estimated {count.format(glassbox.est_tokens)} tokens ·
          </span>
          <ClaimValue claim={cost} strong={false} />
        </p>
      </div>
      <div className="grid grid-cols-1 gap-6 p-5 max-sm:p-4 lg:grid-cols-[minmax(0,0.9fr)_minmax(0,1.1fr)]">
        <div>
          <p className="text-xs font-medium text-fg-subtle">Checked against what the request already carried</p>
          <ul className="mt-2 space-y-2">
            {glassbox.evidence.map((e) => (
              <li key={e.id} className="rounded-md border border-border px-3 py-2 text-sm">
                <p className="flex items-center gap-1.5 font-mono text-xs text-fg-muted">
                  <FileText aria-hidden="true" className="size-3.5" />
                  {e.id}
                </p>
                <p className="mt-1 text-fg">{e.text}</p>
              </li>
            ))}
          </ul>
          <p className="mt-3 text-xs text-fg-subtle">and the answer: “{REFUND.answer}”</p>
        </div>
        <div>
          <p className="text-xs font-medium text-fg-subtle">The questions, all in one call to {glassbox.model}</p>
          <ol className="mt-2 space-y-2">
            {glassbox.questions.map((q) => (
              <li key={q.id} className="rounded-md border border-border px-3 py-2 text-sm">
                <p className="font-medium text-fg">{q.name}</p>
                <p className="mt-0.5 text-fg-muted">
                  <Ticked text={q.asks} />
                </p>
              </li>
            ))}
          </ol>
        </div>
      </div>
      <details className="group border-t border-border">
        <summary className="cursor-pointer list-none px-5 py-3 text-sm font-medium text-fg marker:hidden max-sm:px-4">
          <span className="inline-flex items-center gap-1.5">
            <ArrowRight aria-hidden="true" className="size-4 text-fg-muted transition-transform group-open:rotate-90" />
            The exact request body
          </span>
        </summary>
        <pre className="max-h-96 overflow-auto border-t border-border bg-surface-sunken px-5 py-4 font-mono text-xs leading-5 text-fg max-sm:px-4">
          {JSON.stringify(glassbox.requests.length === 1 ? glassbox.requests[0] : glassbox.requests, null, 2)}
        </pre>
      </details>
      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border px-5 py-3 text-sm max-sm:px-4">
        <span className="text-xs text-fg-muted">Compiled by the gateway’s own request builder from the demo’s refund scenario.</span>
        <a href={link.href} className="inline-flex items-center gap-1 font-medium text-fg underline underline-offset-2" data-glassbox-link>
          {link.exact ? 'See this request on the replay' : demoLinkText()}
          <ArrowRight aria-hidden="true" className="size-4" />
        </a>
      </div>
    </figure>
  );
}

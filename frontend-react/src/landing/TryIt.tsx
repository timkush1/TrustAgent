/**
 * Section 5 (SPEC §8.1): try it. The replay tape is the primary way in (its label says what it is:
 * simulated or recorded, when, and whose answers); trying it live in the browser with your own
 * OpenRouter key arrives in P5 and is shown as coming soon, with no control that pretends to work.
 */
import { ArrowRight, CircleCheck, CircleMinus, Flag, Hourglass, type LucideIcon } from 'lucide-react';
import { useEffect, useState } from 'react';
import { command, copy, DEMO_HREF, outcome, RECORDING_URL } from './content';
import { Command, Section } from './ui';
import { buttonClass } from './styles';
import outcomesDoc from '@contracts/ui/outcomes.v1.json';

interface TapeTrace {
  trace_id: string;
  created_at: string;
  gate_status: string;
  action: string | null;
  would_action: string | null;
  answer_source: string | null;
  question_preview: string | null;
  response_preview: string | null;
}

interface Tape {
  kind: 'recorded' | 'mock';
  label: string;
  recorded_at: string;
  traces: TapeTrace[];
}

type TapeState = { status: 'loading' } | { status: 'error'; message: string } | { status: 'ready'; tape: Tape };

/** The first matching rule of ui/outcomes.v1.json, as the dashboard decides it. */
function outcomeId(t: TapeTrace): string {
  const fields: Record<string, string | null> = { gate_status: t.gate_status, action: t.action, would_action: t.would_action };
  for (const rule of outcomesDoc.rules) {
    if (Object.entries(rule.when).every(([k, list]) => (list as string[]).includes(fields[k] ?? ''))) return rule.outcome;
  }
  return 'passed';
}

const toneVar: Record<string, string> = {
  success: '--tone-success',
  warning: '--tone-warning',
  danger: '--tone-danger',
  info: '--tone-info',
  neutral: '--tone-neutral',
};
const outcomeIcon: Record<string, LucideIcon> = { passed: CircleCheck, flagged: Flag, not_checked: CircleMinus };

function Pill({ id }: { id: string }) {
  const o = outcome(id);
  const Icon = outcomeIcon[id] ?? CircleMinus;
  const neutral = o.tone === 'neutral';
  return (
    <span
      style={{ '--pill': `var(${toneVar[o.tone] ?? '--tone-neutral'})` } as React.CSSProperties}
      className={`inline-flex h-6 shrink-0 items-center gap-1 rounded-full border px-2 text-xs font-medium ${
        neutral
          ? 'border-border bg-surface-sunken text-fg-muted'
          : 'border-[color-mix(in_oklab,var(--pill)_40%,transparent)] bg-[color-mix(in_oklab,var(--pill)_var(--tint),transparent)] text-(--pill)'
      }`}
    >
      <Icon aria-hidden="true" className="size-3.5" />
      {o.label}
    </span>
  );
}

function useTape(): TapeState {
  const [state, setState] = useState<TapeState>({ status: 'loading' });
  useEffect(() => {
    const ctrl = new AbortController();
    fetch(RECORDING_URL, { signal: ctrl.signal, headers: { Accept: 'application/json' } })
      .then(async (res) => {
        if (!res.ok || !(res.headers.get('content-type') ?? '').includes('json')) throw new Error(`HTTP ${res.status}`);
        const doc = (await res.json()) as Tape;
        if (!Array.isArray(doc.traces) || typeof doc.label !== 'string') throw new Error('not a recording');
        setState({ status: 'ready', tape: doc });
      })
      .catch((err: unknown) => {
        if (!ctrl.signal.aborted) setState({ status: 'error', message: err instanceof Error ? err.message : String(err) });
      });
    return () => ctrl.abort();
  }, []);
  return state;
}

const date = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' });

function TapeCard() {
  const state = useTape();
  let body;
  if (state.status === 'loading') {
    body = (
      <div aria-busy="true" className="space-y-3">
        <span className="sr-only" role="status">
          Loading the recording…
        </span>
        {[0, 1, 2].map((i) => (
          <div key={i} className="h-9 rounded-md bg-surface-sunken" />
        ))}
      </div>
    );
  } else if (state.status === 'error') {
    body = <p className="text-sm text-fg-muted">The recording could not be loaded here ({state.message}); the replay page shows it too.</p>;
  } else {
    const { tape } = state;
    const examples = new Map<string, TapeTrace>();
    for (const t of tape.traces) examples.set(`${t.question_preview}\u0000${t.response_preview}`, t);
    const counts = new Map<string, number>();
    for (const t of tape.traces) counts.set(outcomeId(t), (counts.get(outcomeId(t)) ?? 0) + 1);
    const sources = [...new Set(tape.traces.map((t) => t.answer_source).filter(Boolean))].join(' and ') || 'unlabelled';
    body = (
      <>
        <p className="text-sm text-fg" data-recording-label>
          {tape.label}
        </p>
        <p className="mt-1 text-xs text-fg-muted">
          {tape.kind === 'mock' ? 'Recorded from the zero-key demo stack' : 'Recorded'} on {date.format(Date.parse(tape.recorded_at))} ·{' '}
          {tape.traces.length} requests · {sources} answers ·{' '}
          {[...counts.entries()].map(([id, n]) => `${n} ${outcome(id).label.toLowerCase()}`).join(', ')}
        </p>
        <ul className="mt-5 space-y-2" aria-label="Requests on the tape">
          {[...examples.values()].map((t) => (
            <li key={t.trace_id} className="flex items-center gap-3 rounded-md border border-border px-3 py-2">
              <Pill id={outcomeId(t)} />
              <span className="min-w-0 truncate text-sm text-fg-muted">{t.question_preview}</span>
            </li>
          ))}
        </ul>
      </>
    );
  }
  return (
    <div className="flex flex-col rounded-lg border border-border-strong bg-surface p-6 max-sm:p-4">
      <div className="mb-4 flex items-center justify-between gap-3">
        <h3 className="text-lg font-semibold text-fg">The replay</h3>
        <span className="text-xs text-fg-subtle">replayed, not live</span>
      </div>
      {body}
      <div className="mt-auto pt-6">
        <a href={DEMO_HREF} className={buttonClass.primary}>
          Open the replay
          <ArrowRight aria-hidden="true" />
        </a>
      </div>
    </div>
  );
}

const liveScenarios = ['A grounded answer that passes', 'A refund answer that says 60 days when the policy says 30', 'An answer that leaks a customer’s personal data'];

export function TryIt() {
  return (
    <Section
      id="try"
      eyebrow="Try it"
      title="Watch it catch the refund answer."
      lead="Open the dashboard on recorded traffic: every request, what it was checked against, and the claim the deep audit found contradicted."
    >
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <TapeCard />
        <div className="flex flex-col rounded-lg border border-dashed border-border-strong p-6 max-sm:p-4">
          <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
            <h3 className="text-lg font-semibold text-fg">Try it live in your browser</h3>
            <span className="inline-flex h-6 items-center gap-1 rounded-full border border-dashed border-border-strong px-2 text-xs font-medium text-fg-muted">
              <Hourglass aria-hidden="true" className="size-3.5" />
              Coming soon
            </span>
          </div>
          <p className="text-sm text-fg-muted">Connect OpenRouter and run three one-click examples, each with its documents:</p>
          <ul className="mt-3 space-y-1.5 text-sm text-fg">
            {liveScenarios.map((s) => (
              <li key={s} className="flex gap-2">
                <span aria-hidden="true" className="mt-2 size-1 shrink-0 rounded-full bg-fg-subtle" />
                {s}
              </li>
            ))}
          </ul>
          <p className="mt-4 text-sm text-fg-muted">{copy.playground_static.consent[0]}</p>
          <div className="mt-auto space-y-2 pt-6">
            <p className="text-sm text-fg">Until then, the same checks run on your machine with no keys:</p>
            <Command command={command('demo')} />
          </div>
        </div>
      </div>
    </Section>
  );
}

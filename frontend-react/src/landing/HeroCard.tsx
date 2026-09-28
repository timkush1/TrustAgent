/**
 * The hero's answer card (SPEC §8.1), in three beats: the answer arrives; "Flagged · 212 ms ·
 * $0.0002"; "60 days" gets a wavy underline next to the evidence "…within 30 days of purchase"
 * (refund-policy.md). The words are the scenario pack's refund example; the time and cost are the
 * `hero_card_*` claims, labelled Simulated. With reduced motion the card shows its last beat at once.
 */
import { Flag, OctagonX, RotateCcw } from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import { claim } from './claims';
import { copy, outcome, REFUND } from './content';
import { LabelChip } from './ui';

const latency = claim('hero_card_latency_ms');
const cost = claim('hero_card_cost_usd');
const flagged = outcome('flagged');

const words = REFUND.answer.split(/(?<=\s)/);
const quoteAt = REFUND.answer.indexOf(REFUND.quote);
const evidenceAt = REFUND.evidence.indexOf(REFUND.evidenceNumber);

function prefersReducedMotion(): boolean {
  return typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
}

/** 0 question only · 1 answer streaming · 2 flagged · 3 underline and evidence (done). */
function useBeats() {
  const reduced = prefersReducedMotion();
  const [beat, setBeat] = useState(reduced ? 3 : 0);
  const [shown, setShown] = useState(reduced ? words.length : 0);
  const timers = useRef<number[]>([]);

  const clear = useCallback(() => {
    for (const t of timers.current) window.clearTimeout(t);
    timers.current = [];
  }, []);

  /** Schedules the beats from the start; state changes only in the timers' callbacks. */
  const schedule = useCallback(() => {
    clear();
    if (prefersReducedMotion()) return;
    const at = (ms: number, fn: () => void) => timers.current.push(window.setTimeout(fn, ms));
    const start = 500;
    const perWord = 70;
    at(start, () => setBeat(1));
    words.forEach((_, i) => at(start + i * perWord, () => setShown(i + 1)));
    const answered = start + words.length * perWord;
    at(answered + 550, () => setBeat(2));
    at(answered + 1400, () => setBeat(3));
  }, [clear]);

  useEffect(() => {
    schedule();
    return clear;
  }, [schedule, clear]);

  const play = useCallback(() => {
    if (prefersReducedMotion()) return;
    setBeat(0);
    setShown(0);
    schedule();
  }, [schedule]);

  return { beat, shown, play };
}

function Answer({ shown, marked }: { shown: number; marked: boolean }) {
  const visible = words.slice(0, shown).join('');
  if (quoteAt < 0) return <>{visible}</>;
  const before = visible.slice(0, quoteAt);
  const quote = visible.slice(quoteAt, quoteAt + REFUND.quote.length);
  const after = visible.slice(quoteAt + REFUND.quote.length);
  return (
    <>
      {before}
      {quote ? (
        <mark className="hero-mark" data-on={marked ? '' : undefined}>
          {quote}
        </mark>
      ) : null}
      {after}
    </>
  );
}

export function HeroCard() {
  const { beat, shown, play } = useBeats();
  const on = (n: number) => (beat >= n ? '' : undefined);
  return (
    <figure className="w-full">
      <div className="overflow-hidden rounded-lg border border-border-strong bg-surface">
        <div className="flex items-center justify-between gap-3 border-b border-border bg-surface-sunken px-4 py-2.5">
          <span className="truncate font-mono text-xs text-fg-muted">
            {REFUND.route} · {REFUND.model}
          </span>
          <span className="text-xs text-fg-subtle">Illustration</span>
        </div>

        <div aria-hidden="true" className="space-y-5 px-5 py-5 max-sm:px-4">
          <div className="space-y-1.5">
            <p className="text-xs font-medium text-fg-subtle">User</p>
            <p className="rounded-md bg-surface-sunken px-3 py-2 text-sm text-fg">{REFUND.question}</p>
          </div>
          <div className="min-h-[4.5rem] space-y-1.5">
            <p className="text-xs font-medium text-fg-subtle">Assistant</p>
            <p className="text-lg text-fg">
              <Answer shown={shown} marked={beat >= 3} />
              {beat === 1 && shown < words.length ? <span className="ml-0.5 inline-block h-4 w-px translate-y-0.5 bg-fg-muted" /> : null}
            </p>
          </div>
        </div>

        <div className="beat border-t border-border px-5 py-4 max-sm:px-4" data-on={on(2)} aria-hidden={beat < 2}>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
            <span className="inline-flex h-7 items-center gap-1.5 rounded-full border border-[color-mix(in_oklab,var(--tone-warning)_40%,transparent)] bg-[color-mix(in_oklab,var(--tone-warning)_var(--tint),transparent)] px-2.5 text-sm font-medium text-tone-warning">
              <Flag aria-hidden="true" className="size-3.5" />
              {flagged.label}
            </span>
            <span className="tabular text-sm text-fg-muted">
              {latency.display} · {cost.display}
            </span>
            <LabelChip claim={latency} />
          </div>
          <p className="mt-2 text-sm text-fg-muted">
            {copy.tiers.fast_check}: {REFUND.reason.toLowerCase()}.
          </p>
        </div>

        <div className="beat border-t border-border bg-surface-sunken px-5 py-4 max-sm:px-4" data-on={on(3)} aria-hidden={beat < 3}>
          <p className="flex items-center gap-1.5 text-xs font-medium text-fg-muted">
            <OctagonX aria-hidden="true" className="size-3.5 text-v-contradicted" />
            <span className="text-v-contradicted">Contradicted</span>
            <span>by</span>
            <span className="font-mono text-fg">{REFUND.docId}</span>
          </p>
          <p className="mt-1.5 text-sm text-fg">
            “
            {evidenceAt >= 0 ? (
              <>
                {REFUND.evidence.slice(0, evidenceAt)}
                <span className="evidence-quote font-medium">{REFUND.evidenceNumber}</span>
                {REFUND.evidence.slice(evidenceAt + REFUND.evidenceNumber.length)}
              </>
            ) : (
              REFUND.evidence
            )}
            ”
          </p>
        </div>
      </div>
      <figcaption className="mt-3 flex flex-wrap items-center justify-between gap-2 text-xs text-fg-muted">
        <span>
          <span className="sr-only">
            An assistant answered “{REFUND.answer}” It was flagged: {REFUND.quote} contradicts {REFUND.docId}, which says “{REFUND.evidence}”
          </span>
          The scripted refund example from the demo: the policy says 30 days, the answer says 60.
        </span>
        <button
          type="button"
          onClick={play}
          disabled={beat < 3}
          className="inline-flex items-center gap-1 rounded-sm px-1.5 py-1 font-medium text-fg-muted transition-colors enabled:hover:text-fg disabled:invisible"
        >
          <RotateCcw aria-hidden="true" className="size-3.5" />
          Play again
        </button>
      </figcaption>
    </figure>
  );
}

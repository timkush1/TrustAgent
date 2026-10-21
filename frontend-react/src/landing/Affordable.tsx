/**
 * Section 3 (SPEC §8.1): why it is affordable. Three labelled cost lines (plus the full audit they
 * are compared with), the published third-party and vendor figures with their caveats, our own
 * measurements (or "not yet measured"), and a calculator over the same cost model.
 */
import { lazy, Suspense } from 'react';
import { claim, optionalClaim, type ClaimView } from './claims';
import { loaders } from './lazyLoaders';
import { PartBoundary } from './lazyParts';
import { ClaimCaveat, ClaimValue, LabelChip, Section } from './ui';

// The calculator (and the pricing model behind it) loads right after the page, below the fold:
// it is interactive only, and keeps the first load inside the landing budget. It is one of the
// landing's lazy parts, so the hash scroll waits for it, and a chunk that fails to load says so in
// its place instead of taking the page down.
const Calculator = lazy(() => loaders.calculator().then((m) => ({ default: m.Calculator })));

function CalculatorPlaceholder() {
  return (
    <div data-lazy-placeholder className="mt-16 min-h-[26rem] rounded-lg border border-border-strong bg-surface" aria-busy="true">
      <span className="sr-only" role="status">
        Loading the cost calculator…
      </span>
    </div>
  );
}

const costLines: { claim: ClaimView; ours: boolean }[] = [
  { claim: claim('fast_check_cost_per_1k'), ours: true },
  { claim: claim('default_total_cost_per_1k'), ours: true },
  { claim: claim('llm_same_checks_cost_per_1k'), ours: false },
  { claim: claim('llm_full_audit_cost_per_1k'), ours: false },
];

function CostBars() {
  const max = Math.max(...costLines.map((l) => l.claim.value ?? 0));
  return (
    <figure>
      <figcaption className="mb-5 flex items-baseline justify-between gap-3">
        <span className="text-sm font-semibold text-fg">Cost per 1,000 answers</span>
        <span className="text-xs text-fg-muted">Longer is more expensive</span>
      </figcaption>
      <ul className="space-y-5">
        {costLines.map(({ claim: c, ours }) => {
          const value = c.value ?? 0;
          return (
            <li key={c.id} data-chip-scope>
              <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                <span className={ours ? 'text-fg' : 'text-fg-muted'}>{c.text}</span>
                <ClaimValue claim={c} />
              </div>
              <div className="mt-2 h-2 w-full rounded-full bg-surface-sunken" aria-hidden="true">
                <div
                  className={`h-2 rounded-full ${ours ? 'bg-fg' : 'bg-fg-subtle'}`}
                  style={{ width: `max(${((value / max) * 100).toFixed(2)}%, 3px)` }}
                />
              </div>
            </li>
          );
        })}
      </ul>
    </figure>
  );
}

function Figure({ title, chip, children }: { title: string; chip: ClaimView; children: React.ReactNode }) {
  return (
    <li className="border-t border-border py-4 first:border-t-0 first:pt-0" data-chip-scope>
      <div className="flex items-center justify-between gap-3">
        <p className="text-sm font-semibold text-fg">{title}</p>
        <LabelChip claim={chip} />
      </div>
      <div className="mt-1.5 text-fg-muted">{children}</div>
      <ClaimCaveat claim={chip} className="mt-1.5" />
    </li>
  );
}

/** "a", "a and b", "a, b and c". */
const listWords = (items: string[]) => (items.length < 2 ? items.join('') : `${items.slice(0, -1).join(', ')} and ${items[items.length - 1]}`);

/** "Our fast-check latency" → "our fast-check latency"; an acronym ("RAGTruth") keeps its case. */
const lowerFirst = (t: string) => (/^[A-Z][a-z]/.test(t) ? t[0].toLowerCase() + t.slice(1) : t);

/**
 * Why Arize's Jev price per 1,000 is lower than the fast check's: its RAGTruth prompts are shorter
 * than the request the fast-check figure assumes per answer, documents, questions and answer
 * together (pricing/assumptions.v1.json fast_check_input_tokens; a test keeps them in step).
 */
const FAST_CHECK_TOKENS_WORDS = '5,000';
const arizeTokensNote = (fastCheck: ClaimView) =>
  `Its prompts are shorter: the fast check’s ${fastCheck.display} assumes about ${FAST_CHECK_TOKENS_WORDS} tokens per request.`;

const num = (c: ClaimView) => <span className="tabular font-medium text-fg">{c.display}</span>;

function PublishedFigures() {
  const jevIn = claim('jev_input_price_per_mtok');
  const jevOut = claim('jev_output_price_per_mtok');
  const arizeJev = claim('arize_ragtruth_jev_accuracy');
  const arizeOpus = claim('arize_ragtruth_opus_accuracy');
  const arizeJevCost = claim('arize_ragtruth_jev_cost_per_1k');
  const arizeOpusCost = claim('arize_ragtruth_opus_cost_per_1k');
  const threshold = claim('arize_ragtruth_tuned_threshold');
  const atHalf = claim('arize_ragtruth_accuracy_at_default_threshold');
  // OpenRouter's own test: shown while the claims contract keeps it (its figures are re-checked
  // against the source before launch, and withdrawn if the page no longer publishes them).
  const orAcc = optionalClaim('openrouter_faithfulness_accuracy');
  const orMs = optionalClaim('openrouter_faithfulness_median_ms');
  const measured = [claim('fast_check_cost_per_1k_measured'), claim('gate_latency_p50_measured'), claim('ragtruth_qa_agreement_measured')];
  // The only place on the page that names what is not measured yet: the hero shows our figure only
  // once it is published.
  const published = measured.filter((c) => c.hasValue);
  const pending = measured.filter((c) => !c.hasValue);
  return (
    <ul>
      <Figure title="Jev price" chip={jevIn}>
        {num(jevIn)}, {num(jevOut)}.
      </Figure>
      <Figure title="Arize RAGTruth test" chip={arizeJev}>
        Accuracy: Jev {num(arizeJev)} at {num(arizeJevCost)} vs Claude Opus 5 {num(arizeOpus)} at {num(arizeOpusCost)}, only at a tuned{' '}
        {num(threshold)} threshold ({num(atHalf)}). <span data-arize-tokens>{arizeTokensNote(claim('fast_check_cost_per_1k'))}</span>
      </Figure>
      {orAcc && orMs ? (
        <Figure title="OpenRouter faithfulness test" chip={orAcc}>
          {num(orAcc)} accuracy, {num(orMs)} on its own test set (not the fast check’s P50 above).
        </Figure>
      ) : null}
      <li className="border-t border-border pt-4" data-measured>
        <p className="text-sm font-semibold text-fg">Our measurements</p>
        {published.length > 0 ? (
          <ul className="mt-1.5 space-y-1 text-fg-muted">
            {published.map((c) => (
              <li key={c.id} className="flex flex-wrap items-baseline gap-x-2" data-chip-scope>
                <span>{c.text}:</span>
                <ClaimValue claim={c} />
              </li>
            ))}
          </ul>
        ) : null}
        {pending.length > 0 ? (
          <p className="mt-1.5 text-fg-muted">
            {published.length > 0 ? 'Still to publish' : 'Not published yet'}: {listWords(pending.map((c) => lowerFirst(c.text)))}.
          </p>
        ) : null}
        <p className="mt-1.5 text-xs text-fg-subtle">Published only from a committed benchmark run, never from the demo.</p>
      </li>
    </ul>
  );
}

export function Affordable() {
  return (
    <Section
      id="cost"
      eyebrow="Why it’s affordable"
      title="Check every answer for a fraction of a cent. Audit only the risky ones."
      lead="A fast check on every answer; a claim-by-claim audit only for flagged answers and a small sample. Every figure says where it comes from."
    >
      <div className="grid grid-cols-1 gap-x-16 gap-y-12 lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)]">
        <CostBars />
        <PublishedFigures />
      </div>
      <PartBoundary>
        <Suspense fallback={<CalculatorPlaceholder />}>
          <Calculator />
        </Suspense>
      </PartBoundary>
    </Section>
  );
}

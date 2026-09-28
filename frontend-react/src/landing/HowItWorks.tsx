/** Section 2 (SPEC §8.1): three plain steps, the request path, and who it is for. */
import { ArrowDown, ArrowRight, FileText, Gauge, ScanSearch } from 'lucide-react';
import type { ReactNode } from 'react';
import { copy } from './content';
import { Section } from './ui';

const steps: { icon: typeof FileText; title: string; body: ReactNode }[] = [
  {
    icon: FileText,
    title: 'Uses the evidence you already have',
    body: 'System-prompt documents, attached context and tool results. No vector database required: without documents an answer is marked “not checked”, never guessed.',
  },
  {
    icon: Gauge,
    title: copy.tiers.fast_check,
    body: 'One call answers all your checks at once (groundedness, contradiction, refusals, PII, promises) as probabilities. Your policy observes, flags, blocks or falls back, and blocks only when two signals agree.',
  },
  {
    icon: ScanSearch,
    title: copy.tiers.deep_audit,
    body: 'An LLM judge explains flagged and sampled answers claim by claim, streamed to your dashboard.',
  },
];

function Box({ title, children, strong = false }: { title: string; children?: ReactNode; strong?: boolean }) {
  return (
    <div className={`rounded-lg border bg-surface p-4 ${strong ? 'border-fg' : 'border-border-strong'}`}>
      <p className="text-sm font-semibold text-fg">{title}</p>
      {children}
    </div>
  );
}

function Arrow({ label }: { label: string }) {
  return (
    <div className="flex items-center justify-center text-fg-subtle lg:flex-col" aria-hidden="true">
      <span className="text-xs max-lg:mr-2 lg:mb-1">{label}</span>
      <ArrowRight className="size-4 max-lg:rotate-90" />
    </div>
  );
}

/** The request path: app → gateway (instant checks, fast check, policy) → provider; audits on the side. */
function PathDiagram() {
  return (
    <figure className="mb-14">
      <div className="grid grid-cols-1 items-center gap-3 lg:grid-cols-[minmax(0,1fr)_auto_minmax(0,1.6fr)_auto_minmax(0,1fr)]">
        <Box title="Your app">
          <p className="mt-1 text-sm text-fg-muted">OpenAI or Anthropic SDK, base URL changed</p>
        </Box>
        <Arrow label="request" />
        <Box title="TrustAgent gateway" strong>
          <ol className="mt-3 grid grid-cols-1 gap-2 text-sm sm:grid-cols-3">
            {[copy.tiers.instant_checks, copy.tiers.fast_check, 'Your policy'].map((t, i) => (
              <li key={t} className="rounded-md border border-border bg-surface-sunken px-2.5 py-2 text-fg">
                <span className="mr-1.5 font-mono text-xs text-fg-subtle">{i + 1}</span>
                {t}
              </li>
            ))}
          </ol>
        </Box>
        <Arrow label="answer" />
        <Box title="Your LLM provider">
          <p className="mt-1 text-sm text-fg-muted">OpenAI, Anthropic or any compatible API</p>
        </Box>
      </div>
      <div className="mt-3 grid grid-cols-1 lg:grid-cols-[minmax(0,1fr)_auto_minmax(0,1.6fr)_auto_minmax(0,1fr)]">
        <div className="lg:col-start-3">
          <div className="flex justify-center py-1 text-fg-subtle" aria-hidden="true">
            <ArrowDown className="size-4" />
          </div>
          <div className="rounded-lg border border-dashed border-border-strong px-4 py-3 text-sm">
            <span className="font-semibold text-fg">{copy.tiers.deep_audit}</span>
            <span className="text-fg-muted"> · asynchronous, for flagged and sampled answers, claim by claim, into your dashboard</span>
          </div>
        </div>
      </div>
      <figcaption className="sr-only">
        Your app sends its request through the TrustAgent gateway to your LLM provider. On the way back, the gateway runs instant
        checks, the fast check and your policy; flagged and sampled answers also get a deep audit in the background.
      </figcaption>
    </figure>
  );
}

export function HowItWorks() {
  return (
    <Section
      id="how"
      eyebrow="How it works"
      title="A check on every answer, on its way back to your app."
      lead="Point your SDK at the gateway. It checks each answer against the documents in the request, then does what your policy says."
    >
      <PathDiagram />
      <ol className="grid grid-cols-1 gap-x-10 gap-y-8 md:grid-cols-3">
        {steps.map(({ icon: Icon, title, body }, i) => (
          <li key={title} className="border-t border-border-strong pt-5">
            <div className="mb-3 flex items-center gap-3">
              <span className="font-mono text-sm text-fg-subtle">0{i + 1}</span>
              <Icon aria-hidden="true" className="size-4 text-fg-muted" />
            </div>
            <h3 className="text-lg font-semibold text-fg">{title}</h3>
            <p className="mt-2 text-fg-muted">{body}</p>
          </li>
        ))}
      </ol>
      <p className="mt-12 max-w-3xl rounded-lg border border-border bg-surface px-5 py-4 text-fg">
        <span className="font-semibold">Who it’s for: </span>
        <span className="text-fg-muted">
          teams shipping RAG, support or agent bots who need a per-answer safety signal without paying LLM-judge prices on every
          answer.
        </span>
      </p>
    </Section>
  );
}

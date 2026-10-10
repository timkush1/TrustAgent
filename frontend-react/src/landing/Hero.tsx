/** Section 1 (SPEC §8.1): the promise, the refund example in motion, and the three ways in. */
import { ArrowRight } from 'lucide-react';
import { claim, splitClaims } from './claims';
import { command, copy, DEMO_HREF, REPO_URL } from './content';
import { HeroCard } from './HeroCard';
import { ClaimCaveat, ClaimValue, Command, GitHubMark } from './ui';
import { buttonClass, container } from './styles';

/** Who should keep reading: said in the first screen, where a visitor decides. */
const WHO_FOR =
  'teams shipping RAG, support or agent bots who need a per-answer safety signal without paying LLM-judge prices on every answer.';

export function Hero() {
  const thirdParty = claim('jev_p50_latency_openrouter');
  const ours = claim('gate_latency_p50_measured');
  return (
    <section aria-labelledby="hero-title" className="pt-16 pb-20 max-sm:pt-10 max-sm:pb-14">
      <div className={container}>
        <p className="mb-6 inline-flex items-center gap-2 rounded-full border border-border bg-surface px-3 py-1 text-sm text-fg-muted">
          Open-source hallucination gateway
          <span aria-hidden="true" className="text-fg-subtle">
            ·
          </span>
          MIT
        </p>
        <h1
          id="hero-title"
          className="max-w-5xl text-display font-semibold tracking-[-0.04em] text-fg max-lg:text-[3.25rem] max-lg:leading-[1.05] max-sm:text-[2.5rem]"
        >
          {copy.product.tagline}
        </h1>
      </div>
      {/* Wide screens: the words, then the latency, on the left; the answer card on the right. A
          single column reads words, card, latency, so a phone's first screen reaches the example. */}
      <div
        className={`${container} mt-10 grid grid-cols-1 items-start gap-x-16 gap-y-12 max-sm:mt-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.08fr)] lg:grid-rows-[auto_1fr] lg:gap-y-0`}
      >
        <div>
          <p className="max-w-xl text-lead text-fg-muted">
            {splitClaims(copy.product.subhead).map((part, i) =>
              'text' in part ? <span key={i}>{part.text}</span> : <ClaimValue key={i} claim={part.claim} className="inline-flex" />,
            )}
          </p>
          <div className="mt-8 flex flex-wrap items-center gap-3">
            <a href={DEMO_HREF} className={buttonClass.primary}>
              Try the demo
              <ArrowRight aria-hidden="true" />
            </a>
            <a href={REPO_URL} className={buttonClass.secondary}>
              <GitHubMark className="size-4" />
              GitHub
            </a>
          </div>
          <div className="mt-4 flex flex-wrap items-center gap-x-3 gap-y-2">
            <Command command={command('demo')} note="no keys, Docker only" />
          </div>
          <p className="mt-6 max-w-xl text-sm text-fg" data-who-for>
            <span className="font-semibold">Who it’s for: </span>
            <span className="text-fg-muted">{WHO_FOR}</span>
          </p>
        </div>
        <div className="lg:col-start-2 lg:row-span-2 lg:row-start-1">
          <HeroCard />
        </div>
        <dl className="space-y-1.5 border-t border-border pt-5 text-sm lg:col-start-1 lg:row-start-2 lg:mt-8">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1" data-caveat-scope>
            <dt className="text-fg-muted">Fast-check latency:</dt>
            <dd>
              <ClaimValue claim={thirdParty} />
            </dd>
            <dd className="basis-full">
              <ClaimCaveat claim={thirdParty} className="max-w-md" />
            </dd>
          </div>
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <dt className="text-fg-muted">Measured by us:</dt>
            <dd>
              <ClaimValue claim={ours} />
            </dd>
          </div>
        </dl>
      </div>
    </section>
  );
}

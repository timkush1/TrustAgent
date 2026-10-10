/**
 * Section 5 (SPEC §8.1): try it. The replay tape is the primary way in. Its card is built at build
 * time from the dataset this site ships (virtual:trustagent/tape-digest), so the page fetches
 * nothing: what the tape is (simulated or recorded), when, which fast-check snapshot and judge, how
 * its requests came out, and five examples. A simulated tape says it was generated from the demo's
 * scripted scenarios, never that a running stack recorded it.
 *
 * Trying it live with your own OpenRouter key opens the static Playground's live section on one of
 * three scenarios (D99). The landing only links there: it never imports the live code. Where the
 * browser cannot run live checks, or this is the self-hosted dashboard's build, the card says how to
 * run the same checks locally instead, so no link goes nowhere.
 */
import { ArrowRight, CircleCheck, CircleMinus, CircleX, Eye, Flag, Repeat, TriangleAlert, type LucideIcon } from 'lucide-react';
import { digest, type TapeDigestTrace } from 'virtual:trustagent/tape-digest';
import { playground } from '@/config/runtime';
import { LIVE_SCENARIOS, liveHref, type LiveScenarioId } from '@/domain/liveScenarios';
import { splitClaims } from './claims';
import { BASE, command, DEMO_HREF, OUTCOME_IDS, outcome, outcomeOf } from './content';
import { demoLinkText, LIVE_ON_THIS_SITE } from './links';
import { sectionCopy } from './sectionCopy';
import { buttonClass } from './styles';
import { ClaimValue, Command, Section } from './ui';

const toneVar: Record<string, string> = {
  success: '--tone-success',
  warning: '--tone-warning',
  danger: '--tone-danger',
  info: '--tone-info',
  neutral: '--tone-neutral',
};
const outcomeIcon: Record<string, LucideIcon> = {
  passed: CircleCheck,
  flagged: Flag,
  blocked: CircleX,
  replaced: Repeat,
  would_block: Eye,
  would_flag: Eye,
  error: TriangleAlert,
  not_checked: CircleMinus,
};

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

const date = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' });
const count = new Intl.NumberFormat('en-US');

/** "scripted answer" / "generated answer": where an example's answer came from. */
function sourceWords(t: TapeDigestTrace): string | null {
  if (t.answer_source === 'scripted') return 'scripted answer';
  if (t.answer_source === 'generated') return 'generated answer';
  return null;
}

/** The outcomes in contract order (the order the dashboard lists them), with their counts. */
function outcomeCounts(): { id: string; n: number }[] {
  return OUTCOME_IDS.map((id) => ({ id, n: digest.outcomes[id] ?? 0 })).filter(({ n }) => n > 0);
}

export function TapeCard() {
  const total = Object.values(digest.outcomes).reduce((a, n) => a + n, 0);
  const when = date.format(Date.parse(digest.recorded_at));
  return (
    <div className="flex flex-col rounded-lg border border-border-strong bg-surface p-6 max-sm:p-4" data-tape-card>
      <div className="mb-4 flex items-center justify-between gap-3">
        <h3 className="text-lg font-semibold text-fg">The replay</h3>
        <span className="text-xs text-fg-subtle">replayed, not live</span>
      </div>
      <p className="text-sm text-fg" data-recording-label>
        {digest.label}
      </p>
      <p className="mt-1 text-xs text-fg-muted">
        {digest.kind === 'mock' ? `Generated from the zero-key demo’s scripted scenarios, dated ${when}` : `Recorded on ${when}`} · {count.format(total)}{' '}
        requests · {outcomeCounts().map(({ id, n }) => `${count.format(n)} ${outcome(id).label.toLowerCase()}`).join(', ')}
      </p>
      {digest.snapshot || digest.judge ? (
        <dl className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-fg-muted">
          {digest.snapshot ? (
            <div className="flex gap-1">
              <dt>Fast check:</dt>
              <dd className="font-mono text-fg">{digest.snapshot}</dd>
            </div>
          ) : null}
          {digest.judge ? (
            <div className="flex gap-1">
              <dt>Judge:</dt>
              <dd className="font-mono text-fg">{digest.judge}</dd>
            </div>
          ) : null}
        </dl>
      ) : null}
      <ul className="mt-5 space-y-2" aria-label="Examples from the tape">
        {digest.examples.map((t) => (
          <li key={t.trace_id} className="flex items-center gap-3 rounded-md border border-border px-3 py-2">
            <Pill id={outcomeOf(t)} />
            <span className="min-w-0 flex-1 truncate text-sm text-fg-muted">{t.question_preview}</span>
            {sourceWords(t) ? <span className="shrink-0 text-2xs text-fg-subtle">{sourceWords(t)}</span> : null}
          </li>
        ))}
      </ul>
      <div className="mt-auto pt-6">
        <a href={DEMO_HREF} className={buttonClass.primary}>
          {demoLinkText()}
          <ArrowRight aria-hidden="true" />
        </a>
      </div>
    </div>
  );
}

/** What each live scenario shows, in the landing's words (keyed by the frozen LIVE_SCENARIOS). */
const LIVE_WORDS: Record<LiveScenarioId, string> = {
  'grounded-pass': 'A grounded answer that passes',
  'refund-contradiction-flag': 'A refund answer that says 60 days when the policy says 30',
  'pii-leak-flag': 'An answer that leaks a customer’s personal data',
};

/**
 * The consent line with the check's cost (its Estimate chip) and the call cap filled in. The
 * sentence already says "a typical check … costs about", so the figure drops its own "~" and "per check".
 */
function CostLine() {
  const template = sectionCopy.playground_static.consent[1].replace('{cost_per_check}', '{claim:fast_check_cost_per_check}').replace('{max_calls}', count.format(playground.maxCalls));
  return (
    <p className="mt-4 text-sm text-fg-muted" data-chip-scope data-live-cost>
      {splitClaims(template).map((part, i) => {
        if ('text' in part) return <span key={i}>{part.text}</span>;
        const bare = { ...part.claim, display: part.claim.display.replace(/^[~≈]\s*/, '').replace(/\s+per check$/, '') };
        return <ClaimValue key={i} claim={bare} className="inline-flex" />;
      })}
    </p>
  );
}

/**
 * "Try it live in your browser": three links into the static Playground's live section when the
 * browser can run live checks; otherwise how to run the same checks locally.
 */

export function LiveCard({ live = LIVE_ON_THIS_SITE }: { live?: boolean }) {
  return (
    <div className="flex flex-col rounded-lg border border-border-strong bg-surface p-6 max-sm:p-4" data-live-card={live ? 'live' : 'local'}>
      <h3 className="mb-4 text-lg font-semibold text-fg">{live ? 'Try it live in your browser' : 'Run it yourself'}</h3>
      {live ? (
        <>
          <p className="text-sm text-fg-muted">Connect your OpenRouter key and run one of three examples, each with its documents:</p>
          <ul className="mt-3 space-y-2">
            {LIVE_SCENARIOS.map((id) => (
              <li key={id}>
                <a
                  href={liveHref(BASE, id)}
                  data-live-scenario={id}
                  className="group flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2.5 text-sm text-fg transition-colors hover:border-border-strong hover:bg-surface-hover"
                >
                  {LIVE_WORDS[id]}
                  <ArrowRight aria-hidden="true" className="size-4 shrink-0 text-fg-muted group-hover:text-fg" />
                </a>
              </li>
            ))}
          </ul>
          <p className="mt-4 text-sm text-fg-muted">{sectionCopy.playground_static.consent[0]}</p>
          <CostLine />
        </>
      ) : (
        <p className="text-sm text-fg-muted">The same checks run on your machine with mock backends and no keys:</p>
      )}
      <div className="mt-auto space-y-2 pt-6">
        {live ? <p className="text-sm text-fg">Or run it all on your machine, no keys needed:</p> : null}
        <p className="flex flex-wrap items-center gap-2 text-sm text-fg">
          <span>Run it yourself:</span>
          <Command command={command('demo')} />
        </p>
      </div>
    </div>
  );
}

export function TryIt({ live = LIVE_ON_THIS_SITE }: { live?: boolean }) {
  return (
    <Section
      id="try"
      eyebrow="Try it"
      title="Watch it catch the refund answer."
      lead={
        live
          ? 'Replay the demo’s traffic in the real dashboard, or run three checks live with your own key.'
          : 'Replay the demo’s traffic in the real dashboard, or run the same checks on your machine.'
      }
    >
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <TapeCard />
        <LiveCard live={live} />
      </div>
    </Section>
  );
}

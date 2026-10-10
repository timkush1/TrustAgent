/**
 * "Try Jev live" in the static Playground (D99-D104, D114): the visitor picks one of the three D99
 * scenarios (preselected from `?scenario=`) or writes their own documents, question and answer
 * (checked on support-bot), sees what Run will cost, and runs the fast check from this tab with
 * their OpenRouter key. Nothing reaches openrouter.ai before the consent dialog: Run without a key
 * opens it.
 *
 * States: no key, connected (in sessionStorage, or in memory when storage is blocked), running (the
 * spinner after 200 ms), a result, an error with what to do next, the 402 screen, the call cap,
 * the token cap and the pause after 10 rejected calls. Inside a frame only "Open in its own tab"
 * (D106); when the build has no live scope, the `make demo` line (D100 scope R). Every disabled
 * action says why next to it. The caps are a courtesy, not a security boundary: the key's own
 * credit limit is the real one. Loaded lazily, so the live feature stays out of the app entry
 * (D112). Owned by WEB-LIVE (LV-6).
 */
import { Play, RotateCcw, Trash2 } from 'lucide-react';
import { useId, useMemo, useState, type ReactNode } from 'react';
import { CodeBlock } from '@/components/common/CodeBlock';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { Button } from '@/components/ui/button';
import { Field } from '@/components/ui/field';
import { Textarea } from '@/components/ui/input';
import { RadioGroup } from '@/components/ui/radio-group';
import { Spinner } from '@/components/ui/spinner';
import { playground } from '@/config/runtime';
import { LIVE_CHECKS, LIVE_SCENARIOS, type LiveScenarioId } from '@/domain/liveScenarios';
import { formatUsd } from '@/domain/format';
import { commandText, copy, fillTemplate } from '@/domain/presentation';
import { liveActionLabel, liveChecksUsed, liveErrorWords, liveMessage, liveWords } from '@/domain/words/live';
import { CUSTOM_LIMITS, customInput, customProblems, estimateCheck, liveScenario, type CustomText, type LiveInput } from '@/lib/gate/liveCheck';
import { OPENROUTER_PAGES } from '@/lib/openrouter/endpoints';
import type { LiveErrorAction, LiveErrorView } from '@/lib/openrouter/errors';
import { isFramed } from '@/lib/openrouter/frame';
import { hasNoLimit } from '@/lib/openrouter/keyInfo';
import { browserKeyStore } from '@/lib/openrouter/keyStore';
import { browserUsage, type UsageStore } from '@/lib/openrouter/usage';
import { useBrowserKey } from '@/lib/openrouter/useBrowserKey';
import { useUsage } from '@/lib/openrouter/useUsage';
import { ConsentDialog } from './ConsentDialog';
import { ExternalLink } from './ExternalLink';
import { forgetKey } from './forget';
import { useKeyInfo } from './keyCheck';
import { LiveResult } from './LiveResult';
import { NoCreditsScreen } from './NoCreditsScreen';
import { OpenInTab } from './OpenInTab';
import { useLiveCheck } from './useLiveCheck';

type Choice = LiveScenarioId | 'custom';

export interface LivePanelProps {
  /** `?scenario=` (already checked against LIVE_SCENARIOS by the route). */
  scenario?: LiveScenarioId;
  /** `?paste=1`: open the consent dialog (the callback's "Paste a key instead"). */
  openConsent?: boolean;
  /** The build's live scope (D100); false shows the `make demo` line. Tests pass it. */
  liveChecks?: boolean;
  /** The tab's ledger (browserUsage() by default; tests pass their own). */
  usage?: UsageStore;
  /** The per-call token cap (the build's VITE_PLAYGROUND_MAX_TOKENS by default; tests pass a smaller one). */
  maxTokens?: number;
}

const EMPTY: CustomText = { documents: '', question: '', answer: '' };

function Notice({ children, testId, role }: { children: ReactNode; testId?: string; role?: 'alert' | 'status' }) {
  return (
    <div role={role} data-testid={testId} className="space-y-2 rounded-md border border-border bg-surface-sunken px-3 py-2.5 text-sm text-fg">
      {children}
    </div>
  );
}

function ScenarioPreview({ id }: { id: LiveScenarioId }) {
  const s = liveScenario(id);
  return (
    <div className="grid gap-3 md:grid-cols-2" data-testid="live-preview">
      <div className="space-y-1">
        <p className="text-xs font-medium text-fg-muted">{liveWords.asked}</p>
        <p className="text-sm text-fg">{s.question}</p>
      </div>
      <div className="space-y-1">
        <p className="text-xs font-medium text-fg-muted">{liveWords.answered}</p>
        <p className="text-sm text-fg">{s.response}</p>
      </div>
    </div>
  );
}

function CustomForm({ value, onChange, problems }: { value: CustomText; onChange: (v: CustomText) => void; problems: ReturnType<typeof customProblems> }) {
  const tooLong = (field: 'documents' | 'question' | 'answer') =>
    problems.includes(`${field}_long` as never) ? liveWords.too_long(CUSTOM_LIMITS[field]) : undefined;
  return (
    <div className="space-y-3" data-testid="live-custom">
      <p className="text-xs text-fg-muted">{liveWords.custom_hint}</p>
      <Field label={liveWords.documents_label} error={tooLong('documents')}>
        <Textarea rows={4} value={value.documents} onChange={(e) => onChange({ ...value, documents: e.currentTarget.value })} />
      </Field>
      <Field label={liveWords.question_label} error={tooLong('question')}>
        <Textarea rows={2} value={value.question} onChange={(e) => onChange({ ...value, question: e.currentTarget.value })} />
      </Field>
      <Field label={liveWords.answer_label} error={tooLong('answer')}>
        <Textarea rows={3} value={value.answer} onChange={(e) => onChange({ ...value, answer: e.currentTarget.value })} />
      </Field>
    </div>
  );
}

function Unavailable() {
  return (
    <Notice testId="live-unavailable">
      <p className="font-medium">{liveWords.unavailable_title}</p>
      <p>{fillTemplate(copy.playground_live.live_unavailable)}</p>
    </Notice>
  );
}

function CapReached({ maxCalls }: { maxCalls: number }) {
  return (
    <Notice testId="live-cap-reached" role="status">
      <p>{liveMessage('cap_reached', { max_calls: maxCalls })}</p>
      <CodeBlock code={commandText('demo') ?? 'make demo'} label={liveWords.run_yourself} />
    </Notice>
  );
}

export default function LivePanel({ scenario, openConsent = false, liveChecks = LIVE_CHECKS, usage, maxTokens = playground.maxTokens }: LivePanelProps) {
  if (!liveChecks) return <Unavailable />;
  if (isFramed()) return <OpenInTab />;
  return <LivePanelBody scenario={scenario} openConsent={openConsent} usageStore={usage ?? browserUsage()} maxTokens={maxTokens} />;
}

function LivePanelBody({ scenario, openConsent, usageStore, maxTokens }: { scenario?: LiveScenarioId; openConsent: boolean; usageStore: UsageStore; maxTokens: number }) {
  const id = useId();
  const store = browserKeyStore();
  const { key, state: keyState, storageBlocked } = useBrowserKey(store);
  const usage = useUsage(usageStore);
  const keyInfo = useKeyInfo(key, store);
  const { phase, run, clear } = useLiveCheck(store, usageStore, maxTokens);
  const [choice, setChoice] = useState<Choice>(scenario ?? LIVE_SCENARIOS[0]);
  const [custom, setCustom] = useState<CustomText>(EMPTY);
  const [consent, setConsent] = useState(openConsent && store.currentKey() === null);
  const running = phase.kind === 'running';
  const showSpinner = useDelayedLoading(running);

  // `?scenario=` changed while the page stayed (a landing link, Back): follow it.
  const [seenScenario, setSeenScenario] = useState(scenario);
  if (scenario !== seenScenario) {
    setSeenScenario(scenario);
    if (scenario) setChoice(scenario);
  }

  const problems = useMemo(() => (choice === 'custom' ? customProblems(custom) : []), [choice, custom]);
  const input: LiveInput | null = useMemo(() => {
    if (choice !== 'custom') return liveScenario(choice).input;
    return problems.length === 0 ? customInput(custom) : null;
  }, [choice, custom, problems]);
  const estimate = useMemo(() => (input ? estimateCheck(input, playground.jevModel, maxTokens) : null), [input, maxTokens]);

  const returnTo = choice === 'custom' ? 'app/playground/#live' : `app/playground/?scenario=${choice}#live`;
  const capReached = usage.calls >= usage.maxCalls;
  const tokenCap = estimate?.ok === false;
  const problemText =
    problems.includes('documents') ? liveWords.documents_missing : problems.includes('answer') ? liveWords.answer_missing : problems.length > 0 ? liveWords.shorten : null;
  const blockedReason = running ? null : capReached ? null : usage.paused ? null : problemText ?? (tokenCap ? liveMessage('token_cap', { max_tokens: maxTokens }) : null);
  const canRun = !running && !capReached && !usage.paused && input !== null && !tokenCap;

  function start() {
    if (!input) return;
    if (store.currentKey() === null) {
      setConsent(true);
      return;
    }
    run(input);
  }

  function act(action: LiveErrorAction) {
    switch (action) {
      case 'paste_new_key':
        setConsent(true);
        return;
      case 'retry':
        if (usage.paused) usageStore.resume();
        if (phase.kind === 'failed') run(phase.input);
        else if (input) run(input);
        return;
      case 'forget_key':
        forgetKey(store);
        clear();
        return;
      default:
        return;
    }
  }

  const options = [
    ...LIVE_SCENARIOS.map((s) => ({ value: s as Choice, label: liveScenario(s).title })),
    { value: 'custom' as Choice, label: liveWords.custom_title },
  ];

  return (
    <div className="space-y-5" data-testid="live-panel" data-key-state={keyState}>
      <RadioGroup<Choice> label={liveWords.pick_label} options={options} value={choice} onValueChange={setChoice} name="live-scenario" disabled={running} />

      {choice === 'custom' ? <CustomForm value={custom} onChange={setCustom} problems={problems} /> : <ScenarioPreview id={choice} />}

      {storageBlocked || keyState === 'memory' ? <Notice testId="live-storage-blocked">{copy.playground_live.storage_blocked}</Notice> : null}
      {keyInfo && hasNoLimit(keyInfo) ? <Notice testId="live-no-limit">{copy.playground_live.key_no_limit}</Notice> : null}

      {capReached ? (
        <CapReached maxCalls={usage.maxCalls} />
      ) : usage.paused ? (
        <Notice testId="live-rejected-pause" role="status">
          <p>{copy.playground_live.rejected_pause}</p>
          <div className="flex flex-wrap gap-2">
            <Button type="button" size="sm" onClick={() => act('retry')}>
              <RotateCcw aria-hidden="true" />
              {copy.playground_live.actions.retry}
            </Button>
            <Button type="button" size="sm" variant="ghost" onClick={() => act('forget_key')}>
              {copy.playground_live.actions.forget_key}
            </Button>
          </div>
        </Notice>
      ) : (
        <div className="space-y-2">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
            <Button type="button" variant="primary" onClick={start} disabled={!canRun} pending={running} aria-describedby={blockedReason || !key ? `${id}-why` : undefined}>
              <Play aria-hidden="true" />
              {copy.playground_live.run_check}
            </Button>
            {estimate?.ok && estimate.est_tokens.length === 0 ? (
              <span className="text-sm text-fg-muted" data-testid="live-estimate">
                {liveWords.no_call}
              </span>
            ) : estimate?.ok && estimate.cost_usd !== null ? (
              <span className="inline-flex items-center gap-1.5 text-sm text-fg-muted tabular" data-testid="live-estimate">
                ≈ {formatUsd(estimate.cost_usd)}
                <EvidenceBadge label="estimate" basis={liveWords.estimate_basis} subject={liveWords.estimate_subject} />
              </span>
            ) : null}
          </div>
          {blockedReason ? (
            <p id={`${id}-why`} className="text-xs text-fg-muted" data-testid="live-why">
              {blockedReason}
            </p>
          ) : !key ? (
            <p id={`${id}-why`} className="text-xs text-fg-muted">
              {liveWords.connect_first}
            </p>
          ) : null}
        </div>
      )}

      {key || usage.calls > 0 ? (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-border pt-3 text-xs text-fg-muted" data-testid="live-usage">
          <span className="tabular" data-testid="live-counter">
            {liveChecksUsed(usage.calls, usage.maxCalls)}
          </span>
          {usage.cost_source === null ? null : usage.cost_source === 'unknown' ? (
            <span>{liveWords.spent_unknown}</span>
          ) : (
            <span className="inline-flex items-center gap-1.5 tabular" data-testid="live-spent">
              {fillTemplate(copy.playground_static.running_cost, { cost: formatUsd(usage.cost_usd) })}
              <EvidenceBadge
                label={usage.cost_source}
                basis={usage.cost_source === 'vendor' ? liveWords.vendor_basis : liveWords.computed_basis}
                subject={liveWords.spent_subject}
              />
            </span>
          )}
          {key ? (
            <Button type="button" size="sm" variant="ghost" onClick={() => act('forget_key')} className="ml-auto">
              <Trash2 aria-hidden="true" />
              {copy.playground_static.forget_key}
            </Button>
          ) : null}
        </div>
      ) : null}

      <div aria-live="polite" className="min-w-0">
        {phase.kind === 'running' ? (
          showSpinner ? (
            <p className="flex items-center gap-2 text-sm text-fg-muted" data-testid="live-running">
              <Spinner size="sm" />
              {liveWords.running}
            </p>
          ) : null
        ) : phase.kind === 'failed' ? (
          phase.view.screen === 'no_credits' ? (
            <NoCreditsScreen store={store} onRecovered={clear} />
          ) : (
            <FailureNotice view={phase.view} onAction={act} maxTokens={maxTokens} maxCalls={usage.maxCalls} />
          )
        ) : phase.kind === 'done' ? (
          <LiveResult check={phase.check} />
        ) : null}
      </div>

      <ConsentDialog open={consent} onClose={() => setConsent(false)} returnTo={returnTo} />
    </div>
  );
}

function FailureNotice({ view, onAction, maxTokens, maxCalls }: { view: LiveErrorView; onAction: (a: LiveErrorAction) => void; maxTokens: number; maxCalls: number }) {
  const words = liveErrorWords(view, { max_calls: maxCalls, max_tokens: maxTokens });
  return (
    <Notice testId="live-error" role="alert">
      <p data-error-kind={view.kind}>{words.message}</p>
      {words.hint ? <p className="text-fg-muted">{words.hint}</p> : null}
      {view.actions.length > 0 ? (
        <div className="flex flex-wrap items-center gap-3">
          {view.actions.map((a) =>
            a === 'open_credits' || a === 'open_privacy' ? (
              <ExternalLink key={a} href={a === 'open_credits' ? OPENROUTER_PAGES.credits : OPENROUTER_PAGES.privacy} className="text-sm">
                {liveActionLabel(a)}
              </ExternalLink>
            ) : (
              <Button key={a} type="button" size="sm" variant={a === 'retry' || a === 'paste_new_key' ? 'secondary' : 'ghost'} onClick={() => onAction(a)}>
                {a === 'retry' ? <RotateCcw aria-hidden="true" /> : null}
                {liveActionLabel(a)}
              </Button>
            ),
          )}
        </div>
      ) : null}
    </Notice>
  );
}

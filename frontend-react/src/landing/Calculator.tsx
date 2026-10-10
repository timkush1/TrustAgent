/**
 * "Estimate your bill" (SPEC §8.1 section 3): the TS cost core (costModel.ts over
 * src/lib/decision/cost.ts, the same arithmetic as the gateway and the cost vectors) with the
 * reader's traffic, tokens, flag rate, sampling and deep-audit judge. Every result is an estimate
 * and says so; a price the reader types that cannot be used shows a field error, never a crash.
 * Loaded lazily with the pricing data, after the first paint.
 */
import { useId, useMemo, useState, type ReactNode } from 'react';
import { claim, LABEL_TEXT } from './claims';
import { REPO_URL } from './content';
import { sectionCopy } from './sectionCopy';
import { auditShareWords, defaultCalculatorInputs, formatMultiple, judgeOptions, monthlyCost, referenceJudge, type JudgeChoice, type JudgeOption } from './costModel';
import { priceAnchorId } from './priceSources';
import { LabelChip } from './ui';

const VOLUMES = [10_000, 100_000, 1_000_000, 10_000_000] as const;
const volumeWords: Record<(typeof VOLUMES)[number], string> = {
  10_000: '10k',
  100_000: '100k',
  1_000_000: '1M',
  10_000_000: '10M',
};

const usd = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 0 });
const usdCents = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 2 });
const count = new Intl.NumberFormat('en-US');
const perMTok = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 3 });

function money(x: number): string {
  return x >= 100 ? usd.format(x) : usdCents.format(x);
}

const pct = (v: number) => `${Math.round(v * 1000) / 10}%`;
const CUSTOM = 'custom';

/** A typed price; an empty field is no price (NaN), never $0. */
const priceOf = (text: string): number => (text.trim() === '' ? Number.NaN : Number(text));

function Slider({ label, value, min, max, step, format, onChange }: { label: string; value: number; min: number; max: number; step: number; format: (v: number) => string; onChange: (v: number) => void }) {
  const id = useId();
  return (
    <div>
      <div className="flex items-baseline justify-between gap-3">
        <label htmlFor={id} className="text-sm text-fg">
          {label}
        </label>
        <output htmlFor={id} data-input className="tabular text-sm font-medium text-fg">
          {format(value)}
        </output>
      </div>
      <input
        id={id}
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        className="mt-2 w-full accent-(--fg)"
      />
    </div>
  );
}

function Row({ label, value, strong = false, note, testId }: { label: ReactNode; value: string; strong?: boolean; note?: string; testId: string }) {
  return (
    <div className="flex items-baseline justify-between gap-4 py-2.5" data-cost-row={testId}>
      <span className={strong ? 'font-semibold text-fg' : 'text-fg-muted'}>
        {label}
        {note ? <span className="block text-xs text-fg-muted">{note}</span> : null}
      </span>
      <span className={`tabular text-right ${strong ? 'text-xl font-semibold text-fg' : 'font-medium text-fg'}`}>{value}</span>
    </div>
  );
}

/** A price the reader types, in dollars per million tokens. */
function PriceField({ label, value, onChange, error }: { label: string; value: string; onChange: (v: string) => void; error: string | null }) {
  const id = useId();
  const errorId = `${id}-error`;
  return (
    <div className="min-w-0">
      <label htmlFor={id} className="text-xs text-fg-muted">
        {label}
      </label>
      <div className="mt-1 flex items-center rounded-md border border-border-strong bg-surface focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-fg">
        <span aria-hidden="true" className="pl-2.5 text-sm text-fg-muted">
          $
        </span>
        <input
          id={id}
          type="number"
          inputMode="decimal"
          min={0}
          step="any"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? errorId : undefined}
          data-input
          className="tabular h-9 w-full min-w-0 bg-transparent px-1.5 text-sm text-fg outline-none"
        />
      </div>
      {error ? (
        <p id={errorId} className="mt-1 text-xs text-fg">
          {error}
        </p>
      ) : null}
    </div>
  );
}

/** The chosen judge's price, with where it comes from. */
function JudgePrice({ option }: { option: JudgeOption }) {
  const { entry } = option;
  if (option.local) return <p className="mt-2 text-xs text-fg-muted">{entry.note ?? 'Runs on your machine: no per-token price.'}</p>;
  return (
    <p className="mt-2 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-xs text-fg-muted" data-chip-scope>
      <span className="tabular text-fg">
        {perMTok.format(entry.input_per_mtok)} input · {perMTok.format(entry.output_per_mtok)} output
      </span>
      per million tokens
      <LabelChip claim={{ id: priceAnchorId(entry), label: 'vendor', labelText: LABEL_TEXT.vendor }} />
      {entry.as_of ? <span>as of {entry.as_of}</span> : null}
    </p>
  );
}

export function Calculator() {
  const estimate = claim('default_total_cost_per_1k');
  const sameChecksClaim = claim('llm_same_checks_cost_per_1k');
  const options = useMemo(() => judgeOptions(), []);
  const reference = useMemo(() => referenceJudge(options), [options]);
  const defaults = useMemo(() => defaultCalculatorInputs(), []);

  const [volume, setVolume] = useState<(typeof VOLUMES)[number]>(100_000);
  const [tokens, setTokens] = useState(defaults.tokensPerAnswer);
  const [flagRate, setFlagRate] = useState(defaults.flagRate);
  const [passRate, setPassRate] = useState(defaults.passSampleRate);
  const [judgeId, setJudgeId] = useState(reference.id);
  const [customIn, setCustomIn] = useState(String(reference.entry.input_per_mtok));
  const [customOut, setCustomOut] = useState(String(reference.entry.output_per_mtok));

  const option = options.find((o) => o.id === judgeId);
  const judge = useMemo<JudgeChoice>(
    () =>
      option
        ? { kind: 'table', option }
        : // An empty field is not $0: Number('') would quietly price the judge as free.
          { kind: 'custom', inputPerMTok: priceOf(customIn), outputPerMTok: priceOf(customOut) },
    [option, customIn, customOut],
  );
  const result = useMemo(
    () => monthlyCost({ answersPerMonth: volume, tokensPerAnswer: tokens, flagRate, passSampleRate: passRate, judge }),
    [volume, tokens, flagRate, passRate, judge],
  );
  const groupId = useId();
  const judgeSelectId = useId();
  const judgeName = option ? option.name : 'your judge';
  const fieldError = (field: 'custom_input' | 'custom_output') => (!result.ok && result.problem.field === field ? result.problem.message : null);

  return (
    <div className="mt-16 grid grid-cols-1 overflow-hidden rounded-lg border border-border-strong bg-surface lg:grid-cols-2">
      <div className="space-y-6 border-border p-6 max-lg:border-b lg:border-r max-sm:p-4">
        <div>
          <h3 className="text-lg font-semibold text-fg">Estimate your monthly bill</h3>
          <p className="mt-1 text-sm text-fg-muted">Change the inputs; the arithmetic is the gateway’s own cost code.</p>
        </div>
        <fieldset>
          <legend className="text-sm text-fg">Answers per month</legend>
          <div className="mt-2 grid grid-cols-4 gap-1 rounded-md border border-border bg-surface-sunken p-1">
            {VOLUMES.map((v) => (
              <label key={v} className="relative">
                <input
                  type="radio"
                  name={groupId}
                  value={v}
                  checked={volume === v}
                  onChange={() => setVolume(v)}
                  aria-label={`${count.format(v)} answers`}
                  className="peer absolute inset-0 size-full cursor-pointer opacity-0"
                />
                <span
                  data-input
                  className="flex h-8 items-center justify-center rounded-sm text-sm font-medium text-fg-muted transition-colors peer-checked:bg-surface peer-checked:text-fg peer-checked:shadow-[inset_0_0_0_1px_var(--border-strong)] peer-hover:text-fg peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-fg"
                >
                  {volumeWords[v]}
                </span>
              </label>
            ))}
          </div>
        </fieldset>
        <Slider label="Tokens per fast check (prompt, answer, documents)" value={tokens} min={1000} max={20000} step={500} format={(v) => count.format(v)} onChange={setTokens} />
        <Slider label="Answers the checks flag (always audited)" value={flagRate} min={0} max={0.2} step={0.01} format={pct} onChange={setFlagRate} />
        <Slider label="Other answers sampled for a deep audit" value={passRate} min={0} max={0.1} step={0.005} format={pct} onChange={setPassRate} />
        <div>
          <label htmlFor={judgeSelectId} className="text-sm text-fg">
            Deep-audit judge
          </label>
          <select
            id={judgeSelectId}
            value={judgeId}
            onChange={(e) => setJudgeId(e.target.value)}
            className="mt-2 h-9 w-full rounded-md border border-border-strong bg-surface px-2.5 text-sm text-fg"
          >
            {options.map((o) => (
              <option key={o.id} value={o.id}>
                {o.local ? `${o.name}: no per-token price` : o.name}
              </option>
            ))}
            <option value={CUSTOM}>Custom price per million tokens</option>
          </select>
          {option ? (
            <JudgePrice option={option} />
          ) : (
            <div className="mt-3 grid grid-cols-2 gap-3">
              <PriceField label="Input, per million tokens" value={customIn} onChange={setCustomIn} error={fieldError('custom_input')} />
              <PriceField label="Output, per million tokens" value={customOut} onChange={setCustomOut} error={fieldError('custom_output')} />
            </div>
          )}
        </div>
      </div>
      <div className="flex flex-col p-6 max-sm:p-4" data-chip-scope>
        <div className="flex items-center justify-between gap-3">
          <p className="text-sm font-semibold text-fg">Per month</p>
          <LabelChip claim={estimate} />
        </div>
        {result.ok ? (
          <>
            <div className="mt-2 divide-y divide-border">
              <Row testId="fast_check" label="Fast check on every answer" value={money(result.cost.fastCheck)} />
              <Row
                testId="deep_audit"
                label="Deep audits"
                note={`${auditShareWords(flagRate, passRate, result.cost.auditedShare)} · ${judgeName}`}
                value={money(result.cost.deepAudit)}
              />
              <Row testId="total" label="Your total" value={money(result.cost.total)} strong />
            </div>
            <div className="mt-4 rounded-md bg-surface-sunken px-4 py-1">
              <Row testId="same_checks" label="Asking an LLM judge the same questions" note={`${reference.name}, every answer`} value={money(result.cost.sameChecks)} />
              <Row testId="full_audit" label="A full LLM audit of every answer" note={`${reference.name}, claim by claim`} value={money(result.cost.fullAudit)} />
            </div>
            <p className="mt-4 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-sm text-fg" data-chip-scope data-multiple>
              <span>
                The fast check costs <span className="tabular font-semibold">{formatMultiple(result.cost.sameChecksMultiple)}</span> less than asking an LLM judge the same
                questions.
              </span>
              <LabelChip claim={sameChecksClaim} />
            </p>
          </>
        ) : (
          <p className="mt-4 rounded-md border border-border-strong px-4 py-3 text-sm text-fg" role="status">
            {result.problem.field === 'settings' ? result.problem.message : 'Enter both judge prices, $0 or more, to see the estimate.'}
          </p>
        )}
        <p className="mt-auto pt-5 text-xs text-fg-muted">
          {sectionCopy.savings.calculator_footnote} Prices and token assumptions:{' '}
          <a className="font-medium text-fg underline underline-offset-2" href={`${REPO_URL}/blob/main/contracts/pricing/assumptions.v1.json`}>
            pricing/assumptions.v1.json
          </a>
          .
        </p>
      </div>
    </div>
  );
}

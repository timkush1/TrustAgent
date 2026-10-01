/**
 * "Estimate your bill" (SPEC §8.1 section 3): the contract cost model (costModel.ts, the same terms
 * contracts/validate.mjs checks) with the reader's traffic, tokens, flag rate and sampling. Every
 * result is an estimate and says so.
 */
import { useId, useMemo, useState, type ReactNode } from 'react';
import { claim } from './claims';
import { copy, REPO_URL } from './content';
import { defaultInput, monthlyCost } from './costModel';
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

function money(x: number): string {
  return x >= 100 ? usd.format(x) : usdCents.format(x);
}

function Slider({ label, value, min, max, step, format, onChange }: { label: string; value: number; min: number; max: number; step: number; format: (v: number) => string; onChange: (v: number) => void }) {
  const id = useId();
  return (
    <div>
      <div className="flex items-baseline justify-between gap-3">
        <label htmlFor={id} className="text-sm text-fg">
          {label}
        </label>
        <output htmlFor={id} className="tabular text-sm font-medium text-fg">
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

function Row({ label, value, strong = false, note }: { label: ReactNode; value: string; strong?: boolean; note?: string }) {
  return (
    <div className="flex items-baseline justify-between gap-4 py-2.5">
      <span className={strong ? 'font-semibold text-fg' : 'text-fg-muted'}>
        {label}
        {note ? <span className="block text-xs text-fg-subtle">{note}</span> : null}
      </span>
      <span className={`tabular ${strong ? 'text-xl font-semibold text-fg' : 'font-medium text-fg'}`}>{value}</span>
    </div>
  );
}

export function Calculator() {
  const estimate = claim('default_total_cost_per_1k');
  const [volume, setVolume] = useState<(typeof VOLUMES)[number]>(100_000);
  const [tokens, setTokens] = useState(defaultInput('fast_check_input_tokens'));
  const [flagRate, setFlagRate] = useState(defaultInput('grounding_flag_rate'));
  const [passRate, setPassRate] = useState(defaultInput('pass_sample_rate'));
  const cost = useMemo(
    () => monthlyCost({ answersPerMonth: volume, tokensPerAnswer: tokens, flagRate, passSampleRate: passRate }),
    [volume, tokens, flagRate, passRate],
  );
  const pct = (v: number) => `${Math.round(v * 1000) / 10}%`;
  const groupId = useId();

  return (
    <div className="mt-16 grid grid-cols-1 overflow-hidden rounded-lg border border-border-strong bg-surface lg:grid-cols-2">
      <div className="space-y-6 border-border p-6 max-lg:border-b lg:border-r max-sm:p-4">
        <div>
          <h3 className="text-lg font-semibold text-fg">Estimate your monthly bill</h3>
          <p className="mt-1 text-sm text-fg-muted">Change the inputs; the model is the one behind every estimate on this page.</p>
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
                <span className="flex h-8 items-center justify-center rounded-sm text-sm font-medium text-fg-muted transition-colors peer-checked:bg-surface peer-checked:text-fg peer-checked:shadow-[inset_0_0_0_1px_var(--border-strong)] peer-hover:text-fg peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-fg">
                  {volumeWords[v]}
                </span>
              </label>
            ))}
          </div>
        </fieldset>
        <Slider label="Tokens per fast check (prompt, answer, documents)" value={tokens} min={1000} max={20000} step={500} format={(v) => count.format(v)} onChange={setTokens} />
        <Slider label="Answers the checks flag (always audited)" value={flagRate} min={0} max={0.2} step={0.01} format={pct} onChange={setFlagRate} />
        <Slider label="Other answers sampled for a deep audit" value={passRate} min={0} max={0.1} step={0.005} format={pct} onChange={setPassRate} />
      </div>
      <div className="flex flex-col p-6 max-sm:p-4">
        <div className="flex items-center justify-between gap-3">
          <p className="text-sm font-semibold text-fg">Per month</p>
          <LabelChip claim={estimate} />
        </div>
        <div className="mt-2 divide-y divide-border">
          <Row label="Fast check on every answer" value={money(cost.fastCheck)} />
          <Row label="Deep audits" note={`${pct(cost.auditedShare)} of answers, including a ${pct(defaultInput('uniform_sample_rate'))} uniform sample`} value={money(cost.deepAudit)} />
          <Row label="Your total" value={money(cost.total)} strong />
        </div>
        <div className="mt-4 rounded-md bg-surface-sunken px-4 py-1">
          <Row label="Asking an LLM judge the same questions" value={money(cost.sameChecks)} />
          <Row label="A full LLM audit of every answer" value={money(cost.fullAudit)} />
        </div>
        <p className="mt-auto pt-5 text-xs text-fg-muted">
          {copy.savings.calculator_footnote} Prices and token assumptions:{' '}
          <a className="font-medium text-fg underline underline-offset-2" href={`${REPO_URL}/blob/main/contracts/pricing/assumptions.v1.json`}>
            pricing/assumptions.v1.json
          </a>
          .
        </p>
      </div>
    </div>
  );
}

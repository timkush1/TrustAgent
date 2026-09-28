/**
 * One evaluator's risk as a bar in its level's tone, with a thin tick where it starts to flag
 * (`warn_at`) and a thick tick where it can block (`critical_at`, absent when the evaluator never
 * blocks). Exposed as a meter whose text states the value and both thresholds.
 */
import type { CSSProperties } from 'react';
import { formatRisk, r2 } from '@/domain/format';
import { levelWord, toneVar } from '@/domain/presentation';
import { cn } from '@/lib/cn';
import type { EvaluatorLevel } from '@/types/api';

const clamp01 = (x: number) => Math.min(1, Math.max(0, x));
const pct = (x: number) => `${clamp01(x) * 100}%`;

export interface RiskBarProps {
  risk: number;
  level: EvaluatorLevel;
  warnAt: number;
  criticalAt: number | null;
  /** Accessible name, e.g. "Groundedness risk". */
  label: string;
  className?: string;
}

function riskValueText(risk: number, warnAt: number, criticalAt: number | null): string {
  const block = criticalAt === null ? 'never blocks' : `blocks at ${formatRisk(criticalAt)}`;
  return `${formatRisk(risk)} (flags at ${formatRisk(warnAt)}, ${block})`;
}

export function RiskBar({ risk, level, warnAt, criticalAt, label, className }: RiskBarProps) {
  const tone = levelWord(level).tone;
  const value = clamp01(risk);
  return (
    <div
      role="meter"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={1}
      aria-valuenow={r2(value)}
      aria-valuetext={riskValueText(value, warnAt, criticalAt)}
      style={{ '--bar': `var(${toneVar(tone)})` } as CSSProperties}
      className={cn('relative h-1.5 w-full rounded-full bg-surface-sunken', className)}
    >
      <div className="absolute inset-y-0 left-0 rounded-full bg-(--bar)" style={{ width: pct(value) }} />
      <span
        aria-hidden="true"
        data-tick="flag"
        className="absolute -inset-y-1 w-px -translate-x-1/2 bg-fg-muted"
        style={{ left: pct(warnAt) }}
      />
      {criticalAt !== null ? (
        <span
          aria-hidden="true"
          data-tick="block"
          className="absolute -inset-y-1 w-0.5 -translate-x-1/2 bg-fg"
          style={{ left: pct(criticalAt) }}
        />
      ) : null}
    </div>
  );
}

/** The key for the two ticks, shown once under a list of bars. */
export function RiskBarLegend({ className }: { className?: string }) {
  return (
    <p className={cn('flex flex-wrap items-center gap-x-3 gap-y-1 text-2xs text-fg-muted', className)}>
      <span className="inline-flex items-center gap-1">
        <span aria-hidden="true" className="h-3 w-px bg-fg-muted" />
        Flags from here
      </span>
      <span className="inline-flex items-center gap-1">
        <span aria-hidden="true" className="h-3 w-0.5 bg-fg" />
        Can block from here
      </span>
    </p>
  );
}

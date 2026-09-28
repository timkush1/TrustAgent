/**
 * Words and icons for the control-plane enums the P3 pages show (SPEC §3.3, §7.1, §7.2, §7.4):
 * policy modes, downgrades, block readiness, calibration states, value labels, evidence gaps,
 * policy sources, evaluator origins and categories, stages, backends and latency stages.
 *
 * It extends src/domain/presentation.ts (same rules: copy from contracts/ui/*.v1.json wherever the
 * contract has it, plain words otherwise, never a raw enum in a component) and lives in its own
 * module so the Overview, Policies, Evaluators, Playground and Knowledge pages can grow it without
 * touching the trace and traffic presentation.
 */
import {
  BadgeCheck,
  Calculator,
  CircleDashed,
  CornerDownRight,
  Eye,
  Flag,
  FlaskConical,
  Globe,
  ShieldX,
  Store,
  type LucideIcon,
} from 'lucide-react';
import type {
  BlockReadiness,
  CalibrationState,
  DecisionBackend,
  Downgrade,
  EvaluatorMeta,
  EvidenceCoverage,
  GateAction,
  OverviewStats,
  PolicyDryRunResult,
  PolicyMode,
  PolicyVersionMeta,
  SystemStatus,
  ValueLabel,
} from '@/types/api';
import { copy, fillTemplate, glossaryTerm, humanizeId } from './presentation';

// ------------------------------------------------------------------------------------------ modes

export interface ModeView {
  mode: PolicyMode;
  label: string;
  /** The glossary's one-line definition. */
  description: string;
  icon: LucideIcon;
  /** Glossary id for <Term>. */
  term: string;
}

const MODE_ICONS: Record<PolicyMode, LucideIcon> = {
  observe: Eye,
  flag: Flag,
  block: ShieldX,
  fallback: CornerDownRight,
};

const MODE_LABELS: Record<PolicyMode, string> = {
  observe: 'Observe',
  flag: 'Flag',
  block: 'Block',
  fallback: 'Fallback',
};

/** Policy modes in increasing strictness (the order a picker lists them). */
export const POLICY_MODES: readonly PolicyMode[] = ['observe', 'flag', 'block', 'fallback'];

export function modeView(mode: PolicyMode): ModeView {
  return {
    mode,
    label: MODE_LABELS[mode] ?? humanizeId(mode),
    description: glossaryTerm(mode)?.short ?? '',
    icon: MODE_ICONS[mode] ?? Eye,
    term: mode,
  };
}

/** "Block → currently acting as Flag", or null when the route runs in its own mode. */
export function actingAsText(mode: PolicyMode, effective: PolicyMode): string | null {
  if (mode === effective) return null;
  return fillTemplate(copy.policies.acting_as, {
    mode: modeView(mode).label,
    effective_mode: modeView(effective).label,
  });
}

export function failModeLabel(fail: 'open' | 'closed'): string {
  return fail === 'closed' ? 'Fail closed' : 'Fail open';
}

// ------------------------------------------------------------------------------------ downgrades

/** Why a route acts in a milder mode than configured (copy.downgrades), with the server's own words as fallback. */
export function downgradeText(d: Pick<Downgrade, 'code' | 'message'>): string {
  const text = (copy.downgrades as Record<string, string>)[d.code];
  return text ?? d.message;
}

// ------------------------------------------------------------------------------- block readiness

export type ReadinessItem = BlockReadiness['missing'][number];

/** The five block-readiness requirements (SPEC §7.2), in the order the checklist lists them. */
export const READINESS_ITEMS: readonly ReadinessItem[] = ['mode', 'evidence', 'corroborator', 'calibration', 'buffering'];

const READINESS_WORDS: Record<ReadinessItem, { label: string; missing: string }> = {
  mode: {
    label: 'Route set to Block or Fallback',
    missing: 'The route is not in Block or Fallback mode.',
  },
  evidence: {
    label: 'Documents to check against',
    missing: 'None of the route’s evidence sources can supply documents for this check.',
  },
  corroborator: {
    label: 'A second signal that can agree',
    missing: 'No second check or instant check can confirm this one, so it cannot block alone.',
  },
  calibration: {
    label: 'Calibrated for the current fast-check version',
    missing: 'Thresholds are not calibrated on labelled traffic for this fast-check version.',
  },
  buffering: {
    label: 'Streams buffered',
    missing: 'Streamed answers pass straight through, so they can only be flagged.',
  },
};

export function readinessWords(item: ReadinessItem): { label: string; missing: string } {
  return READINESS_WORDS[item] ?? { label: humanizeId(item), missing: humanizeId(item) };
}

// --------------------------------------------------------------------------------- calibration

export interface CalibrationStateView {
  label: string;
  description: string;
  /** Good enough to block on (a measured calibration). */
  measured: boolean;
}

const CALIBRATION_STATES: Record<CalibrationState, CalibrationStateView> = {
  measured: { label: 'Calibrated', description: 'Thresholds fitted on at least 200 of your labels.', measured: true },
  provisional: { label: 'Provisional', description: 'Fitted on too few labels to block on yet.', measured: false },
  proxy: { label: 'Proxy calibration', description: 'Fitted on deep-audit labels, not human ones.', measured: false },
  mock: { label: 'Simulated calibration', description: 'Mock thresholds for the demo stack.', measured: false },
  family: { label: 'Borrowed calibration', description: 'Taken from another version of the fast check.', measured: false },
  none: { label: 'Not calibrated', description: 'Raw fast-check probabilities, no calibration yet.', measured: false },
};

export function calibrationStateView(state: CalibrationState): CalibrationStateView {
  return CALIBRATION_STATES[state] ?? CALIBRATION_STATES.none;
}

// ------------------------------------------------------------------------------- value labels

export interface ValueLabelView {
  label: string;
  icon: LucideIcon;
  /** Glossary id for the definition. */
  term: string;
}

const VALUE_LABEL_ICONS: Record<ValueLabel, LucideIcon> = {
  measured: BadgeCheck,
  estimate: Calculator,
  third_party: Globe,
  vendor: Store,
  simulated: FlaskConical,
};

/** Measured / Estimate / Third-party / Vendor / Simulated (copy.value_labels). */
export function valueLabelView(label: ValueLabel): ValueLabelView {
  return {
    label: copy.value_labels[label] ?? humanizeId(label),
    icon: VALUE_LABEL_ICONS[label] ?? CircleDashed,
    term: label,
  };
}

// ----------------------------------------------------------------------------- evidence gaps

type GapReason = EvidenceCoverage['top_reasons'][number]['reason'];

const GAP_REASONS: Record<GapReason, { label: string; hint: string }> = {
  no_evidence: {
    label: 'No documents in the request',
    hint: 'Attach the documents your model used with the trustagent.context extension, or put them in the system prompt.',
  },
  evidence_too_short: {
    label: 'Documents too short to check against',
    hint: 'Very short snippets are skipped; send the full passage the answer relies on.',
  },
  evidence_packed_away: {
    label: 'Documents left out to fit the size budget',
    hint: 'Raise evidence.max_tokens for the route, or send fewer, more relevant documents.',
  },
  too_long: {
    label: 'Answer too long to check',
    hint: 'Answers over the size limit pass unchecked; long answers are checked by the deep audit instead.',
  },
};

export function evidenceGapView(reason: GapReason): { label: string; hint: string } {
  return GAP_REASONS[reason] ?? { label: humanizeId(reason), hint: '' };
}

// ---------------------------------------------------------------------------- policy versions

const POLICY_SOURCES: Record<PolicyVersionMeta['source'] | SystemStatus['policy']['source'], string> = {
  bootstrap: 'Built-in default',
  builtin: 'Built-in default',
  file: 'From policies.yaml',
  dashboard: 'Edited in the dashboard',
  rollback: 'Rolled back',
  adopt_file: 'Adopted from policies.yaml',
};

export function policySourceLabel(source: PolicyVersionMeta['source'] | SystemStatus['policy']['source']): string {
  return POLICY_SOURCES[source] ?? humanizeId(source);
}

// ------------------------------------------------------------------------------- evaluators

const ORIGINS: Record<EvaluatorMeta['origin'], string> = {
  builtin: 'Built-in',
  file: 'From a file',
  dashboard: 'Made in the dashboard',
};

export function evaluatorOriginLabel(origin: EvaluatorMeta['origin']): string {
  return ORIGINS[origin] ?? humanizeId(origin);
}

const CATEGORIES: Record<string, string> = {
  grounding: 'Grounding',
  safety: 'Safety',
  compliance: 'Compliance',
  quality: 'Quality',
  security: 'Security',
  custom: 'Custom',
};

export function evaluatorCategoryLabel(category: string): string {
  return CATEGORIES[category] ?? humanizeId(category);
}

/** "Active" or "Watching" (shadow: shown in traces, never changes a response). */
export function stageLabel(stage: 'active' | 'shadow'): string {
  return stage === 'shadow' ? 'Watching' : 'Active';
}

// ------------------------------------------------------------------------------- backends

const BACKENDS: Record<DecisionBackend, string> = {
  'openrouter-decisions': 'OpenRouter',
  'openrouter-systemone': 'OpenRouter (systemone route)',
  typesafe: 'TypeSafe direct',
  compatible: 'Other gateway',
  mock: 'Mock (simulated)',
  off: 'Off',
};

export function backendLabel(backend: DecisionBackend): string {
  return BACKENDS[backend] ?? humanizeId(backend);
}

// ------------------------------------------------------------------------------ latency stages

type StageKey = keyof OverviewStats['stage_latency_ms'];

const STAGES: Record<StageKey, string> = {
  detectors: copy.tiers.instant_checks,
  evidence: 'Reading documents',
  knowledge: 'Knowledge base search',
  decision: copy.tiers.fast_check,
  queue_wait: 'Deep audit queue',
  decompose: 'Splitting into claims',
  retrieve: 'Finding evidence',
  verify: 'Checking claims',
  score: 'Scoring',
};

/** Stages in pipeline order. */
export const STAGE_ORDER: readonly StageKey[] = [
  'detectors',
  'evidence',
  'knowledge',
  'decision',
  'queue_wait',
  'decompose',
  'retrieve',
  'verify',
  'score',
];

export function stageLatencyLabel(stage: StageKey): string {
  return STAGES[stage] ?? humanizeId(stage);
}

const LATENCY_KINDS: Record<keyof OverviewStats['latency_ms'], string> = {
  gate: copy.tiers.fast_check,
  overhead: 'Added to each request',
  upstream: 'Your model',
  audit: copy.tiers.deep_audit,
};

export function latencyKindLabel(kind: keyof OverviewStats['latency_ms']): string {
  return LATENCY_KINDS[kind] ?? humanizeId(kind);
}

// -------------------------------------------------------------------------- gate actions

const ACTIONS: Record<GateAction, string> = {
  observe: 'Observed only',
  pass: 'Passed',
  flag: 'Flagged',
  block: 'Blocked',
  fallback: 'Replaced (fallback)',
};

/** Past-tense words for an action, for counts ("12 flagged"). */
export function gateActionLabel(action: GateAction): string {
  return ACTIONS[action] ?? humanizeId(action);
}

export const GATE_ACTION_ORDER: readonly GateAction[] = ['pass', 'observe', 'flag', 'fallback', 'block'];

type SkipKey = PolicyDryRunResult['skipped'][number]['reason'];

const DRY_RUN_SKIPS: Record<SkipKey, string> = {
  evaluator_not_in_history: 'A check in this version never ran on these answers, so it cannot be replayed',
  no_answers: 'No stored answers for these routes in this window',
  body_expired: 'The stored answer text has expired',
};

export function dryRunSkipLabel(reason: SkipKey): string {
  return DRY_RUN_SKIPS[reason] ?? humanizeId(reason);
}

// ----------------------------------------------------------------------------- knowledge

export function kbClaimStatusLabel(status: 'accepted' | 'quarantined'): string {
  return status === 'quarantined' ? 'Quarantined' : 'Accepted';
}

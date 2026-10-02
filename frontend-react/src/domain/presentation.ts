/**
 * The presentation layer (SPEC §3.3): every word, icon and tone the UI shows for a server enum
 * comes from here, never from a raw enum string in a component (eslint `no-restricted-syntax`
 * enforces that in src/components and src/routes).
 *
 * Sources: contracts/ui/outcomes.v1.json (outcomes, reasons, level words, status reasons),
 * contracts/ui/copy.v1.json (tier names, evidence sources, states), contracts/ui/glossary.v1.json
 * (<Term> tooltips) and contracts/ui/commands.v1.json ({command:id} placeholders).
 */
import {
  CircleCheck,
  CircleDashed,
  CircleMinus,
  CircleQuestionMark,
  Check,
  Contrast,
  CornerDownRight,
  Flag,
  LoaderCircle,
  Minus,
  OctagonX,
  ShieldAlert,
  ShieldX,
  TriangleAlert,
  type LucideIcon,
} from 'lucide-react';
import type {
  AuditVerdict,
  ClaimVerdict,
  DecisionBackend,
  EvaluatorLevel,
  EvidenceSource,
  EvidenceSummary,
  GateAction,
  GateStatus,
  GateVerdict,
  SkipReason,
  TraceAuditStatus,
  TraceSummary,
  WouldAction,
} from '@/types/api';
import {
  commandsContract,
  copy,
  glossaryContract,
  outcomesContract,
  type GlossaryTerm,
  type OutcomeEntry,
  type OutcomeId,
  type Tone,
} from './contracts';

export type { OutcomeId, Tone } from './contracts';

// ---------------------------------------------------------------------------------------- templates

/**
 * Fills `{name}` placeholders. `{command:id}` resolves from ui/commands.v1.json. A placeholder with
 * no value is dropped together with the ": " or " (…)" that introduces it, so "Mentions a number
 * not in your documents: {finding}" reads cleanly when the finding is not at hand.
 */
export function fillTemplate(template: string, vars: Record<string, string | number | null | undefined> = {}): string {
  const valueOf = (name: string): string | undefined => {
    if (name.startsWith('command:')) return commandText(name.slice('command:'.length)) ?? undefined;
    const v = vars[name];
    return v === null || v === undefined || v === '' ? undefined : String(v);
  };
  return template
    .replace(/\s*\(\{([a-z0-9_:]+)\}\)/g, (m, name: string) => {
      const v = valueOf(name);
      return v === undefined ? '' : m.replace(`{${name}}`, v);
    })
    .replace(/:\s*\{([a-z0-9_:]+)\}/g, (m, name: string) => {
      const v = valueOf(name);
      return v === undefined ? '' : m.replace(`{${name}}`, v);
    })
    .replace(/\{([a-z0-9_:]+)\}/g, (_m, name: string) => valueOf(name) ?? '')
    .trim();
}

export function commandText(id: string): string | null {
  return commandsContract.commands.find((c) => c.id === id)?.command ?? null;
}

// Longest first, so `make demo-down && make up` wins over `make demo-down`.
const commandStrings = commandsContract.commands.map((c) => c.command).sort((a, b) => b.length - a.length);

/** Splits filled copy into text and command runs, so commands can render as code. */
export function splitCommands(text: string): { text: string; command: boolean }[] {
  const parts: { text: string; command: boolean }[] = [];
  let rest = text;
  while (rest) {
    let best: { at: number; cmd: string } | null = null;
    for (const cmd of commandStrings) {
      const at = rest.indexOf(cmd);
      if (at !== -1 && (best === null || at < best.at)) best = { at, cmd };
    }
    if (!best) {
      parts.push({ text: rest, command: false });
      break;
    }
    if (best.at > 0) parts.push({ text: rest.slice(0, best.at), command: false });
    parts.push({ text: best.cmd, command: true });
    rest = rest.slice(best.at + best.cmd.length);
  }
  return parts;
}

// ----------------------------------------------------------------------------------------- outcomes

export interface OutcomeInput {
  gate_status: GateStatus | 'pending';
  action: GateAction | null;
  would_action: WouldAction | null;
}

/** First matching rule of ui/outcomes.v1.json (SPEC §2.9 "Outcome for the UI"). */
export function outcomeOf(t: OutcomeInput): OutcomeId {
  const fields: Record<string, string | null> = {
    gate_status: t.gate_status,
    action: t.action,
    would_action: t.would_action,
  };
  for (const rule of outcomesContract.rules) {
    const matches = Object.entries(rule.when).every(([key, list]) =>
      (list as readonly string[]).includes(fields[key] ?? ''),
    );
    if (matches) return rule.outcome;
  }
  return 'passed';
}

/**
 * How a pill is drawn, on top of its tone: `solid` (inverted fill: the answer was stopped),
 * `tint` (tinted fill: something happened to the answer), `outline` (dashed: it only would have),
 * `quiet` (neutral: nothing to judge yet). Shape + icon + label keep outcomes apart without color.
 */
export type Emphasis = 'solid' | 'tint' | 'outline' | 'quiet';

export interface OutcomeView extends OutcomeEntry {
  icon: LucideIcon;
  emphasis: Emphasis;
}

const outcomeStyle: Record<OutcomeId, { icon: LucideIcon; emphasis: Emphasis }> = {
  checking: { icon: LoaderCircle, emphasis: 'quiet' },
  blocked: { icon: ShieldX, emphasis: 'solid' },
  replaced: { icon: CornerDownRight, emphasis: 'tint' },
  flagged: { icon: Flag, emphasis: 'tint' },
  would_block: { icon: ShieldAlert, emphasis: 'outline' },
  would_flag: { icon: Flag, emphasis: 'outline' },
  error: { icon: TriangleAlert, emphasis: 'tint' },
  not_checked: { icon: CircleMinus, emphasis: 'quiet' },
  passed: { icon: CircleCheck, emphasis: 'tint' },
};

const outcomeById = new Map(outcomesContract.outcomes.map((o) => [o.id, o]));

export function outcomeView(id: OutcomeId): OutcomeView {
  const entry = outcomeById.get(id);
  if (!entry) throw new Error(`ui/outcomes.v1.json has no outcome ${id}`);
  return { ...entry, ...outcomeStyle[id] };
}

/** Status words for "Passed unchecked: {reason}" and "Not checked: {reason}". */
export function statusReason(key: string | null | undefined): string | null {
  if (!key) return null;
  return outcomesContract.status_reasons[key] ?? null;
}

/**
 * The reason key behind a trace's gate status: the skip reason, then "no documents" for a
 * not_assessed trace that had no evidence, then the gate status itself.
 */
export function statusReasonKey(t: {
  gate_status: GateStatus | 'pending';
  skip_reason: SkipReason | null;
  checked_against?: EvidenceSummary | null;
  degraded?: string | null;
}): string | null {
  if (t.skip_reason) return t.skip_reason;
  if (t.gate_status === 'not_assessed' && (t.checked_against == null || t.checked_against.used.length === 0)) {
    return 'no_evidence';
  }
  if ((t.gate_status === 'timeout' || t.gate_status === 'error') && t.degraded) return t.degraded;
  if (t.gate_status === 'pending' || t.gate_status === 'ok') return null;
  return t.gate_status;
}

/** Outcome pill, headline and next step for a trace (lists, headers, the outcome card). */
export function traceOutcome(
  t: Pick<TraceSummary, 'gate_status' | 'action' | 'would_action' | 'skip_reason' | 'checked_against'> & {
    degraded?: string | null;
  },
): OutcomeView & { headlineText: string } {
  const view = outcomeView(outcomeOf(t));
  const reason = statusReason(statusReasonKey(t));
  return { ...view, headlineText: fillTemplate(view.headline, { reason }) };
}

// ------------------------------------------------------------------------------------------ reasons

function globToRegExp(glob: string): RegExp {
  const escaped = glob.replace(/[.+?^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '([^:]+)');
  return new RegExp(`^${escaped}$`);
}

const reasonMatchers = outcomesContract.reasons.map((r) => ({ ...r, re: globToRegExp(r.match) }));

export interface ReasonContext {
  /** Detector finding text for `det:*` reasons, e.g. the "60" of "60 days". */
  finding?: string | null;
  /** Degraded cause for `fail_closed`. */
  degraded?: string | null;
  /** Display names of evaluators by id; defaults to a humanized id. */
  evaluatorName?: (id: string) => string | undefined;
}

export function humanizeId(id: string): string {
  const words = id.replace(/[_-]+/g, ' ').trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/** Plain-words copy for one decision reason (`contradiction:critical`, `det:…`, `disagreement`). */
export function reasonCopy(reason: string, ctx: ReasonContext = {}): string {
  for (const m of reasonMatchers) {
    if (!m.re.test(reason)) continue;
    const [evaluatorId = '', levelKey = ''] = reason.split(':');
    const level = isLevel(levelKey) ? levelWord(levelKey).label : levelKey;
    return fillTemplate(m.copy, {
      finding: ctx.finding,
      degraded: statusReason(ctx.degraded) ?? ctx.degraded,
      evaluator: ctx.evaluatorName?.(evaluatorId) ?? humanizeId(evaluatorId),
      level,
    });
  }
  return humanizeId(reason);
}

/** The headline reason for a row: the first reason, in plain words, or null. */
export function headlineReason(reasons: readonly string[], ctx?: ReasonContext): string | null {
  return reasons.length > 0 ? reasonCopy(reasons[0], ctx) : null;
}

// ------------------------------------------------------------------------------------------- levels

export interface LevelView {
  level: EvaluatorLevel;
  label: string;
  tone: Tone;
}

function isLevel(s: string): s is EvaluatorLevel {
  return s === 'ok' || s === 'warn' || s === 'critical';
}

/** "Low risk" / "Elevated risk" / "High risk". */
export function levelWord(level: EvaluatorLevel): LevelView {
  return { level, ...outcomesContract.levels[level] };
}

// ----------------------------------------------------------------------------------------- verdicts

/** Mark style for answer text; matches `mark[data-v]` in index.css. */
export type MarkStyle = 'supported' | 'contradicted' | 'unsupported' | 'partial' | 'unknown' | null;

export interface VerdictView {
  label: string;
  /** Short explanation for tooltips and screen readers. */
  description: string;
  tone: Tone;
  /** CSS custom property carrying the verdict hue. */
  colorVar: string;
  icon: LucideIcon;
  mark: MarkStyle;
  /** Underline style used by `mark`, for legends. */
  underline: 'solid' | 'wavy' | 'dotted' | 'dashed' | 'double' | 'none';
}

// Severity order (most severe first) is the key order.
const claimVerdicts: Record<ClaimVerdict, VerdictView> = {
  CONTRADICTED: {
    label: 'Contradicted',
    description: 'Your documents say this is false.',
    tone: 'danger',
    colorVar: '--v-contradicted',
    icon: OctagonX,
    mark: 'contradicted',
    underline: 'wavy',
  },
  UNSUPPORTED: {
    label: 'Unsupported',
    description: "Nothing in your documents supports this.",
    tone: 'warning',
    colorVar: '--v-unsupported',
    icon: CircleQuestionMark,
    mark: 'unsupported',
    underline: 'dotted',
  },
  PARTIALLY_SUPPORTED: {
    label: 'Partly supported',
    description: 'Your documents support part of this.',
    tone: 'info',
    colorVar: '--v-partial',
    icon: Contrast,
    mark: 'partial',
    underline: 'dashed',
  },
  SUPPORTED: {
    label: 'Supported',
    description: 'Your documents state or directly imply this.',
    tone: 'success',
    colorVar: '--v-supported',
    icon: Check,
    mark: 'supported',
    underline: 'solid',
  },
  UNKNOWN: {
    label: 'Unknown',
    description: 'The judge could not decide (its answer could not be read).',
    tone: 'neutral',
    colorVar: '--v-unknown',
    icon: CircleDashed,
    mark: 'unknown',
    underline: 'double',
  },
  NOT_CHECKED: {
    label: 'Not checked',
    description: 'There were no documents to check this against.',
    tone: 'neutral',
    colorVar: '--v-not-checked',
    icon: Minus,
    mark: null,
    underline: 'none',
  },
};

/** Claim verdicts, most severe first. */
export const CLAIM_VERDICT_ORDER = Object.keys(claimVerdicts) as ClaimVerdict[];

export function claimVerdictView(v: ClaimVerdict): VerdictView {
  return claimVerdicts[v] ?? claimVerdicts.UNKNOWN;
}

const gateVerdictToClaim: Record<GateVerdict, ClaimVerdict> = {
  supported: 'SUPPORTED',
  unsupported: 'UNSUPPORTED',
  contradicted: 'CONTRADICTED',
  not_assessed: 'NOT_CHECKED',
  unknown: 'UNKNOWN',
};

/** The fast check's verdict, drawn like the matching claim verdict. */
export function gateVerdictView(v: GateVerdict): VerdictView {
  return claimVerdicts[gateVerdictToClaim[v]];
}

export interface AuditVerdictView {
  label: string;
  tone: Tone;
  icon: LucideIcon;
}

const auditVerdicts: Record<AuditVerdict, AuditVerdictView> = {
  GROUNDED: { label: 'Grounded', tone: 'success', icon: Check },
  HALLUCINATED: { label: 'Hallucinated', tone: 'danger', icon: OctagonX },
  UNVERIFIABLE: { label: 'Unverifiable', tone: 'warning', icon: CircleQuestionMark },
  NO_CLAIMS: { label: 'No claims to check', tone: 'neutral', icon: Minus },
  NOT_ASSESSED: { label: 'Not assessed', tone: 'neutral', icon: CircleMinus },
};

export function auditVerdictView(v: AuditVerdict): AuditVerdictView {
  return auditVerdicts[v];
}

export interface AuditStateView {
  label: string;
  tone: Tone;
  icon: LucideIcon;
  /** The audit is still on its way (show a skeleton, expect an update). */
  pending: boolean;
}

const auditStates: Record<TraceAuditStatus, Omit<AuditStateView, 'label'> & { label: string }> = {
  none: { label: 'No deep audit', tone: 'neutral', icon: Minus, pending: false },
  queued: { label: 'Deep audit queued', tone: 'neutral', icon: LoaderCircle, pending: true },
  running: { label: 'Deep audit running', tone: 'neutral', icon: LoaderCircle, pending: true },
  completed: { label: 'Deep audit done', tone: 'neutral', icon: Check, pending: false },
  degraded: { label: 'Deep audit incomplete', tone: 'warning', icon: TriangleAlert, pending: false },
  failed: { label: 'Deep audit failed', tone: 'danger', icon: TriangleAlert, pending: false },
  dropped: { label: 'Deep audit skipped', tone: 'neutral', icon: Minus, pending: false },
  sampled_out: { label: 'Not sampled for deep audit', tone: 'neutral', icon: Minus, pending: false },
  disabled: { label: 'Deep audit off', tone: 'neutral', icon: Minus, pending: false },
  not_assessed: { label: 'Not assessed', tone: 'neutral', icon: CircleMinus, pending: false },
};

/**
 * The deep-audit state of a trace in words. A finished audit shows its verdict; a trace the
 * engine could not assess (no documents) reads "Not assessed" whether the gate skipped the audit
 * (status not_assessed) or the engine answered NOT_ASSESSED.
 */
export function auditStateView(audit: Pick<TraceSummary['audit'], 'status' | 'verdict'>): AuditStateView {
  if ((audit.status === 'completed' || audit.status === 'degraded') && audit.verdict) {
    const v = auditVerdicts[audit.verdict];
    return {
      label: audit.status === 'degraded' ? `${v.label} (incomplete)` : v.label,
      tone: v.tone,
      icon: v.icon,
      pending: false,
    };
  }
  return auditStates[audit.status] ?? auditStates.none;
}

// -------------------------------------------------------------------------------------- flags

/** Words for a trace flag (traces.flags, SPEC §2.3); an unknown flag is humanized. */
export function flagWord(flag: string): string {
  const words: Record<string, string> = copy.trace.flags;
  return Object.hasOwn(words, flag) ? words[flag] : humanizeId(flag);
}

// ----------------------------------------------------------------------------------------- evidence

export function evidenceSourceLabel(source: EvidenceSource | 'none'): string {
  return copy.evidence_sources[source];
}

/**
 * What an answer was checked against: document labels when known, else source names, else
 * "No documents". `text` is the full "Checked against: …" line.
 */
export function checkedAgainst(summary: EvidenceSummary | null | undefined): {
  items: string[];
  none: boolean;
  text: string;
} {
  const used = summary?.used ?? [];
  const items = used.flatMap((u) => (u.labels.length > 0 ? u.labels : [evidenceSourceLabel(u.source)]));
  const none = items.length === 0;
  const sources = none ? evidenceSourceLabel('none') : items.join(' · ');
  return { items, none, text: fillTemplate(copy.trace.checked_against, { sources }) };
}

// ------------------------------------------------------------------------------------ tiers & terms

/** Tier names are fixed product words (SPEC §3.3): "Fast check (Jev)" and "Deep audit". */
export const TIER = {
  instant_checks: copy.tiers.instant_checks,
  fast_check: copy.tiers.fast_check,
  deep_audit: copy.tiers.deep_audit,
} as const;

const glossaryById = new Map(glossaryContract.terms.map((t) => [t.id, t]));

export function glossaryTerm(id: string): GlossaryTerm | null {
  return glossaryById.get(id) ?? null;
}

/** Words the primary UI avoids (internal jargon); tests scan copy for them. */
export const AVOID_IN_PRIMARY_UI: readonly string[] = glossaryContract.avoid_in_primary_ui;

// ------------------------------------------------------------------------------------------- tones

/** CSS custom property for a tone (see index.css). */
export function toneVar(tone: Tone): string {
  return `--tone-${tone}`;
}

export { copy };

// P4 words, one module per plane and owner, each prefixed (sec*, cost*, eval*, studio*; P4-PLAN D86).
export * from './words/security';
export * from './words/costs';
export * from './words/evaluators';
export * from './words/studio';

// ------------------------------------------------------------------------------------------- costs

export type CostSource = 'reported' | 'computed' | 'free_local' | 'simulated' | 'unknown';

/**
 * The honest-number label of a cost (DESIGN.md §8): vendor-reported, computed from the price
 * table (an estimate), simulated by a mock, or a local model with no API fee. Null when unknown:
 * the figure is then not shown as a number.
 */
export function costSourceLabel(source: CostSource): string | null {
  switch (source) {
    case 'reported':
      return copy.value_labels.vendor;
    case 'computed':
      return copy.value_labels.estimate;
    case 'simulated':
      return copy.value_labels.simulated;
    case 'free_local':
      return 'Local model, no API fee';
    default:
      return null;
  }
}

/** The least certain of several cost sources (simulated, then unknown, then estimate, then vendor). */
export function weakestCostSource(sources: readonly CostSource[]): CostSource {
  const order: CostSource[] = ['simulated', 'unknown', 'computed', 'free_local', 'reported'];
  for (const s of order) if (sources.includes(s)) return s;
  return 'unknown';
}

// ---------------------------------------------------------------------------------- model names

const DECISION_BACKEND_WORDS: Record<DecisionBackend, string> = {
  'openrouter-decisions': 'OpenRouter',
  'openrouter-systemone': 'OpenRouter System One',
  typesafe: 'TypeSafe',
  compatible: 'Compatible gateway',
  mock: 'Mock (simulated)',
  off: 'Off',
};

/** Words for the fast check's backend (DecisionModelUsage.backend). */
export function decisionBackendLabel(backend: DecisionBackend): string {
  return DECISION_BACKEND_WORDS[backend] ?? 'Unknown backend';
}

const JUDGE_PROVIDER_WORDS: Record<string, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  openrouter: 'OpenRouter',
  ollama: 'Ollama (local)',
  mock: 'Mock (simulated)',
  none: 'No judge',
};

/** Words for the deep-audit judge's provider (JudgeInfo.effective_provider). */
export function judgeProviderLabel(provider: string): string {
  return JUDGE_PROVIDER_WORDS[provider] ?? provider;
}

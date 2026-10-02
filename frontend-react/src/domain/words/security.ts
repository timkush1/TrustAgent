/**
 * Words, icons and tones for the P4 security server values (provider, verify status, used_by, backend cards, store states and skip reasons).
 * Owned by WEB-ON after Step 0b. presentation.ts re-exports this module with `export *`, so every
 * export here starts with `sec` (P4-PLAN D86) and can never collide with another plane's words.
 * Sentences come from contracts/ui (copy.v1.json); the words for enum values live here, so a
 * component never renders a raw server value.
 *
 * `copy` is imported from ../contracts, not ../presentation: presentation.ts re-exports this module,
 * so its constants are not initialised yet while this one loads. Functions (fillTemplate,
 * humanizeId) are hoisted and safe to import from it.
 */
import {
  CircleAlert,
  CircleCheck,
  CircleDashed,
  CircleHelp,
  ShieldAlert,
  WifiOff,
  type LucideIcon,
} from 'lucide-react';
import type {
  BindingSkipReason,
  ComponentStateName,
  DecisionModelStatus,
  BindingTarget,
  CredentialProvider,
  CredentialVerifyStatus,
  CredentialView,
  DecisionBackend,
  RuntimeSettings,
  Scope,
  SystemStatus,
  WarningDetails,
} from '@/types/api';
import { copy, type Tone } from '../contracts';
import { formatCount } from '../format';
import { fillTemplate, humanizeId } from '../presentation';

// -------------------------------------------------------------------------------- credentials

/** The credential names the gateway knows (P4-PLAN D29), in the order Connections shows them. */
export const SEC_VENDOR_NAMES = ['openrouter', 'typesafe', 'anthropic', 'openai'] as const;
export type SecVendorName = (typeof SEC_VENDOR_NAMES)[number];

const PROVIDERS: Record<CredentialProvider, string> = {
  openrouter: 'OpenRouter',
  typesafe: 'TypeSafe',
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  generic: 'Other gateway',
};

/** Words for CredentialView.provider. */
export function secProviderLabel(provider: CredentialProvider): string {
  return PROVIDERS[provider] ?? humanizeId(provider);
}

const CREDENTIAL_NAMES: Record<string, string> = {
  openrouter: 'OpenRouter',
  typesafe: 'TypeSafe',
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  compatible: 'Other gateway',
  'upstream-default': 'Upstream key (your model)',
  'upstream-anthropic': 'Upstream key (Anthropic route)',
  'upstream-fallback': 'Upstream key (fallback)',
};

/** Words for a credential's name (`openrouter`, `upstream-default`, …). */
export function secCredentialLabel(name: string): string {
  return CREDENTIAL_NAMES[name] ?? humanizeId(name);
}

/** Where a vendor's keys are made, for the Paste dialog. */
export const SEC_KEY_PAGES: Record<SecVendorName, { url: string; label: string; placeholder: string }> = {
  openrouter: { url: 'https://openrouter.ai/settings/keys', label: 'openrouter.ai/settings/keys', placeholder: 'sk-or-v1-…' },
  typesafe: { url: 'https://console.typesafe.ai', label: 'console.typesafe.ai', placeholder: 'ts-…' },
  anthropic: { url: 'https://console.anthropic.com/settings/keys', label: 'console.anthropic.com', placeholder: 'sk-ant-…' },
  openai: { url: 'https://platform.openai.com/api-keys', label: 'platform.openai.com/api-keys', placeholder: 'sk-…' },
};

export interface SecVerifyView {
  label: string;
  tone: Tone;
  icon: LucideIcon;
  /** What to do about it, when anything. */
  hint: string | null;
}

const VERIFY: Record<CredentialVerifyStatus, SecVerifyView> = {
  ok: { label: 'Working', tone: 'success', icon: CircleCheck, hint: null },
  unauthorized: { label: 'Key rejected', tone: 'danger', icon: ShieldAlert, hint: 'The provider refused this key. Paste a new one, or connect again.' },
  no_credits: { label: 'No credits', tone: 'warning', icon: CircleAlert, hint: copy.setup.step_connect.no_credits },
  unreachable: { label: "Couldn't reach the provider", tone: 'warning', icon: WifiOff, hint: 'The provider did not answer. Try the test again in a minute.' },
  unknown: { label: 'Not checked', tone: 'neutral', icon: CircleHelp, hint: 'This kind of key is not checked with its provider; it is used as is.' },
};

const NEVER_TESTED: SecVerifyView = { label: 'Not tested yet', tone: 'neutral', icon: CircleDashed, hint: null };

/** Words for a key check's result (CredentialVerification.status, CredentialView.last_verify_status). */
export function secVerifyView(status: CredentialVerifyStatus | null | undefined): SecVerifyView {
  return status ? (VERIFY[status] ?? NEVER_TESTED) : NEVER_TESTED;
}

type UsedBy = CredentialView['used_by'][number];

const USED_BY: Record<UsedBy, string> = {
  decision_model: copy.tiers.fast_check,
  judge: `${copy.tiers.deep_audit} judge`,
  upstream_default: 'Your model (upstream)',
  upstream_anthropic: 'Anthropic route (upstream)',
  upstream_fallback: 'Fallback answers (upstream)',
};

/** Words for a "Used for" chip (CredentialView.used_by). */
export function secUsedByLabel(target: UsedBy): string {
  return USED_BY[target] ?? humanizeId(target);
}

/** Words for a binding target (BindingTarget): "Fast check (Jev)", "Deep audit judge". */
export function secBindingLabel(target: BindingTarget): string {
  return USED_BY[target];
}

/** Joins words for a sentence: "A", "A and B", "A, B and C". */
export function secJoinWords(words: readonly string[]): string {
  if (words.length <= 1) return words[0] ?? '';
  return `${words.slice(0, -1).join(', ')} and ${words[words.length - 1]}`;
}

/** Why a binding was not applied (CredentialWriteResult.bindings.skipped[].reason, D75). */
export function secSkipReasonText(reason: BindingSkipReason, target: BindingTarget): string {
  switch (reason) {
    case 'env_managed':
      return target === 'judge'
        ? copy.setup.step_connect.judge_env_managed
        : `The ${secBindingLabel(target)} is set by the environment, so it was left as it is.`;
    case 'backend_pinned':
      return `The ${secBindingLabel(target)} backend is pinned in .env to one that does not use this key, so it was left as it is.`;
    case 'verification_failed':
      return `Not used for the ${secBindingLabel(target)} yet: the key check did not pass.`;
    default:
      return `The ${secBindingLabel(target)} was left as it is.`;
  }
}

// ----------------------------------------------------------------------------- store states

type StoreState = SystemStatus['credentials']['store'];

export interface SecStoreView {
  /** Keys can be saved here. */
  writable: boolean;
  title: string;
  /** What to do, from contracts/ui; null when the store is ready. */
  body: string | null;
}

const STORE: Record<StoreState, SecStoreView> = {
  ready: { writable: true, title: 'Saved keys are encrypted', body: null },
  unavailable: { writable: false, title: "Keys can't be saved yet", body: copy.settings.store_unavailable },
  no_database: { writable: false, title: "Keys can't be saved without a database", body: copy.settings.no_database },
};

/** Words for SystemStatus.credentials.store (P4-PLAN D32). */
export function secStoreView(store: StoreState): SecStoreView {
  return STORE[store] ?? STORE.unavailable;
}

// ------------------------------------------------------------------------- backends (Models)

export interface SecBackendCard {
  id: 'openrouter' | 'typesafe' | 'compatible';
  title: string;
  description: string;
  /** The backend the card selects. */
  backend: DecisionBackend;
  /** The credential the backend uses (bindings.decision_model). */
  credential: 'openrouter' | 'typesafe' | 'compatible';
  recommended: boolean;
}

/** The three fast-check backend cards of Settings → Models (SPEC §3.3, §6). */
export const SEC_BACKEND_CARDS: readonly SecBackendCard[] = [
  {
    id: 'openrouter',
    title: 'OpenRouter',
    description: 'One OpenRouter key runs the fast check and the deep-audit judge. No TypeSafe account needed.',
    backend: 'openrouter-decisions',
    credential: 'openrouter',
    recommended: true,
  },
  {
    id: 'typesafe',
    title: 'TypeSafe direct',
    description: 'Calls TypeSafe AI with a console.typesafe.ai key.',
    backend: 'typesafe',
    credential: 'typesafe',
    recommended: false,
  },
  {
    id: 'compatible',
    title: 'Other gateway',
    description: 'Any OpenAI-compatible gateway that serves the model, with its own base URL and key.',
    backend: 'compatible',
    credential: 'compatible',
    recommended: false,
  },
];

/** The card a backend belongs to (both OpenRouter routes share one card); null for mock, off and auto. */
export function secBackendCardOf(backend: RuntimeSettings['decision_model']['backend'] | DecisionBackend): SecBackendCard | null {
  if (backend === 'openrouter-systemone') return SEC_BACKEND_CARDS[0];
  return SEC_BACKEND_CARDS.find((c) => c.backend === backend) ?? null;
}

const SETTING_BACKENDS: Record<RuntimeSettings['decision_model']['backend'], string> = {
  auto: 'Automatic (from the connected key)',
  'openrouter-decisions': 'OpenRouter',
  'openrouter-systemone': 'OpenRouter (systemone route)',
  typesafe: 'TypeSafe direct',
  compatible: 'Other gateway',
  mock: 'Simulated (mock)',
  off: 'Off',
};

/** Words for RuntimeSettings.decision_model.backend, `auto` included. */
export function secBackendSettingLabel(backend: RuntimeSettings['decision_model']['backend']): string {
  return SETTING_BACKENDS[backend] ?? humanizeId(backend);
}

const JUDGE_PROVIDERS: Record<RuntimeSettings['judge']['provider'], string> = {
  auto: 'Automatic (from the connected key)',
  openrouter: 'OpenRouter',
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  ollama: 'Ollama (local)',
  mock: 'Simulated (mock)',
  none: 'No judge',
};

/** The judge providers Settings → Models offers, in order. */
export const SEC_JUDGE_PROVIDERS = Object.keys(JUDGE_PROVIDERS) as RuntimeSettings['judge']['provider'][];

/** Words for RuntimeSettings.judge.provider, `auto` included. */
export function secJudgeSettingLabel(provider: RuntimeSettings['judge']['provider']): string {
  return JUDGE_PROVIDERS[provider] ?? humanizeId(provider);
}

/** Which credential a judge provider uses, for the "uses key" hint; null for the ones that need none. */
export function secJudgeCredential(provider: RuntimeSettings['judge']['provider']): SecVendorName | null {
  return provider === 'openrouter' || provider === 'anthropic' || provider === 'openai' ? provider : null;
}

const DECISION_STATES: Record<DecisionModelStatus['state'], { label: string; tone: Tone }> = {
  ready: { label: 'Ready', tone: 'success' },
  mock: { label: 'Simulated checks', tone: 'info' },
  checking: { label: 'Checking the connection…', tone: 'neutral' },
  off: { label: 'Off: answers pass unchecked', tone: 'warning' },
  degraded: { label: 'Degraded', tone: 'warning' },
  breaker_open: { label: 'Paused after repeated errors', tone: 'warning' },
  unauthorized: { label: 'Key rejected', tone: 'danger' },
  no_credits: { label: 'No OpenRouter credits', tone: 'danger' },
  budget_exhausted: { label: "Today's budget is used up", tone: 'warning' },
  misconfigured: { label: 'Misconfigured', tone: 'danger' },
  unreachable: { label: 'Unreachable', tone: 'warning' },
};

/** Words and tone for the fast check's live state (SystemStatus.decision_model.state). */
export function secDecisionStateView(state: DecisionModelStatus['state']): { label: string; tone: Tone } {
  return DECISION_STATES[state] ?? { label: humanizeId(state), tone: 'neutral' };
}

const COMPONENT_STATES: Record<ComponentStateName, { label: string; tone: Tone }> = {
  up: { label: 'Ready', tone: 'success' },
  degraded: { label: 'Degraded', tone: 'warning' },
  down: { label: 'Down', tone: 'danger' },
  disabled: { label: 'Off', tone: 'neutral' },
  unknown: { label: 'Unknown', tone: 'neutral' },
};

/** Words and tone for a component state (the judge, the engine). */
export function secComponentStateView(state: ComponentStateName): { label: string; tone: Tone } {
  return COMPONENT_STATES[state] ?? COMPONENT_STATES.unknown;
}

/** The env vars behind env-managed settings paths (P4-PLAN D33), for "Set in .env (…)". */
const PATH_ENV: Record<string, string> = {
  'decision_model.backend': 'TRUTHTABLE_JEV_BACKEND',
  'decision_model.model': 'TRUTHTABLE_JEV_MODEL',
  'decision_model.base_url': 'TRUTHTABLE_JEV_BASE_URL',
  'decision_model.path': 'TRUTHTABLE_JEV_PATH',
  'judge.provider': 'LLM_PROVIDER',
  'judge.model': 'LLM_MODEL',
  'upstream.default.url': 'TRUTHTABLE_UPSTREAM_URL',
  'upstream.default.auth_header': 'TRUTHTABLE_UPSTREAM_AUTH_HEADER',
  'upstream.anthropic.url': 'TRUTHTABLE_ANTHROPIC_UPSTREAM_URL',
  'upstream.fallback.url': 'TRUTHTABLE_FALLBACK_UPSTREAM_URL',
  'upstream.fallback.format': 'TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT',
  'budgets.decision_usd_day': 'TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY',
  'budgets.llm_usd_day': 'TRUTHTABLE_SPEND_CAP_LLM_USD_DAY',
};

/** The env var that pins a RuntimeSettings path (D33), or null (bindings follow a key, not one var). */
export function secEnvKeyOfPath(path: string): string | null {
  return PATH_ENV[path] ?? null;
}

// ------------------------------------------------------------------------------ API keys

const SCOPES: Record<Scope, { label: string; description: string }> = {
  ingest: { label: 'Send requests', description: 'Apps send their traffic through the gateway with it.' },
  read: { label: 'Read the dashboard', description: 'Sees traces, statuses and settings, without prompts and answers.' },
  content: { label: 'See prompts and answers', description: 'Can see prompts and answers in traces and exports.' },
  admin: { label: 'Admin', description: 'Changes settings, keys and policies.' },
};

/** Scopes in the order the Access page lists them. */
export const SEC_SCOPES: readonly Scope[] = ['ingest', 'read', 'content', 'admin'];

/** Words for an API key scope. `content` reads "can see prompts and answers" (P4-PLAN ON-3). */
export function secScopeView(scope: Scope): { label: string; description: string } {
  return SCOPES[scope] ?? { label: humanizeId(scope), description: '' };
}

const AUDIT_ACTIONS: Record<string, string> = {
  'setup.claim': 'Claimed the setup link',
  'setup_token.mint': 'Printed a new setup link',
  'session.sign_in': 'Signed in',
  'session.revoke_all': 'Signed out everywhere',
  'api_key.create': 'Created an API key',
  'api_key.revoke': 'Revoked an API key',
  'settings.update': 'Changed settings',
  'credential.put': 'Saved a key',
  'credential.rotate': 'Replaced a key',
  'credential.delete': 'Removed a key',
  'credential.verify': 'Tested a key',
  'credential.connect_openrouter': 'Connected OpenRouter',
  'evaluator.create': 'Created an evaluator',
  'evaluator.version': 'Saved an evaluator version',
  'evaluator.archive': 'Archived an evaluator',
  'calibration.import': 'Imported a calibration',
  'calibration.fit': 'Fitted a calibration',
  'calibration.activate': 'Adopted a calibration',
  'calibration.deactivate': 'Turned a calibration off',
  'studio.dry_run': 'Ran a Studio dry run',
  'kb.upload': 'Uploaded knowledge documents',
  'export.replay': 'Exported a replay recording',
};

/** Every admin-audit action of P4-PLAN D44, for tests and filters. */
export const SEC_AUDIT_ACTIONS = Object.keys(AUDIT_ACTIONS);

/** Words for an admin audit-log action (`credential.put`, …, D44). */
export function secAuditActionLabel(action: string): string {
  return AUDIT_ACTIONS[action] ?? humanizeId(action.replace(/[._]/g, ' '));
}

/** Words for a settings group an admin changed (the first segment of a RuntimeSettings path). */
const SETTINGS_GROUPS: Record<string, string> = {
  decision_model: copy.tiers.fast_check,
  judge: `${copy.tiers.deep_audit} judge`,
  upstream: 'Upstreams',
  bindings: 'Which key runs which check',
  budgets: 'Daily budgets',
  pricing_overrides: 'Prices',
};

/** The diff fields of a settings.update entry that describe it rather than name a setting. */
const SETTINGS_DIFF_META = new Set(['keys', 'paths', 'fields', 'credential', 'unbound', 'reason']);

function diffRecord(diff: unknown): Record<string, unknown> | null {
  return diff !== null && typeof diff === 'object' && !Array.isArray(diff) ? (diff as Record<string, unknown>) : null;
}

function diffString(d: Record<string, unknown>, key: string): string | null {
  const v = d[key];
  return typeof v === 'string' && v !== '' ? v : null;
}

function diffStrings(d: Record<string, unknown>, key: string): string[] {
  const v = d[key];
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string' && x !== '') : [];
}

function usedByWords(targets: readonly string[]): string {
  return secJoinWords(targets.map((t) => secUsedByLabel(t as UsedBy)));
}

function settingsGroups(d: Record<string, unknown>): string[] {
  const named = diffStrings(d, 'keys');
  const paths = named.length > 0 ? named : diffStrings(d, 'paths').length > 0 ? diffStrings(d, 'paths') : Object.keys(d).filter((k) => !SETTINGS_DIFF_META.has(k));
  const groups = [...new Set(paths.map((path) => path.split('.')[0]))];
  return groups.map((g) => SETTINGS_GROUPS[g] ?? humanizeId(g));
}

/**
 * What an admin audit entry acted on (AdminAuditEntry.target): a saved key or a settings group in
 * words, anything else (a key id, an evaluator) as the id it is, shown in mono.
 */
export function secAuditTargetView(action: string, target: string | null | undefined): { text: string; mono: boolean } {
  if (!target) return { text: '—', mono: false };
  const credential = action.startsWith('credential.') || (action === 'settings.update' && target in CREDENTIAL_NAMES);
  if (credential) return { text: secCredentialLabel(target), mono: false };
  if (action === 'settings.update' && target in SETTINGS_GROUPS) return { text: SETTINGS_GROUPS[target], mono: false };
  return { text: target, mono: true };
}

/**
 * What an admin audit entry changed, in words for the activity table (D44): the key's label and
 * scopes, a saved key's masked form, check result and checks, or the settings groups. The entry's
 * dialog keeps the full record. Entries with nothing to add read "—".
 */
export function secAuditChangeSummary(action: string, diff: unknown): string {
  const d = diffRecord(diff);
  if (!d || Object.keys(d).length === 0) return action === 'session.revoke_all' ? 'Every dashboard session ended' : '—';
  const parts: string[] = [];
  const label = diffString(d, 'label');
  const scopes = diffStrings(d, 'scopes').map((sc) => secScopeView(sc as Scope).label);
  switch (action) {
    case 'setup.claim':
    case 'api_key.create':
    case 'api_key.revoke':
    case 'session.sign_in':
      if (label) parts.push(`“${label}”`);
      if (scopes.length > 0) parts.push(secJoinWords(scopes));
      break;
    case 'session.revoke_all':
      parts.push('Every dashboard session ended');
      break;
    case 'setup_token.mint':
      parts.push(d.forced === true ? 'Recovery link (an admin already existed)' : 'First-run link');
      break;
    case 'credential.put':
    case 'credential.rotate':
    case 'credential.connect_openrouter':
    case 'credential.verify': {
      const masked = diffString(d, 'masked');
      const status = diffString(d, 'verify') ?? diffString(d, 'status');
      const bound = action === 'credential.verify' ? [] : diffStrings(d, 'bindings');
      const asked = diffStrings(d, 'bind');
      if (masked) parts.push(masked);
      if (status) parts.push(secVerifyView(status as CredentialVerifyStatus).label);
      if (bound.length > 0) parts.push(`now used for ${usedByWords(bound)}`);
      else if (asked.length > 0 && !status) parts.push(`for ${usedByWords(asked)}`);
      break;
    }
    case 'credential.delete': {
      const unbound = diffStrings(d, 'unbound');
      parts.push(unbound.length > 0 ? `was used for ${usedByWords(unbound)}` : 'was not in use');
      break;
    }
    case 'settings.update': {
      const groups = settingsGroups(d);
      if (groups.length > 0) parts.push(secJoinWords(groups));
      if (diffString(d, 'reason') === 'credential.delete') parts.push('after a key was removed');
      break;
    }
    default: {
      const keys = Object.keys(d).map((k) => humanizeId(k.replace(/\./g, ' ')));
      parts.push(keys.slice(0, 3).join(', ') + (keys.length > 3 ? ` +${keys.length - 3}` : ''));
    }
  }
  return parts.length > 0 ? parts.join(' · ') : '—';
}

// ------------------------------------------------------------------------------- warnings

/**
 * Words for a settings or status warning by code (D32, D77), filled from its details; null for a
 * code without words here, so the caller shows the server's own message.
 */
export function secWarningText(code: string | undefined, details: WarningDetails | undefined): string | null {
  const names = (details?.names ?? []).map(secCredentialLabel);
  const routes = details?.routes ?? [];
  switch (code) {
    case 'master_key_changed':
      return fillTemplate(copy.settings.master_key_changed, { names: secJoinWords(names) || 'some saved keys' });
    case 'fail_closed_unprotected':
      return routes.length > 0
        ? `These routes fail closed and have no working fast check, so they block every answer: ${routes.join(', ')}.`
        : 'Some routes fail closed and have no working fast check, so they block every answer.';
    default:
      return null;
  }
}

/** The "Ready · 231 ms · $0.00001" line of a passed key check (copy.setup.step_connect.probe_ready). */
export function secProbeReadyText(latencyMs: number | null | undefined, cost: string | null): string {
  if (latencyMs === null || latencyMs === undefined) return cost ? `Ready · ${cost}` : 'Ready';
  const line = fillTemplate(copy.setup.step_connect.probe_ready, { latency_ms: formatCount(latencyMs), cost: cost ?? '' });
  return line.replace(/\s*·\s*$/, '');
}

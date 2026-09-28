/**
 * Plain words for a route card (SPEC §3.3 Policies): how requests are matched, which settings
 * matter for what happens to a risky answer, and why an edit is not possible here.
 */
import type { DisabledReasonId, Reason } from '@/components/common/DisabledReason';
import { evidenceSourceLabel, humanizeId } from '@/domain/presentation';
import type { EffectiveRoute, EvidenceSource, PolicyState } from '@/types/api';
import { DEFAULT_EVIDENCE_SOURCES, DEFAULT_ROUTE } from './policyDraft';

export function isDefaultRoute(name: string): boolean {
  return name === DEFAULT_ROUTE;
}

export function routeTitle(name: string): string {
  return isDefaultRoute(name) ? 'Default route' : name;
}

function list(value: unknown): string[] {
  return Array.isArray(value) ? value.map(String) : [];
}

/** "Header X-TrustAgent-Route is support-bot", "Path /v1/chat/completions", … */
export function matchSummary(name: string, match: Record<string, unknown>): string[] {
  if (isDefaultRoute(name)) return ['Requests that no other route matches'];
  const out: string[] = [];
  const headers = match.headers;
  if (headers && typeof headers === 'object') {
    for (const [k, v] of Object.entries(headers as Record<string, unknown>)) out.push(`Header ${k} is ${String(v)}`);
  }
  const paths = list(match.paths);
  if (paths.length) out.push(`${paths.length === 1 ? 'Path' : 'Paths'} ${paths.join(', ')}`);
  const models = list(match.models);
  if (models.length) out.push(`${models.length === 1 ? 'Model' : 'Models'} ${models.join(', ')}`);
  const keys = list(match.key_labels);
  if (keys.length) out.push(`${keys.length === 1 ? 'Key' : 'Keys'} ${keys.join(', ')}`);
  return out.length > 0 ? out : ['Every request (no matchers)'];
}

export function evaluatorId(ref: string): string {
  const at = ref.lastIndexOf('@');
  return at === -1 ? ref : ref.slice(0, at);
}

export function evaluatorVersion(ref: string): number | null {
  const at = ref.lastIndexOf('@');
  const v = at === -1 ? NaN : Number(ref.slice(at + 1));
  return Number.isInteger(v) ? v : null;
}

/** "Groundedness" from the library, else the humanized id. */
export function evaluatorDisplayName(ref: string, names: ReadonlyMap<string, string>): string {
  const id = evaluatorId(ref);
  return names.get(id) ?? humanizeId(id);
}

export interface SettingFact {
  label: string;
  value: string;
}

/** The settings that decide what happens to a risky answer, in words. */
export function settingFacts(route: EffectiveRoute): SettingFact[] {
  const s = route.settings;
  const sources = (s.evidence?.sources ?? DEFAULT_EVIDENCE_SOURCES) as EvidenceSource[];
  const onNoEvidence = { ignore: 'Pass them unchanged', flag: 'Flag them', block: 'Block them' } as const;
  return [
    { label: 'If a check cannot finish', value: route.fail === 'closed' ? 'Block the answer (fail closed)' : 'Deliver it unchecked (fail open)' },
    { label: 'Streamed answers', value: s.streaming === 'buffer' ? 'Held until checked (buffered)' : 'Passed straight through' },
    { label: 'Check deadline', value: s.deadline_ms ? `${s.deadline_ms} ms` : '600 ms' },
    { label: 'Documents come from', value: sources.map((src) => evidenceSourceLabel(src)).join(' · ') },
    { label: 'Answers with no documents', value: onNoEvidence[s.on_no_evidence ?? 'ignore'] },
  ];
}

const EDITABLE_REASONS: { match: RegExp; reason: (raw: string) => Reason }[] = [
  {
    match: /file-managed|no database/i,
    reason: () => ({ text: 'This proxy has no database, so the policy is file-managed: edit config/policies.yaml and it reloads.' }),
  },
  {
    match: /locked|CONFIG_LOCK/i,
    reason: () => ({ text: 'Locked by TRUTHTABLE_CONFIG_LOCK: config/policies.yaml is the only source of the policy.' }),
  },
  { match: /admin/i, reason: () => 'needs_admin' },
];

/**
 * Why the policy cannot be changed from here, or null: the instance's own reason (read-only, not
 * an admin) first, then the server's `editable_reason` (SPEC §2.4 "Policy editability").
 */
export function policyWriteReason(state: Pick<PolicyState, 'editable' | 'editable_reason'> | undefined, instance: DisabledReasonId | null): Reason | null {
  if (instance) return instance;
  if (!state || state.editable) return null;
  const raw = state.editable_reason ?? '';
  const known = EDITABLE_REASONS.find((r) => r.match.test(raw));
  return known ? known.reason(raw) : { text: raw || 'This policy cannot be edited here.' };
}

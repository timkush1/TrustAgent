/**
 * Words for the static site's live mode ("Try Jev live": browser key, caps, OpenRouter errors,
 * PKCE callback; D99-D105, D136). Owned by WEB-LIVE. presentation.ts re-exports this module with
 * `export *`, so every export here starts with `live` (D86) and can never collide with another
 * plane's words. The words themselves come from contracts/ui copy.playground_live.* (and the
 * playground_static cap line), never from a vendor message or a gateway banner.
 *
 * `copy` is imported from ../contracts, not ../presentation: presentation.ts re-exports this module,
 * so its constants are not initialised yet while this one loads. fillTemplate is a hoisted function
 * and safe to import from it.
 */
import type { LiveErrorAction, LiveErrorView, LiveMessageId } from '@/lib/openrouter/errors';
import { copy } from '../contracts';
import { fillTemplate } from '../presentation';

/** Values the live sentences take: {max_calls}, {max_tokens}. */
export interface LiveMessageVars {
  max_calls?: number;
  max_tokens?: number;
}

/** One live sentence, its placeholders filled. */
export function liveMessage(id: LiveMessageId, vars: LiveMessageVars = {}): string {
  const live = copy.playground_live;
  let template: string;
  if (id === 'cap_reached') template = copy.playground_static.cap_reached;
  else if (id.startsWith('errors.')) template = live.errors[id.slice('errors.'.length) as keyof typeof live.errors];
  else template = live[id as 'retry_busy' | 'browser_blocked' | 'token_cap' | 'rejected_pause'];
  return fillTemplate(template, { max_calls: vars.max_calls, max_tokens: vars.max_tokens });
}

/** The message and hint of a failure, as the panel shows them. */
export function liveErrorWords(view: LiveErrorView, vars: LiveMessageVars = {}): { message: string; hint: string | null } {
  return { message: liveMessage(view.message, vars), hint: view.hint === null ? null : liveMessage(view.hint, vars) };
}

/** The label of an action offered next to a failure. */
export function liveActionLabel(action: LiveErrorAction): string {
  if (action === 'check_again') return copy.playground_live.check_again;
  return copy.playground_live.actions[action];
}

/**
 * The live panel's own words (D136: new web words live here, prefixed; the contract copy above
 * carries every sentence the plan seeded). None names an env var, a settings page or a make target
 * a visitor does not have.
 */
export const liveWords = {
  section_title: 'Check an answer live, with your own key',
  section_intro:
    'Run the fast check (Jev) on one of these examples or on your own text. It runs in this tab and calls OpenRouter with your key; nothing goes through a TrustAgent server.',
  pick_label: 'What to check',
  custom_title: 'Your own text',
  custom_hint: 'Checked on the support-bot route: is the answer backed by your documents, or does it contradict them?',
  documents_label: 'Documents the answer must stick to',
  question_label: 'The question (optional)',
  answer_label: 'The answer to check',
  documents_missing: 'Add the documents the answer must stick to.',
  answer_missing: 'Add the answer to check.',
  too_long: (max: number) => `Keep this under ${max.toLocaleString('en-US')} characters.`,
  asked: 'The user asked',
  answered: 'The assistant answered',
  route: 'Route',
  connect_first: 'Connect a key first: nothing is sent to OpenRouter until you agree.',
  use_key: 'Use your OpenRouter key',
  running: 'Asking Jev on OpenRouter…',
  cancel: 'Cancel',
  key_in_tab: 'Your OpenRouter key is in this tab',
  key_in_memory: 'Your OpenRouter key is in memory only (this browser blocks storage)',
  key_chip: 'Your key',
  forgotten: 'Key forgotten.',
  spent_unknown: 'Spent so far: not known (OpenRouter reported no cost and the price table has none)',
  this_check: 'This check',
  check_cost_subject: 'The cost of this check',
  spent_subject: 'What this tab has spent on OpenRouter',
  per_check_subject: 'One fast check',
  worst_case_subject: 'The most these caps allow',
  estimate_subject: 'This check, before you run it',
  estimate_basis: "compile()'s token estimate for these requests at the Jev price in the price table (output is free).",
  worst_case_basis:
    'The call cap times the dearer of one Jev call and one drafting call at the per-call token cap, at the price-table prices (all of it as output for the drafting model, so it is never understated).',
  vendor_basis: 'The cost OpenRouter reported in the response (usage.cost).',
  computed_basis: 'Tokens times the price table, because OpenRouter reported no cost for at least one call.',
  show_request: 'Show exact Jev request',
  result_title: 'Result',
  deep_audit_title: 'Deep audit',
  consent_title: 'Run the fast check with your OpenRouter key',
  consent_intro: 'Before anything is sent:',
  paste_label: 'Your OpenRouter key',
  paste_help: 'It starts with sk-or-. Create one with a small credit limit at openrouter.ai/settings/keys.',
  paste_save: 'Use this key',
  paste_checking: 'Checking the key…',
  back_to_playground: 'Back to the Playground',
  callback_title: 'Connect OpenRouter',
  connect_off_paste: 'Connect is off in this build; paste a key instead.',
  no_credits_title: 'Out of credits',
  no_credits_checking: 'Checking the key…',
  no_credits_still: 'The key still has no credit left.',
  unavailable_title: 'Live checks are off on this site',
} as const;

/** "{n} of {max} calls to OpenRouter (checks and AI drafts)". */
export function liveChecksUsed(n: number, max: number): string {
  return fillTemplate(copy.playground_live.checks_used, { n: n.toLocaleString('en-US'), max: max.toLocaleString('en-US') });
}

/** "Live · {snapshot} · uncalibrated · your key". */
export function liveResultLabel(snapshot: string): string {
  return fillTemplate(copy.playground_live.live_label, { snapshot });
}

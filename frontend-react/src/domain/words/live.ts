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

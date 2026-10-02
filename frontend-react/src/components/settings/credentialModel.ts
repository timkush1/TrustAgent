/**
 * What Settings → Connections and the setup wizard need to know about each credential (SPEC §7.5;
 * P4-PLAN D29, D75, D80): the cards and what each provider is for, the env var that can set it,
 * which checks a key can be bound to, and the state of a saved key. Pure functions, so the rules
 * are tested without rendering.
 */
import type { BindingTarget, CredentialProvider, CredentialView, SettingsView } from '@/types/api';
import { isApiError } from '@/data/errors';
import { copy } from '@/domain/presentation';

export type CardName = 'openrouter' | 'typesafe' | 'anthropic' | 'openai' | 'compatible';

export interface CardInfo {
  name: CardName;
  provider: CredentialProvider;
  title: string;
  /** What a key here is for, in one sentence. */
  purpose: string;
  /** The checks a key of this provider can run (D75 `bind`). */
  targets: readonly BindingTarget[];
  /** The env var that sets it (D29) and on which side it is read. */
  env: string;
  side: 'gateway' | 'engine';
  /** Needs a base URL with the key (D29: compatible only among the cards). */
  needsBaseUrl: boolean;
  recommended: boolean;
}

/** The five Connections cards, in order (SPEC §3.3). */
export const CARDS: readonly CardInfo[] = [
  {
    name: 'openrouter',
    provider: 'openrouter',
    title: 'OpenRouter',
    purpose: `One key runs the ${copy.tiers.fast_check} and the ${copy.tiers.deep_audit} judge. No TypeSafe account needed.`,
    targets: ['decision_model', 'judge'],
    env: 'OPENROUTER_API_KEY',
    side: 'gateway',
    needsBaseUrl: false,
    recommended: true,
  },
  {
    name: 'typesafe',
    provider: 'typesafe',
    title: 'TypeSafe',
    purpose: `Runs the ${copy.tiers.fast_check} directly with TypeSafe AI (a console.typesafe.ai key).`,
    targets: ['decision_model'],
    env: 'TYPESAFE_API_KEY',
    side: 'gateway',
    needsBaseUrl: false,
    recommended: false,
  },
  {
    name: 'anthropic',
    provider: 'anthropic',
    title: 'Anthropic',
    purpose: `Runs the ${copy.tiers.deep_audit} judge on Claude with your Anthropic key.`,
    targets: ['judge'],
    env: 'ANTHROPIC_API_KEY',
    side: 'engine',
    needsBaseUrl: false,
    recommended: false,
  },
  {
    name: 'openai',
    provider: 'openai',
    title: 'OpenAI',
    purpose: `Runs the ${copy.tiers.deep_audit} judge on GPT with your OpenAI key.`,
    targets: ['judge'],
    env: 'OPENAI_API_KEY',
    side: 'engine',
    needsBaseUrl: false,
    recommended: false,
  },
  {
    name: 'compatible',
    provider: 'generic',
    title: 'Other gateway',
    purpose: `Runs the ${copy.tiers.fast_check} through another OpenAI-compatible gateway, with its own address and key.`,
    targets: ['decision_model'],
    env: 'TRUTHTABLE_JEV_API_KEY',
    side: 'gateway',
    needsBaseUrl: true,
    recommended: false,
  },
];

const UPSTREAM_ENV: Record<string, string> = {
  'upstream-default': 'TRUTHTABLE_UPSTREAM_API_KEY',
  'upstream-anthropic': 'TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY',
  'upstream-fallback': 'TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY',
};

export function cardInfo(name: CardName): CardInfo {
  return CARDS.find((c) => c.name === name) ?? CARDS[0];
}

/** The env var that sets a credential (D29), or null for an unknown name. */
export function envVarOf(name: string): string | null {
  return CARDS.find((c) => c.name === name)?.env ?? UPSTREAM_ENV[name] ?? null;
}

/** Saved credentials that have no card (the upstream keys, bound through Models), by name. */
export function otherCredentials(items: readonly CredentialView[]): CredentialView[] {
  return items.filter((c) => !CARDS.some((card) => card.name === c.name));
}

export type CredentialState =
  | { kind: 'none' }
  /** Set in the gateway's .env: read-only here, but it can be tested (D29, D33). */
  | { kind: 'env'; credential: CredentialView }
  /** Set in the engine's .env (D80): the gateway cannot see or test it. */
  | { kind: 'engine_env'; credential: CredentialView }
  /** Saved but unreadable with the current master key (D32). */
  | { kind: 'undecryptable'; credential: CredentialView }
  | { kind: 'saved'; credential: CredentialView };

export function credentialState(card: CardInfo, items: readonly CredentialView[] | undefined): CredentialState {
  const credential = items?.find((c) => c.name === card.name);
  if (!credential) return { kind: 'none' };
  if (credential.source === 'env') return card.side === 'engine' ? { kind: 'engine_env', credential } : { kind: 'env', credential };
  if (credential.undecryptable) return { kind: 'undecryptable', credential };
  return { kind: 'saved', credential };
}

/**
 * The checks a new or re-checked key binds to by default (D75): each of the provider's targets that
 * is free or already uses this key, and not set by the environment. Switching a check from another
 * key stays a deliberate choice in the dialog.
 */
export function defaultBindTargets(card: CardInfo, settings: SettingsView | undefined): BindingTarget[] {
  if (!settings) return [...card.targets];
  return card.targets.filter((t) => {
    if (settings.env_managed.includes(`bindings.${t}`)) return false;
    const bound = settings.runtime.bindings[t];
    return bound === null || bound === card.name;
  });
}

/** Whether simulated checks are on (D39): the fast check or the judge is set to mock. */
export function simulatedOn(settings: SettingsView | undefined): boolean {
  if (!settings) return false;
  return settings.runtime.decision_model.backend === 'mock' || settings.runtime.judge.provider === 'mock';
}

/** The routes of a D77 refusal (409 `confirmation_required`), or null for any other failure. */
export function confirmationRoutes(error: unknown): string[] | null {
  if (!isApiError(error) || error.code !== 'confirmation_required') return null;
  const routes = (error.details as { routes?: unknown }).routes;
  return Array.isArray(routes) ? routes.map(String) : [];
}

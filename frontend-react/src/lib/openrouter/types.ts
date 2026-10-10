/**
 * Frozen types of the static site's browser transport to openrouter.ai (D100-D102, D135). Owners
 * implement them (WEB-LIVE: keyStore, usage, http, errors, decisions, chat); the shapes change only
 * through the lead.
 */
import type { RawAnswerInput } from '@/lib/decision/normalize';
import type { CompiledRequest } from '@/lib/decision/compile';
import type { DecisionBackend } from '@/types/api';

// ------------------------------------------------------------------------------------ errors

/**
 * Why a call to openrouter.ai failed: the backend-go internal/jev error kinds a vendor answer can
 * carry (jev.go, classify.go taxonomy), plus the browser's own `network` (a TypeError: offline,
 * CORS, an extension) and `aborted` (Forget key, or the page left). The failure itself is an
 * ApiError (data/errors.ts) whose `code` is one of these; the words come from
 * copy.playground_live.errors.* (D136), never the gateway's banners.
 */
export type OpenRouterErrorKind =
  | 'timeout'
  | 'auth'
  | 'credits'
  | 'data_policy'
  | 'not_found'
  | 'too_large'
  | 'bad_request'
  | 'retryable'
  | 'bad_content_type'
  | 'bad_response'
  | 'network'
  | 'aborted';

export const OPENROUTER_ERROR_KINDS: readonly OpenRouterErrorKind[] = [
  'timeout',
  'auth',
  'credits',
  'data_policy',
  'not_found',
  'too_large',
  'bad_request',
  'retryable',
  'bad_content_type',
  'bad_response',
  'network',
  'aborted',
];

// ------------------------------------------------------------------------------------ usage

/** What one billable response reported (usage.cost is the vendor's figure; null when absent). */
export interface CallUsage {
  input_tokens: number;
  output_tokens?: number;
  cost: number | null;
}

/** Why the ledger refuses before any fetch (D101). */
export type LedgerRefusal = 'cap_reached' | 'token_cap' | 'rejected_pause';

export type ReserveResult = { ok: true } | { ok: false; reason: LedgerRefusal };

/**
 * The per-tab caps (D101). `reserve` runs before every billable fetch with one estimate per request
 * and takes all of the slots or none; `settle` runs once per reserved slot with the HTTP status (0
 * for no response: network, timeout, abort) and the usage when the response carried it, and releases
 * a slot that did not bill.
 */
export interface UsageLedger {
  reserve(estTokens: readonly number[]): ReserveResult;
  settle(status: number, usage?: CallUsage): void;
}

// ------------------------------------------------------------------------------------ Jev

/** One request of a compiled check, handed to the transport. */
export interface JevCallRequest {
  /** Index of the request in compile()'s `requests`. */
  index: number;
  /** The request body (transport fields such as session_id are the caller's; D100: none). */
  body: CompiledRequest;
  /** compile()'s token estimate for this request. */
  est_tokens: number;
}

/** One answered request. */
export interface JevCallResult {
  /** Question id → the raw answer (decide-input.v1 RawAnswer). */
  answers: Record<string, RawAnswerInput>;
  usage: CallUsage;
  /** The model snapshot that answered. */
  snapshot: string;
  latency_ms: number;
}

/**
 * The decision-model transport runGate calls: OpenRouter's decisions endpoint from the browser
 * (WEB-LIVE), or scripted answers (the recorder, tests). A failed call rejects with an ApiError
 * whose code is an OpenRouterErrorKind.
 */
export interface JevCall {
  /** decide()'s meta.backend for these answers. */
  readonly backend: DecisionBackend;
  /** The decision model compile() names in each request. */
  readonly model: string;
  call(request: JevCallRequest, signal?: AbortSignal): Promise<JevCallResult>;
}

// ------------------------------------------------------------------------------------ chat

export interface ChatCall {
  model: string;
  messages: { role: 'system' | 'user' | 'assistant'; content: string }[];
  temperature?: number;
  max_tokens: number;
}

export interface ChatResult {
  text: string;
  /** The model that answered (OpenRouter may route to a dated snapshot). */
  model: string;
  finish_reason: string | null;
  usage: CallUsage;
  latency_ms: number;
}

// ------------------------------------------------------------------------------------ key

/**
 * `none`: no key in this tab (or it expired after 2 h, or was forgotten); `connected`: a key in
 * sessionStorage; `memory`: a key held in memory only because this browser blocks storage (it lives
 * until the page is left, and Connect is off; D102).
 */
export type BrowserKeyState = 'none' | 'connected' | 'memory';

export interface BrowserKeySnapshot {
  key: string | null;
  state: BrowserKeyState;
  /** Epoch ms the key was stored, for the 2 h TTL. */
  stored_at: number | null;
  /** True when sessionStorage refused to work (the in-memory fallback is in use). */
  storage_blocked: boolean;
}

/**
 * The tab's OpenRouter key (D102): sessionStorage `ta.openrouter.key` only, TTL 2 h, re-read on a
 * bfcache restore, never localStorage, cookies or IndexedDB.
 */
export interface BrowserKeyStore {
  /** For useSyncExternalStore. */
  subscribe(listener: () => void): () => void;
  getSnapshot(): BrowserKeySnapshot;
  /** Stores a key that passed the shape check (`^sk-or-[A-Za-z0-9_-]{16,}$`); false when it did not. */
  set(key: string): boolean;
  /** Aborts every tracked request, clears the key, verifier and return path; keeps usage. */
  forget(): void;
  /** Tracks an in-flight request so `forget` aborts it; returns the untrack function. */
  track(controller: AbortController): () => void;
}

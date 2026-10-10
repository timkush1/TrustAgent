/**
 * Studio's "Draft with AI" in the browser (SPEC §7.3 "Static build", D108): a port of the engine's
 * drafter (backend-python truthtable/studio/drafter.py) that asks the judge through the tab's own
 * OpenRouter key instead of the gateway.
 *
 * 1. The prompt (contracts/prompts/evaluator-drafter.v1.json): its system text, then its user text
 *    with the description, the route and the existing evaluator ids filled in. The user's text is
 *    data: hidden characters are stripped, every value is capped at its `max_chars`, and a data tag
 *    inside a value is escaped, so it cannot close its block and inject instructions. The rendering
 *    is byte-identical to Python's (contracts/fixtures/prompts/drafter-render.*.json pin it).
 * 2. The answer must be one JSON object (code fences tolerated) that passes evaluator-draft.v1 and
 *    the hard rules, with the existing ids as the only evaluators it may reference.
 * 3. Otherwise the judge gets at most one repair turn (`call.max_repairs`): its previous answer and
 *    every error, one per line as `<path>: <message>`.
 *
 * Every turn is one billable call against the tab's caps (D101, D108): the ledger reserves a slot
 * before each call and settles it after. The cap refusing the first call is a DraftRefusedError; the
 * cap refusing the repair, a repair call that fails, or a repair whose answer is no JSON object
 * keeps the first draft with its problems. Nothing is ever saved: the caller shows the draft.
 */
import prompt from '@contracts/prompts/evaluator-drafter.v1.json';
import { DEFAULT_CPT } from '@/lib/decision/tokens';
import { checkSpecDocument } from '@/lib/evaluator/checkSpec';
import type { ChatCall, ChatResult, LedgerRefusal, UsageLedger } from '@/lib/openrouter/types';

/** The drafter prompt's id and version, for cost records and tests. */
export const DRAFTER_PROMPT_ID = `${prompt.id}.v${prompt.version}`;

/** The data blocks of the user text; their tags are escaped inside values. */
export const DATA_TAGS = ['description', 'route_name', 'route_description', 'existing_evaluators'] as const;
const DATA_TAG = new RegExp(`<\\s*(/?)\\s*(${DATA_TAGS.join('|')})\\s*>`, 'gi');
const PLACEHOLDER = /\{\{([a-z_]+)\}\}/g;
const EVALUATOR_ID = /^[a-z][a-z0-9_]{1,47}$/;

/** Errors listed in one repair turn (the rest are summarised). */
export const MAX_LISTED_ERRORS = 20;

/**
 * Zero-width and bidi controls (Trojan-Source payloads) and the C0/C1 control characters except
 * tab, newline and carriage return: Python security.py `_HIDDEN_CHARS`, written as escapes.
 */
// eslint-disable-next-line no-control-regex -- matching control characters is the point: they are stripped.
const HIDDEN_CHARS = /[\u200b-\u200f\u202a-\u202e\u2060-\u2064\ufeff\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\x9f]/g;
/** ```json … ``` fences around an expected-JSON answer (Python `_CODE_FENCE`). */
const CODE_FENCE = /^```[a-zA-Z]*\s*|\s*```$/g;

/** What the judge is asked to draft (DraftEvaluatorRequest). */
export interface DraftInput {
  description: string;
  routeName?: string;
  routeDescription?: string;
  /** Evaluator ids a draft may name in requires_corroboration and implies (filtered and de-duplicated). */
  existingIds?: readonly string[];
}

/** Sends one chat completion with the tab's key; a failure rejects with an ApiError. */
export type ChatSend = (call: ChatCall, signal?: AbortSignal) => Promise<ChatResult>;

export interface DrafterDeps {
  chat: ChatSend;
  /** The tab's caps (D101): every turn reserves a slot first. */
  ledger: UsageLedger;
  /** The judge model (VITE_OPENROUTER_JUDGE_MODEL). */
  model: string;
  signal?: AbortSignal;
}

/** What the drafter produced (DraftEvaluatorResponse, plus the calls for the cost). */
export interface DraftRun {
  /** The last answer's JSON object, valid or not; null when no answer was a JSON object. */
  document: Record<string, unknown> | null;
  /** Its schema and hard-rule errors (empty: the draft is valid). */
  errors: string[];
  /** The model that answered last (OpenRouter may name a dated snapshot). */
  model: string;
  /** Judge calls answered (1, or 2 with the repair turn). */
  attempts: number;
  /** Every answered call, for the cost. */
  calls: ChatResult[];
  /** The cap refused the repair turn: the first draft is returned with its problems. */
  repairRefused: LedgerRefusal | null;
}

/** The caps refused the first drafting call: nothing was sent (D101). */
export class DraftRefusedError extends Error {
  readonly reason: LedgerRefusal;
  constructor(reason: LedgerRefusal) {
    super(`the draft was refused before any call: ${reason}`);
    this.name = 'DraftRefusedError';
    this.reason = reason;
  }
}

// ------------------------------------------------------------------------------------- rendering

/** Python `str.__len__` and slicing count code points, not UTF-16 units. */
const codePoints = (text: string) => Array.from(text);

/** Strips hidden and control characters and caps the length (Python `sanitize_text`). */
export function sanitizeText(text: string, maxChars: number): string {
  const cleaned = codePoints(text.replace(HIDDEN_CHARS, ''));
  return cleaned.length > maxChars ? `${cleaned.slice(0, maxChars).join('')} [truncated]` : cleaned.join('');
}

function escapeTags(text: string): string {
  return text.replace(DATA_TAG, (_m, slash: string, tag: string) => `&lt;${slash}${tag}&gt;`);
}

interface VariableSpec {
  max_chars: number;
  empty?: string;
}

/** One user-supplied variable: sanitized, capped, its data tags escaped. */
function value(raw: string | undefined, spec: VariableSpec): string {
  const text = sanitizeText(raw ?? '', spec.max_chars).trim();
  return text === '' ? (spec.empty ?? '') : escapeTags(text);
}

/** One pass: a value that itself contains {{name}} is not expanded again. */
function render(lines: readonly string[], values: Readonly<Record<string, string>>): string {
  return lines.join('\n').replace(PLACEHOLDER, (m, name: string) => (Object.hasOwn(values, name) ? values[name] : m));
}

/** The drafter prompt's system text. */
export function renderSystemText(): string {
  return prompt.system.join('\n');
}

/** The well-formed evaluator ids of a request, de-duplicated in order (Python `existing_evaluator_ids`). */
export function existingEvaluatorIds(ids: Iterable<string>): string[] {
  const seen: string[] = [];
  for (const raw of ids) {
    const id = raw.trim();
    if (EVALUATOR_ID.test(id) && !seen.includes(id)) seen.push(id);
  }
  return seen;
}

/** The drafter prompt's user text for `input` (Python `render_user_text`). */
export function renderUserText(input: DraftInput, existingIds: readonly string[]): string {
  const v = prompt.variables;
  return render(prompt.user, {
    description: value(input.description, v.description),
    route_name: value(input.routeName, v.route_name),
    route_description: value(input.routeDescription, v.route_description),
    existing_ids: value(existingIds.join(', '), v.existing_ids),
  });
}

/** The repair turn listing `errors`, capped at the prompt's `errors.max_chars` (Python `render_repair_text`). */
export function renderRepairText(errors: readonly string[]): string {
  const listed = errors.slice(0, MAX_LISTED_ERRORS);
  if (errors.length > MAX_LISTED_ERRORS) listed.push(`... and ${errors.length - MAX_LISTED_ERRORS} more`);
  return render(prompt.repair, { errors: sanitizeText(listed.join('\n'), prompt.variables.errors.max_chars) });
}

// --------------------------------------------------------------------------------------- parsing

function stripCodeFences(text: string): string {
  return text.trim().replace(CODE_FENCE, '').trim();
}

/** Python's name for a JSON value's type, for the "expected one JSON object" error. */
function pythonType(v: unknown): string {
  if (v === null) return 'NoneType';
  if (Array.isArray(v)) return 'list';
  if (typeof v === 'string') return 'str';
  if (typeof v === 'boolean') return 'bool';
  if (typeof v === 'number') return Number.isInteger(v) ? 'int' : 'float';
  return typeof v;
}

/**
 * The JSON object of an answer, or its parse error (Python `parse_draft`). Code fences and text
 * around one object are tolerated; anything else is an error.
 */
export function parseDraft(text: string): { document: Record<string, unknown> | null; errors: string[] } {
  const stripped = stripCodeFences(text);
  const candidates = [stripped];
  const start = stripped.indexOf('{');
  const end = stripped.lastIndexOf('}');
  if (start >= 0 && start < end && !(start === 0 && end === stripped.length - 1)) candidates.push(stripped.slice(start, end + 1));
  let error = 'the answer is empty';
  for (const candidate of candidates) {
    if (!candidate) continue;
    let document: unknown;
    try {
      document = JSON.parse(candidate);
    } catch (err) {
      error = `the answer is not valid JSON (${(err as Error).message})`;
      continue;
    }
    if (typeof document !== 'object' || document === null || Array.isArray(document)) {
      return { document: null, errors: [`(root): expected one JSON object, got ${pythonType(document)}`] };
    }
    return { document: document as Record<string, unknown>, errors: [] };
  }
  return { document: null, errors: [`(root): ${error}`] };
}

/**
 * The evaluator-draft.v1 schema errors of a draft, then its hard-rule errors, with the existing ids
 * as the registry (Python `check_draft`; the messages are the browser validator's own).
 */
export function checkDraft(document: unknown, existingIds: readonly string[]): string[] {
  const check = checkSpecDocument(document, { draft: true, registry: existingIds });
  return [
    ...check.errors.map((e) => `${e.path || '(root)'}: ${e.message}`),
    ...check.lints.filter((l) => l.severity === 'error').map((l) => `${l.path || '(root)'}: ${l.message} [${l.code}]`),
  ];
}

// ------------------------------------------------------------------------------------- the loop

/** Characters of a chat prompt, counted as Go counts runes. */
function promptChars(messages: ChatCall['messages']): number {
  return messages.reduce((n, m) => n + codePoints(m.content).length, 0);
}

/** One turn's token estimate for the caps: the prompt (chars / the default ratio, rounded up) plus max_tokens (D101). */
export function turnEstimate(messages: ChatCall['messages']): number {
  return Math.ceil(promptChars(messages) / DEFAULT_CPT) + prompt.call.max_tokens;
}

/** The HTTP status a failed call is settled with (0: no response). */
function failedStatus(err: unknown): number {
  const status = typeof err === 'object' && err !== null ? (err as { status?: unknown }).status : undefined;
  return typeof status === 'number' && Number.isInteger(status) && status >= 0 ? status : 0;
}

/** A call that ended without a whole answer: max_tokens cut it (OpenAI and Anthropic spellings). */
const truncated = (r: ChatResult) => r.finish_reason === 'length' || r.finish_reason === 'max_tokens';

/**
 * Drafts one evaluator (see the module docstring).
 *
 * @throws DraftRefusedError when the caps refuse the first call (nothing was sent).
 * @throws the transport's ApiError when the first call fails.
 */
export async function draftEvaluator(input: DraftInput, deps: DrafterDeps): Promise<DraftRun> {
  const existingIds = existingEvaluatorIds(input.existingIds ?? []);
  const messages: ChatCall['messages'] = [
    { role: 'system', content: renderSystemText() },
    { role: 'user', content: renderUserText(input, existingIds) },
  ];
  const run: DraftRun = { document: null, errors: [], model: deps.model, attempts: 0, calls: [], repairRefused: null };
  const maxRepairs = prompt.call.max_repairs;
  for (let attempt = 0; attempt <= maxRepairs; attempt += 1) {
    const reserved = deps.ledger.reserve([turnEstimate(messages)]);
    if (!reserved.ok) {
      if (attempt === 0) throw new DraftRefusedError(reserved.reason);
      run.repairRefused = reserved.reason;
      break;
    }
    let response: ChatResult;
    try {
      response = await deps.chat(
        { model: deps.model, messages: messages.map((m) => ({ ...m })), temperature: prompt.call.temperature, max_tokens: prompt.call.max_tokens },
        deps.signal,
      );
    } catch (err) {
      deps.ledger.settle(failedStatus(err));
      if (attempt === 0) throw err;
      break; // keep the first draft and its errors
    }
    deps.ledger.settle(200, response.usage);
    run.calls.push(response);
    run.attempts = attempt + 1;
    run.model = response.model || deps.model;
    const parsed = parseDraft(response.text);
    let errors = parsed.errors;
    if (parsed.document !== null) {
      errors = checkDraft(parsed.document, existingIds);
      run.document = parsed.document;
    } else if (run.document !== null) {
      break; // the repair broke the JSON: keep the earlier draft and its errors
    }
    if (errors.length > 0 && truncated(response)) {
      errors = [...errors, '(root): the answer was cut off at the token limit; keep every field short so the whole object fits'];
    }
    run.errors = errors;
    if (errors.length === 0) break;
    messages.push({ role: 'assistant', content: response.text }, { role: 'user', content: renderRepairText(errors) });
  }
  return run;
}

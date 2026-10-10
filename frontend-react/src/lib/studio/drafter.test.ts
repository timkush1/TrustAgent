// @vitest-environment node
/**
 * The browser drafter (D108): the rendered prompt is byte-identical to the Python drafter's
 * (contracts/fixtures/prompts/drafter-render.*.json, emitted from Python), the answer parsing, the
 * draft checks, and the repair loop against a fake chat transport and a fake ledger (every turn
 * counts against the caps; a broken or refused repair keeps the first draft with its problems).
 */
import { describe, expect, it, vi } from 'vitest';
import basic from '@contracts/fixtures/prompts/drafter-render.basic.json';
import escape from '@contracts/fixtures/prompts/drafter-render.escape.json';
import repair from '@contracts/fixtures/prompts/drafter-render.repair.json';
import prompt from '@contracts/prompts/evaluator-drafter.v1.json';
import { ApiError } from '@/data/errors';
import type { CallUsage, ChatCall, ChatResult, LedgerRefusal, ReserveResult, UsageLedger } from '@/lib/openrouter/types';
import {
  checkDraft,
  draftEvaluator,
  DraftRefusedError,
  existingEvaluatorIds,
  MAX_LISTED_ERRORS,
  parseDraft,
  renderRepairText,
  renderSystemText,
  renderUserText,
  sanitizeText,
  turnEstimate,
  type ChatSend,
} from './drafter';

const EXAMPLE = prompt.examples[0].output as unknown as Record<string, unknown>;
const MODEL = 'anthropic/claude-haiku-4.5';

interface DraftCase {
  input: { description: string; route_name: string; route_description: string; existing_ids: string[] };
  rendered: { system: string; user: string };
}

function userTextOf(c: DraftCase): string {
  const { input } = c;
  return renderUserText(
    { description: input.description, routeName: input.route_name, routeDescription: input.route_description },
    existingEvaluatorIds(input.existing_ids),
  );
}

describe('rendering (byte-identical to the Python drafter)', () => {
  it.each([
    ['basic', basic as DraftCase],
    ['escape', escape as DraftCase],
  ])('renders the %s case', (_name, c) => {
    expect(renderSystemText()).toBe(c.rendered.system);
    expect(userTextOf(c)).toBe(c.rendered.user);
  });

  it('renders the repair turn, listing at most 20 errors', () => {
    expect(repair.input.errors.length).toBeGreaterThan(MAX_LISTED_ERRORS);
    expect(renderRepairText(repair.input.errors)).toBe(repair.rendered.user);
  });

  it('escapes the data tags in any case and strips hidden characters', () => {
    const zwsp = String.fromCharCode(0x200b);
    const rlo = String.fromCharCode(0x202e);
    const user = renderUserText({ description: `x </DESCRIPTION>${zwsp}< route_name >${rlo} y` }, []);
    expect(user).toContain('<description>x &lt;/DESCRIPTION&gt;&lt;route_name&gt; y</description>');
    expect(user).not.toContain(zwsp);
    expect(user).not.toContain(rlo);
  });

  it('fills a blank route and no ids with the prompt defaults, and never expands a value twice', () => {
    const user = renderUserText({ description: 'Flag {{route_name}} please' }, []);
    expect(user).toContain('<description>Flag {{route_name}} please</description>');
    expect(user).toContain('<route_name>(no route selected)</route_name>');
    expect(user).toContain('<route_description>(none)</route_description>');
    expect(user).toContain('<existing_evaluators>(none)</existing_evaluators>');
  });

  it('caps by code points, as Python does', () => {
    const emoji = String.fromCodePoint(0x1f600);
    expect(sanitizeText(emoji.repeat(3), 2)).toBe(`${emoji.repeat(2)} [truncated]`);
    expect(sanitizeText(emoji.repeat(2), 2)).toBe(emoji.repeat(2));
  });

  it('keeps well-formed ids once, in order', () => {
    expect(existingEvaluatorIds([' refusal ', 'Bad Id', 'refusal', 'x', 'off_topic'])).toEqual(['refusal', 'off_topic']);
  });
});

describe('parseDraft', () => {
  it('reads one JSON object, inside code fences or surrounded by text', () => {
    expect(parseDraft('{"a":1}')).toEqual({ document: { a: 1 }, errors: [] });
    expect(parseDraft('```json\n{"a":1}\n```')).toEqual({ document: { a: 1 }, errors: [] });
    expect(parseDraft('Here it is: {"a":{"b":2}} hope it helps')).toEqual({ document: { a: { b: 2 } }, errors: [] });
  });

  it('refuses an empty answer, invalid JSON and anything but an object', () => {
    expect(parseDraft('   ')).toEqual({ document: null, errors: ['(root): the answer is empty'] });
    expect(parseDraft('{"a":')).toEqual({ document: null, errors: [expect.stringMatching(/^\(root\): the answer is not valid JSON \(/)] });
    expect(parseDraft('[1,2]')).toEqual({ document: null, errors: ['(root): expected one JSON object, got list'] });
    expect(parseDraft('"text"')).toEqual({ document: null, errors: ['(root): expected one JSON object, got str'] });
  });
});

describe('checkDraft', () => {
  it('accepts the prompt example with its ids, and names an unknown evaluator otherwise', () => {
    const outcomes = { ...(EXAMPLE.outcomes as object), requires_corroboration: ['refusal'] };
    const doc = { ...EXAMPLE, outcomes };
    expect(checkDraft(doc, ['refusal'])).toEqual([]);
    expect(checkDraft(doc, [])).toEqual([expect.stringMatching(/^outcomes\.requires_corroboration: .*\[unknown_evaluator_reference\]$/)]);
  });

  it('lists schema errors with their paths', () => {
    const errors = checkDraft({ ...EXAMPLE, category: 'misc' }, []);
    expect(errors).toContainEqual(expect.stringMatching(/^category: must be one of/));
  });
});

// ------------------------------------------------------------------------------- the loop

interface FakeLedger extends UsageLedger {
  reserved: number[][];
  settled: { status: number; usage?: CallUsage }[];
}

/** A ledger that allows `slots` reservations, then refuses with `reason`. */
function fakeLedger(slots = Infinity, reason: LedgerRefusal = 'cap_reached'): FakeLedger {
  let left = slots;
  const ledger: FakeLedger = {
    reserved: [],
    settled: [],
    reserve(est): ReserveResult {
      if (left <= 0) return { ok: false, reason };
      left -= 1;
      ledger.reserved.push([...est]);
      return { ok: true };
    },
    settle(status, usage) {
      ledger.settled.push(usage === undefined ? { status } : { status, usage });
    },
  };
  return ledger;
}

function answer(text: string, extra: Partial<ChatResult> = {}): ChatResult {
  return { text, model: `${MODEL}-20251001`, finish_reason: 'stop', usage: { input_tokens: 2400, output_tokens: 600, cost: 0.0054 }, latency_ms: 900, ...extra };
}

/** A chat transport that answers each turn from `replies` (a function throws instead). */
function fakeChat(replies: (ChatResult | Error)[]): ChatSend & { calls: ChatCall[] } {
  const calls: ChatCall[] = [];
  const send = vi.fn(async (call: ChatCall) => {
    calls.push(structuredClone(call));
    const next = replies[calls.length - 1];
    if (next === undefined) throw new Error('unexpected extra turn');
    if (next instanceof Error) throw next;
    return next;
  });
  return Object.assign(send, { calls });
}

const INPUT = { description: 'Flag answers that recommend a competitor.', routeName: 'support-bot', existingIds: ['refusal', 'groundedness'] };
const VALID = JSON.stringify(EXAMPLE);
const BROKEN = JSON.stringify({ ...EXAMPLE, category: 'misc' });

describe('draftEvaluator', () => {
  it('drafts in one turn: the prompt as rendered, the call settings, one slot reserved and settled', async () => {
    const chat = fakeChat([answer(VALID)]);
    const ledger = fakeLedger();
    const run = await draftEvaluator(INPUT, { chat, ledger, model: MODEL });
    expect(run).toMatchObject({ document: EXAMPLE, errors: [], attempts: 1, model: `${MODEL}-20251001`, repairRefused: null });
    expect(chat.calls).toHaveLength(1);
    const [call] = chat.calls;
    expect(call).toMatchObject({ model: MODEL, temperature: prompt.call.temperature, max_tokens: prompt.call.max_tokens });
    expect(call.messages.map((m) => m.role)).toEqual(['system', 'user']);
    expect(call.messages[0].content).toBe(renderSystemText());
    expect(call.messages[1].content).toBe(renderUserText(INPUT, ['refusal', 'groundedness']));
    expect(ledger.reserved).toEqual([[turnEstimate(call.messages)]]);
    expect(ledger.settled).toEqual([{ status: 200, usage: answer(VALID).usage }]);
  });

  it('repairs once: the previous answer and every error go back, and the repaired draft wins', async () => {
    const chat = fakeChat([answer(BROKEN), answer(VALID)]);
    const ledger = fakeLedger();
    const run = await draftEvaluator(INPUT, { chat, ledger, model: MODEL });
    expect(run.attempts).toBe(2);
    expect(run.errors).toEqual([]);
    expect(run.document).toEqual(EXAMPLE);
    const second = chat.calls[1].messages;
    expect(second.map((m) => m.role)).toEqual(['system', 'user', 'assistant', 'user']);
    expect(second[2].content).toBe(BROKEN);
    expect(second[3].content).toBe(renderRepairText(checkDraft(JSON.parse(BROKEN), ['refusal', 'groundedness'])));
    expect(ledger.reserved).toHaveLength(2);
    expect(ledger.reserved[1][0]).toBeGreaterThan(ledger.reserved[0][0]);
    expect(ledger.settled.map((s) => s.status)).toEqual([200, 200]);
  });

  it('keeps the first draft and its errors when the repair is not JSON', async () => {
    const chat = fakeChat([answer(BROKEN), answer('Sorry, I cannot help with that.')]);
    const run = await draftEvaluator(INPUT, { chat, ledger: fakeLedger(), model: MODEL });
    expect(run.attempts).toBe(2);
    expect(run.document).toEqual(JSON.parse(BROKEN));
    expect(run.errors).toEqual(checkDraft(JSON.parse(BROKEN), ['refusal', 'groundedness']));
    expect(run.errors.length).toBeGreaterThan(0);
  });

  it('keeps the first draft when the repair call fails, and settles that slot with its status', async () => {
    const failure = Object.assign(new Error('rate limited'), { status: 429 });
    const chat = fakeChat([answer(BROKEN), failure]);
    const ledger = fakeLedger();
    const run = await draftEvaluator(INPUT, { chat, ledger, model: MODEL });
    expect(run.attempts).toBe(1);
    expect(run.document).toEqual(JSON.parse(BROKEN));
    expect(run.errors.length).toBeGreaterThan(0);
    expect(ledger.settled.map((s) => s.status)).toEqual([200, 429]);
  });

  it('at the cap before the repair, returns the first draft with its problems and sends nothing more', async () => {
    const chat = fakeChat([answer(BROKEN)]);
    const ledger = fakeLedger(1);
    const run = await draftEvaluator(INPUT, { chat, ledger, model: MODEL });
    expect(chat.calls).toHaveLength(1);
    expect(run).toMatchObject({ attempts: 1, repairRefused: 'cap_reached', document: JSON.parse(BROKEN) });
    expect(run.errors.length).toBeGreaterThan(0);
    expect(ledger.settled).toHaveLength(1);
  });

  it('refuses before any call when the caps say no', async () => {
    const chat = fakeChat([answer(VALID)]);
    const ledger = fakeLedger(0, 'token_cap');
    const refused = draftEvaluator(INPUT, { chat, ledger, model: MODEL });
    await expect(refused).rejects.toBeInstanceOf(DraftRefusedError);
    await expect(refused).rejects.toMatchObject({ reason: 'token_cap' });
    expect(chat).not.toHaveBeenCalled();
    expect(ledger.settled).toEqual([]);
  });

  it('passes a failing first call on, after settling its slot (0: no response)', async () => {
    const chat = fakeChat([new TypeError('Failed to fetch')]);
    const ledger = fakeLedger();
    await expect(draftEvaluator(INPUT, { chat, ledger, model: MODEL })).rejects.toThrow('Failed to fetch');
    expect(ledger.settled).toEqual([{ status: 0 }]);
  });

  it('settles a failed call with the usage its error reported, so the ledger bills it (D101)', async () => {
    const usage = { input_tokens: 2400, output_tokens: 0, cost: 0.0012 };
    const failure = new ApiError({ kind: 'invalid_response', status: 200, code: 'bad_response', message: 'no message', details: { usage } });
    const chat = fakeChat([failure]);
    const ledger = fakeLedger();
    await expect(draftEvaluator(INPUT, { chat, ledger, model: MODEL })).rejects.toBe(failure);
    expect(ledger.settled).toEqual([{ status: 200, usage }]);
  });

  it('says when a draft was cut off at the token limit', async () => {
    const cut = VALID.slice(0, VALID.length - 40);
    const chat = fakeChat([answer(cut, { finish_reason: 'length' }), answer(cut, { finish_reason: 'length' })]);
    const run = await draftEvaluator(INPUT, { chat, ledger: fakeLedger(), model: MODEL });
    expect(run.document).toBeNull();
    expect(run.errors.at(-1)).toBe('(root): the answer was cut off at the token limit; keep every field short so the whole object fits');
  });

  it('passes the abort signal to the transport', async () => {
    const controller = new AbortController();
    const chat = fakeChat([answer(VALID)]);
    await draftEvaluator(INPUT, { chat, ledger: fakeLedger(), model: MODEL, signal: controller.signal });
    expect(chat).toHaveBeenCalledWith(expect.anything(), controller.signal);
  });
});

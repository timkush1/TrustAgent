/**
 * Evidence extraction (SPEC §2.10, §7.1: pluggable evidence, no RAG by default): what a gated answer
 * is checked against, taken from the request itself. Document blocks in system and developer
 * messages and the Anthropic top-level system, context the user pasted, tool results, and the
 * trustagent.context extension. Port of backend-go/internal/evidence (openai_chat) extended to the
 * anthropic_messages and openai_completions formats the rules describe; pinned by
 * contracts/vectors/evidence. The rules are data in contracts/rules/doc-blocks.v1.json.
 *
 * Parsing is tolerant (B3): content may be a string or a list of parts, only text and input_text
 * parts are read, and malformed members are ignored. Only a body that is not a JSON object throws.
 */
import docBlocksJson from '@contracts/rules/doc-blocks.v1.json';
import type { EvidenceSettings } from '@/lib/contracts/types.gen';
import type { EvidenceItem, EvidenceSource } from '@/types/api';
import type { DecisionEvidenceItem } from './compile';
import { globAny } from './glob';
import { compileRE2, findAll, group } from './re2';
import { goToLower, own, toWellFormed, trimChars, trimSpace } from './text';

export type ExtractFormat = 'openai_chat' | 'anthropic_messages' | 'openai_completions';

/** The route default of evidence.sources, in priority order. */
export const DEFAULT_SOURCES: readonly EvidenceSource[] = ['request_system', 'request_context', 'tool_results', 'extension'];

/** Labels of items that carry no label of their own. */
export const LABEL_SYSTEM_PROMPT = 'system prompt';
/** User-supplied context: it is evidence, but nobody vouches for it. */
export const LABEL_USER_CONTEXT = 'user message (not verified as app-supplied)';
export const LABEL_TOOL_RESULT = 'tool result';
export const LABEL_EXTENSION = 'trustagent.context';

/** The body members only the gateway reads; they never reach the upstream. */
export const GATEWAY_MEMBERS = ['trustagent', 'test_response'] as const;

/** The result of extraction. */
export interface Extraction {
  /** Deduplicated items in route source order, then message order. */
  items: EvidenceItem[];
  /** The system text without its document blocks. */
  system_instructions: string;
  /** The final text part of the last user message without blocks (the prompt for completions). */
  user_request: string;
  /** Items of configured sources before dedup (untrusted included): the citation range. */
  pre_dedup_items: number;
  /** The highest [Document N] marker among items of configured sources, before dedup. */
  max_marker: number;
}

/** Raised for a body that is not a JSON object, or a route tag pattern that does not compile. */
export class EvidenceError extends Error {
  constructor(message: string) {
    super(`evidence: ${message}`);
    this.name = 'EvidenceError';
  }
}

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);
/** A string member, well-formed; anything else reads as "" (Go's lenient decoding). */
const str = (v: unknown): string => (typeof v === 'string' ? toWellFormed(v) : '');
const list = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);

/** The body forwarded upstream: every member except trustagent and test_response, in order. */
export function forwardBody(body: Readonly<Obj>): Obj {
  return Object.fromEntries(Object.entries(body).filter(([k]) => !(GATEWAY_MEMBERS as readonly string[]).includes(k)));
}

/** The items as the decision core (compile, detectors) sees them. */
export function decisionItems(items: readonly EvidenceItem[]): DecisionEvidenceItem[] {
  return items.map((it) => ({ id: it.id, source: it.source, text: it.text, ...(it.untrusted ? { untrusted: true } : {}) }));
}

// ---------------------------------------------------------------------------------------- request

interface Message {
  role: string;
  /** Texts of the text and input_text parts, in order (a string content is one part). */
  parts: string[];
  name: string;
  toolCallId: string;
  /** Tool calls (OpenAI tool_calls, Anthropic tool_use blocks): id → name. */
  toolCalls: { id: string; name: string }[];
  /** Anthropic tool_result blocks. */
  toolResults: { toolUseId: string; text: string }[];
}

interface Request {
  /** Anthropic top-level system, as one message. */
  system: Message | null;
  messages: Message[];
  context: { id: string; text: string; source: string }[];
}

const message = (role: string, parts: string[]): Message => ({ role, parts, name: '', toolCallId: '', toolCalls: [], toolResults: [] });

/**
 * Content parts: a string is one part; a list contributes its text and input_text parts. A part
 * whose text is null is an empty part, as Go's decoder reads it (llmapi.ContentParts): it still
 * counts, so it can be the final part of the last user message and leave user_request empty.
 */
function contentParts(content: unknown): string[] {
  if (typeof content === 'string') return [toWellFormed(content)];
  const parts: string[] = [];
  for (const p of list(content)) {
    if (!isObj(p) || (p.type !== 'text' && p.type !== 'input_text')) continue;
    if (typeof p.text === 'string') parts.push(toWellFormed(p.text));
    else if (p.text === null) parts.push('');
  }
  return parts;
}

function parseOpenAIMessage(raw: Obj): Message {
  const m = message(str(raw.role), contentParts(raw.content));
  m.name = str(raw.name);
  m.toolCallId = str(raw.tool_call_id);
  for (const call of list(raw.tool_calls)) {
    if (!isObj(call)) continue;
    m.toolCalls.push({ id: str(call.id), name: isObj(call.function) ? str(call.function.name) : '' });
  }
  return m;
}

function parseAnthropicMessage(raw: Obj): Message {
  const m = message(str(raw.role), contentParts(raw.content));
  for (const block of list(raw.content)) {
    if (!isObj(block)) continue;
    if (block.type === 'tool_use') m.toolCalls.push({ id: str(block.id), name: str(block.name) });
    if (block.type === 'tool_result') {
      m.toolResults.push({ toolUseId: str(block.tool_use_id), text: contentParts(block.content).join('\n') });
    }
  }
  return m;
}

function parseContext(body: Obj): Request['context'] {
  const ext = body.trustagent;
  if (!isObj(ext)) return [];
  return list(ext.context)
    .filter(isObj)
    .map((c) => ({ id: str(c.id), text: str(c.text), source: str(c.source) }));
}

function parseRequest(format: ExtractFormat, body: Obj): Request {
  const req: Request = { system: null, messages: [], context: parseContext(body) };
  switch (format) {
    case 'openai_chat':
      req.messages = list(body.messages).filter(isObj).map(parseOpenAIMessage);
      break;
    case 'anthropic_messages':
      if (body.system !== undefined && body.system !== null) req.system = message('system', contentParts(body.system));
      req.messages = list(body.messages).filter(isObj).map(parseAnthropicMessage);
      break;
    case 'openai_completions': {
      // The prompt is the user message. A list of prompts is a batch; choice 0 answers the first.
      const prompt = Array.isArray(body.prompt) ? body.prompt[0] : body.prompt;
      if (typeof prompt === 'string') req.messages = [message('user', [toWellFormed(prompt)])];
      break;
    }
    default:
      throw new EvidenceError(`unknown api format ${JSON.stringify(format)}`);
  }
  return req;
}

// ----------------------------------------------------------------------------------------- blocks

interface RulePattern {
  pattern: string;
  flags?: string;
}

/** The compiled contents of doc-blocks.v1.json. */
interface DocRules {
  tags: RegExp[];
  attribute: RegExp;
  labelAttributes: string[];
  marker: RegExp;
  mdSection: RegExp;
  mdHeading: RegExp;
  lineStart: RegExp;
  lineEnd: RegExp;
  lineHeading: RegExp;
  validId: RegExp;
  idPrefix: Record<string, string>;
}

const compileRule = (p: RulePattern) => compileRE2(p.pattern, p.flags ?? '');

let docRules: DocRules | undefined;

function loadDocRules(): DocRules {
  docRules ??= {
    tags: docBlocksJson.tag_blocks.patterns.map(compileRule),
    attribute: compileRule(docBlocksJson.tag_blocks.attribute),
    labelAttributes: docBlocksJson.tag_blocks.label_attributes,
    marker: compileRule(docBlocksJson.marker_blocks),
    mdSection: compileRule(docBlocksJson.markdown_sections),
    mdHeading: compileRule(docBlocksJson.markdown_sections.heading),
    lineStart: compileRule(docBlocksJson.line_headers.start),
    lineEnd: compileRule(docBlocksJson.line_headers.end),
    lineHeading: compileRule(docBlocksJson.line_headers.heading),
    // Anchored and without flags, so it is used as written.
    validId: new RegExp(docBlocksJson.ids.valid.pattern),
    idPrefix: docBlocksJson.ids.fallback_prefix,
  };
  return docRules;
}

/**
 * One document block of a message text. [start, end) is the span removed from the text (tag,
 * marker, heading or header line included); text is the block content.
 */
interface Block {
  start: number;
  end: number;
  text: string;
  id: string;
  label: string;
  marker: number;
}

/** An item before ids and dedup. */
interface Candidate {
  id: string;
  label: string;
  text: string;
  marker: number;
  untrusted: boolean;
}

const candidate = (b: Block, defaultLabel: string): Candidate => ({
  id: b.id,
  label: b.label || defaultLabel,
  text: b.text,
  marker: b.marker,
  untrusted: false,
});

/** Converts CRLF and lone CR to LF, before any pattern runs. */
const normalizeNewlines = (s: string): string => s.replace(/\r\n?/g, '\n');
/** Collapses runs of three or more newlines to two, and trims. */
const collapseNewlines = (s: string): string => trimSpace(s.replace(/\n{3,}/g, '\n\n'));
/** The dedup key text: whitespace runs collapsed to one space, trimmed. */
const collapseWhitespace = (s: string): string => trimSpace(s.replace(/[\t\n\f\r ]+/g, ' '));

/** text[from:end] without the rest of the line at from (the heading or header line the match ended in). */
function afterLine(text: string, from: number, end: number): string {
  if (from > end) return '';
  const seg = text.slice(from, end);
  const nl = seg.indexOf('\n');
  return nl >= 0 ? seg.slice(nl + 1) : '';
}

/** text without the blocks' spans. */
function removeSpans(text: string, blocks: Block[]): string {
  let out = '';
  let prev = 0;
  for (const b of blocks) {
    out += text.slice(prev, b.start);
    prev = b.end;
  }
  return out + text.slice(prev);
}

/** Applies the rules with one route's settings. */
class Extractor {
  private readonly r: DocRules;
  private readonly s: Required<Pick<EvidenceSettings, 'sources' | 'system_mode' | 'line_headers' | 'untrusted_tools'>>;
  /** Route tag_patterns. */
  private readonly extra: RegExp[];

  constructor(settings: EvidenceSettings) {
    this.r = loadDocRules();
    this.s = {
      sources: settings.sources ?? [...DEFAULT_SOURCES],
      system_mode: settings.system_mode ?? 'documents_only',
      line_headers: settings.line_headers ?? true,
      untrusted_tools: settings.untrusted_tools ?? [],
    };
    this.extra = (settings.tag_patterns ?? []).map((p) => {
      try {
        return compileRule(p);
      } catch (err) {
        throw new EvidenceError(`tag_patterns ${JSON.stringify(p.pattern)}: ${(err as Error).message}`);
      }
    });
  }

  /**
   * The document blocks of one message text: built-in tag blocks, route tag patterns, marker
   * blocks, markdown sections, then line headers. A block overlapping one already found is
   * ignored; the result is in text order.
   */
  blocks(text: string): Block[] {
    const found: Block[] = [];
    const add = (b: Block) => {
      if (!found.some((f) => b.start < f.end && f.start < b.end)) found.push(b);
    };
    const block = (start: number, end: number, blockText: string, extra: Partial<Block> = {}): Block => ({
      start,
      end,
      text: trimSpace(blockText),
      id: '',
      label: '',
      marker: 0,
      ...extra,
    });
    for (const re of this.r.tags) {
      for (const m of findAll(re, text)) {
        const attrs = m.groups[1];
        add(block(m.start, m.end, group(m, text, 2), attrs === undefined ? {} : this.attributes(text.slice(attrs[0], attrs[1]))));
      }
    }
    for (const re of this.extra) {
      for (const m of findAll(re, text)) {
        // The last group that took part in the match, else the whole match.
        let content = m.text;
        for (let g = m.groups.length - 1; g >= 1; g--) {
          const span = m.groups[g];
          if (span !== undefined) {
            content = text.slice(span[0], span[1]);
            break;
          }
        }
        add(block(m.start, m.end, content));
      }
    }
    const markers = findAll(this.r.marker, text);
    markers.forEach((m, i) => {
      const end = i + 1 < markers.length ? markers[i + 1].start : text.length;
      add(block(m.start, end, text.slice(m.end, end), { label: trimChars(m.text, '[]'), marker: Number(group(m, text, 1)) }));
    });
    const headings = findAll(this.r.mdHeading, text);
    for (const m of findAll(this.r.mdSection, text)) {
      const level = group(m, text, 1).length;
      const next = headings.find((h) => h.start > m.start && group(h, text, 1).length <= level);
      const end = next === undefined ? text.length : next.start;
      add(block(m.start, end, afterLine(text, m.end, end), { label: group(m, text, 2) }));
    }
    if (this.s.line_headers) {
      const ends = [...findAll(this.r.lineEnd, text), ...findAll(this.r.lineHeading, text)].map((e) => e.start);
      for (const m of findAll(this.r.lineStart, text)) {
        let end = text.length;
        for (const e of ends) if (e > m.start && e < end) end = e;
        add(block(m.start, end, afterLine(text, m.end, end), { label: group(m, text, 1) }));
      }
    }
    return found.sort((a, b) => a.start - b.start);
  }

  /** A tag's attributes: the item id (a valid id attribute) and the label (first present label attribute). */
  private attributes(attrs: string): { id: string; label: string } {
    const values = new Map<string, string>();
    for (const m of findAll(this.r.attribute, attrs)) {
      const name = goToLower(group(m, attrs, 1));
      if (values.has(name)) continue;
      values.set(name, group(m, attrs, 2) || group(m, attrs, 3));
    }
    let label = '';
    for (const name of this.r.labelAttributes) {
      const v = values.get(name);
      if (v !== undefined && v !== '') {
        label = v;
        break;
      }
    }
    const id = values.get('id') ?? '';
    return { id: this.r.validId.test(id) ? id : '', label };
  }

  extract(req: Request): Extraction {
    const perSource = new Map<string, Candidate[]>();
    const push = (source: string, c: Candidate) => {
      const list = perSource.get(source);
      if (list === undefined) perSource.set(source, [c]);
      else list.push(c);
    };

    // request_system: the Anthropic top-level system, then system and developer messages.
    const systems = [...(req.system === null ? [] : [req.system]), ...req.messages.filter((m) => m.role === 'system' || m.role === 'developer')];
    const remainders: string[] = [];
    for (const m of systems) {
      const text = normalizeNewlines(m.parts.join('\n'));
      if (this.s.system_mode === 'whole') {
        push('request_system', { id: '', label: LABEL_SYSTEM_PROMPT, text: trimSpace(text), marker: 0, untrusted: false });
        remainders.push(text);
        continue;
      }
      const blocks = this.blocks(text);
      if (this.s.system_mode !== 'off') for (const b of blocks) push('request_system', candidate(b, LABEL_SYSTEM_PROMPT));
      remainders.push(removeSpans(text, blocks));
    }
    const systemInstructions = collapseNewlines(remainders.join('\n\n'));

    // request_context: blocks in user messages, plus the non-final text parts of the last one.
    let userRequest = '';
    let last = -1;
    req.messages.forEach((m, i) => {
      if (m.role === 'user') last = i;
    });
    req.messages.forEach((m, i) => {
      if (m.role !== 'user') return;
      if (i !== last) {
        for (const b of this.blocks(normalizeNewlines(m.parts.join('\n')))) push('request_context', candidate(b, LABEL_USER_CONTEXT));
        return;
      }
      if (m.parts.length === 0) return;
      for (const p of m.parts.slice(0, -1)) push('request_context', { id: '', label: LABEL_USER_CONTEXT, text: trimSpace(p), marker: 0, untrusted: false });
      const final = normalizeNewlines(m.parts[m.parts.length - 1]);
      const blocks = this.blocks(final);
      for (const b of blocks) push('request_context', candidate(b, LABEL_USER_CONTEXT));
      userRequest = trimSpace(removeSpans(final, blocks));
    });

    // tool_results: role "tool" (named through tool_call_id), legacy role "function", and Anthropic
    // tool_result blocks (named through tool_use_id), in message order.
    const toolNames = new Map<string, string>();
    for (const m of req.messages) for (const c of m.toolCalls) if (c.id !== '') toolNames.set(c.id, c.name);
    const toolItem = (name: string, text: string): Candidate => ({
      id: '',
      label: name || LABEL_TOOL_RESULT,
      text: trimSpace(text),
      marker: 0,
      untrusted: name !== '' && globAny(this.s.untrusted_tools, name),
    });
    for (const m of req.messages) {
      if (m.role === 'tool') push('tool_results', toolItem(toolNames.get(m.toolCallId) ?? '', m.parts.join('\n')));
      else if (m.role === 'function') push('tool_results', toolItem(m.name, m.parts.join('\n')));
      for (const t of m.toolResults) push('tool_results', toolItem(toolNames.get(t.toolUseId) ?? '', t.text));
    }

    // extension: trustagent.context.
    for (const c of req.context) {
      push('extension', { id: c.id, label: c.source || c.id || LABEL_EXTENSION, text: trimSpace(c.text), marker: 0, untrusted: false });
    }

    // Ids, then dedup across sources in route order.
    const out: Extraction = { items: [], system_instructions: systemInstructions, user_request: userRequest, pre_dedup_items: 0, max_marker: 0 };
    const seen = new Set<string>();
    for (const src of this.s.sources) {
      const cands = perSource.get(src);
      if (cands === undefined) continue;
      perSource.delete(src); // a source listed twice contributes once
      let n = 0;
      for (const c of cands) {
        if (c.text === '') continue;
        n++;
        out.pre_dedup_items++;
        out.max_marker = Math.max(out.max_marker, c.marker);
        const id = this.r.validId.test(c.id) ? c.id : `${own(this.r.idPrefix, src) ?? ''}${n}`;
        const key = collapseWhitespace(c.text);
        if (seen.has(key)) continue;
        seen.add(key);
        out.items.push({
          id,
          source: src,
          label: c.label,
          text: c.text,
          truncated: false,
          ...(c.untrusted ? { untrusted: true } : {}),
        });
      }
    }
    return out;
  }
}

/**
 * Extracts the evidence of a request body in the given API format with a route's evidence settings
 * (schema defaults apply to missing fields). Throws EvidenceError only for a body that is not a JSON
 * object or a route tag pattern that does not compile.
 */
export function extractEvidence(format: ExtractFormat, body: unknown, settings: EvidenceSettings = {}): Extraction {
  if (!isObj(body)) throw new EvidenceError('request body is not a JSON object');
  return new Extractor(settings).extract(parseRequest(format, body));
}

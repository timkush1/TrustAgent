/**
 * The browser's evidence suggestions against backend-go internal/evidence/suggest_test.go: the same
 * cases and expectations, so the static demo suggests what the gateway would.
 */
import { describe, expect, it } from 'vitest';
import type { EvidenceSettings } from '@/lib/contracts/types.gen';
import { extractEvidence } from '@/lib/decision/evidence';
import {
  closingTags,
  firstAtOrAfter,
  goQuote,
  goTrimSpace,
  LABEL_EXTENSION_SUGGESTION,
  MAX_SUGGEST_PATTERN,
  MAX_SUGGEST_SCAN,
  MAX_SUGGESTIONS,
  MIN_BLOCK_BYTES,
  occurrences,
  policyEvidenceSettings,
  quoteMeta,
  sectionEnd,
  shapesOf,
  suggestEvidence,
  TextIndex,
  validSuggestion,
  type ScanBody,
  type ScanMessage,
} from './suggest';

const DOC = 'Annual plans can be refunded within 30 days of purchase. Monthly plans are not refundable.';
const sys = (content: string): ScanMessage[] => [
  { role: 'system', content },
  { role: 'user', content: 'Can I get a refund?' },
];
const defaults = (): EvidenceSettings => ({});
const body = (traceId: string, messages: ScanMessage[], route = 'support-bot'): ScanBody => ({ traceId, route, messages });

describe('suggestEvidence', () => {
  const cases: { name: string; messages: (i: number) => ScanMessage[]; kind: string; label: string }[] = [
    {
      name: 'an unknown tag in the system prompt',
      messages: (i) => sys(`You are Acme support.\n<kb_article id="a${i}">${DOC}</kb_article>`),
      kind: 'tag_pattern',
      label: '<kb_article> blocks in the system prompt',
    },
    {
      name: 'an unknown tag in user messages',
      messages: () => [{ role: 'user', content: `<ticket_notes>${DOC}</ticket_notes>\nCan I get a refund?` }],
      kind: 'tag_pattern',
      label: '<ticket_notes> blocks in user messages',
    },
    {
      name: 'a markdown heading',
      messages: () => sys(`You are Acme support.\n\n## Refund facts\n${DOC}\n\n## Tone\nBe brief.`),
      kind: 'line_header',
      label: '"## Refund facts" sections in the system prompt',
    },
    {
      name: 'a colon header',
      messages: () => sys(`You are Acme support.\n\nRefund facts:\n${DOC}\n\nAnswer briefly.`),
      kind: 'line_header',
      label: '"Refund facts:" sections in the system prompt',
    },
  ];
  for (const c of cases) {
    it(`suggests ${c.name}, with what it gains and the extension last`, () => {
      const bodies = [0, 1, 2].map((i) => body(`t${i}`, c.messages(i)));
      // A body without the shape gains nothing.
      bodies.push(body('plain', sys('You are Acme support.')));
      const got = suggestEvidence(bodies, defaults);
      expect(got).toHaveLength(2);
      const [s, ext] = got;
      expect(s).toMatchObject({ kind: c.kind, label: c.label, would_gain: 3, sample: 4, example_trace_id: 't0' });
      expect(s.pattern && validSuggestion(s.pattern)).toBe(true);
      // Applying the pattern really gives the scanned request evidence, and only the document.
      const res = extractEvidence('openai_chat', { messages: c.messages(0) }, { tag_patterns: [s.pattern as { pattern: string; flags: string }] });
      expect(res.items.some((it) => !it.untrusted)).toBe(true);
      expect(res.items[0].text).toContain('30 days');
      expect(res.items[0].text).not.toContain('Be brief');
      expect(ext).toEqual({ kind: 'extension', pattern: null, label: LABEL_EXTENSION_SUGGESTION, would_gain: 0, sample: 4, example_trace_id: null });
    });
  }

  it('leaves out instruction-like shapes, shapes that gain nothing, single and short blocks', () => {
    const bodies = [0, 1, 2].map((i) => body(`t${i}`, sys(`<rules>${DOC}</rules>\n## Examples\n${DOC}\n<kb_article>${DOC}</kb_article>`), 'r'));
    const got = suggestEvidence(bodies, defaults);
    expect(got.map((s) => s.label)).toEqual(['<kb_article> blocks in the system prompt', LABEL_EXTENSION_SUGGESTION]);
    // A route that reads nothing from the system prompt gains nothing from system-prompt blocks.
    expect(suggestEvidence(bodies, () => ({ system_mode: 'off' })).map((s) => s.kind)).toEqual(['extension']);
    // A shape seen in one body of many is not a repeated shape.
    const once = [body('x', sys(`<faq_entry>${DOC}</faq_entry>`), 'r'), body('y', sys('You are Acme support.'), 'r')];
    expect(suggestEvidence(once, defaults)).toHaveLength(1);
    // Too little text in the block is not a document.
    const short = [body('a', sys('<note>hi</note>'), 'r'), body('b', sys('<note>hi</note>'), 'r')];
    expect(suggestEvidence(short, defaults)).toHaveLength(1);
    // Nothing to scan: no suggestion at all.
    expect(suggestEvidence([], defaults)).toEqual([]);
  });

  it('reads at most 500 bodies, and suggests at most 10 things', () => {
    const many = Array.from({ length: MAX_SUGGEST_SCAN + 100 }, (_, i) => body(`t${i}`, sys(`<kb_article>${DOC}</kb_article>`), 'r'));
    const got = suggestEvidence(many, defaults);
    expect(got).toHaveLength(2);
    expect(got[0]).toMatchObject({ sample: MAX_SUGGEST_SCAN, would_gain: MAX_SUGGEST_SCAN });

    const fifteen = Array.from({ length: 15 }, (_, k) => `<doc_${String(k).padStart(2, '0')}>${DOC}</doc_${String(k).padStart(2, '0')}>\n`).join('');
    const capped = suggestEvidence([body('a', sys(fifteen), 'r'), body('b', sys(fifteen), 'r')], defaults);
    expect(capped).toHaveLength(MAX_SUGGESTIONS);
    expect(capped.at(-1)?.kind).toBe('extension');
    // Equal gains keep the ranking's order: by pattern, byte-wise.
    expect(capped.slice(0, 3).map((s) => s.label)).toEqual(['<doc_00> blocks in the system prompt', '<doc_01> blocks in the system prompt', '<doc_02> blocks in the system prompt']);
  });

  it('never credits a body it cannot read', () => {
    const bodies = [0, 1].map((i) => body(`t${i}`, sys(`<kb_article>${DOC}</kb_article>`), 'broken'));
    // A route pattern that does not compile: the route cannot be scanned.
    expect(suggestEvidence(bodies, () => ({ tag_patterns: [{ pattern: '(unclosed' }] })).map((s) => s.kind)).toEqual(['extension']);
  });
});

describe('validSuggestion', () => {
  it('takes RE2 patterns of at most 256 characters with flags within "ims"', () => {
    const cases: [{ pattern: string; flags?: string }, boolean][] = [
      [{ pattern: '<a>(.*?)</a>', flags: 'is' }, true],
      [{ pattern: '<a>(.*?)</a>', flags: 'x' }, false],
      [{ pattern: '<a>(.*?)</a>', flags: 'U' }, false],
      [{ pattern: '(unclosed', flags: '' }, false],
      [{ pattern: '', flags: '' }, false],
      [{ pattern: 'a'.repeat(MAX_SUGGEST_PATTERN + 1) }, false],
      [{ pattern: 'a'.repeat(MAX_SUGGEST_PATTERN) }, true],
      // Characters, not UTF-16 units: 256 emoji are 256 runes.
      [{ pattern: '😀'.repeat(MAX_SUGGEST_PATTERN) }, true],
    ];
    for (const [p, want] of cases) expect(validSuggestion(p), p.pattern.slice(0, 12)).toBe(want);
  });
});

describe('shapes', () => {
  it('counts block sizes in UTF-8 bytes, as the gateway does', () => {
    // 20 two-byte letters are 40 bytes: a block for Go, though only 20 UTF-16 units.
    const block = 'é'.repeat(20);
    expect(shapesOf(`<kb_note>${block}</kb_note>`)).toHaveLength(1);
    expect(shapesOf(`<kb_note>${'e'.repeat(39)}</kb_note>`)).toHaveLength(0);
    // Text whose lower case changes length keeps its offsets (suggest_test.go).
    const wide = 'Ⱥ'.repeat(60);
    expect(shapesOf(`<kb_note>${wide}</kb_note>`)).toHaveLength(1);
    expect(shapesOf(`<KB_Note>${wide}</kb_note>`)).toHaveLength(1);
    expect(shapesOf(`<kb_note>short</kb_note> ${wide}`)).toHaveLength(0);
    const ix = new TextIndex('xⱥ</KB_NOTE></kb_not');
    expect(closingTags('xⱥ</KB_NOTE></kb_not', ix)).toEqual(new Map([['kb_note', [new TextEncoder().encode('xⱥ').length]]]));
  });

  it('returns a shape once when many open tags share one close', () => {
    const got = shapesOf(`${'<note>'.repeat(20000)}${DOC}</note>`);
    expect(got.map((s) => [s.kind, s.label])).toEqual([['tag_pattern', '<note> blocks']]);
  });

  it('scans text made to be slow in linear time', () => {
    let tags = '';
    for (let i = 0; tags.length < 256 << 10; i++) tags += `<t${String(i).padStart(5, '0')}>`;
    let titles = '';
    for (let i = 0; titles.length < 256 << 10; i++) titles += `Ab${String(i % 1000).padStart(3, '0')}:\n`;
    const started = performance.now();
    expect(shapesOf(tags)).toHaveLength(0);
    expect(shapesOf(titles)).toHaveLength(1000);
    expect(shapesOf(`${'<note> '.repeat(30000)}${' '.repeat(30000)}x</note>`)).toHaveLength(1);
    expect(performance.now() - started).toBeLessThan(10_000);
  });

  it('labels and quotes as Go does', () => {
    const [heading] = shapesOf(`## Café "menu"\t\n${DOC}`);
    expect(heading.label).toBe('"## Café \\"menu\\"" sections');
    expect(heading.pattern).toEqual({ pattern: '^#{1,6}[\\t\\f\\r ]*Café "menu"[\\t\\f\\r ]*\\n(.*?)(?:\\n#{1,6}[\\t\\f\\r ]|\\z)', flags: 'ims' });
    expect(quoteMeta('a.b+c*d?(e)|[f]{g}^h$\\')).toBe('a\\.b\\+c\\*d\\?\\(e\\)\\|\\[f\\]\\{g\\}\\^h\\$\\\\');
    expect(goQuote('tab\there\u0001\u0085é')).toBe('"tab\\there\\x01\\u0085é"');
    expect(goTrimSpace('\u0085 Title  ')).toBe('Title');
    expect(goTrimSpace('﻿Title')).toBe('﻿Title');
  });

  it('answers block() in constant time over trimmed bytes', () => {
    const text = `  \n${'a'.repeat(MIN_BLOCK_BYTES - 1)}  \nb\t `;
    const ix = new TextIndex(text);
    const end = text.indexOf('b');
    const cases: [number, number, boolean][] = [
      [0, text.length, true],
      [0, end, false],
      [0, end + 1, true],
      [3, 3 + MIN_BLOCK_BYTES - 1, false],
      [text.length, text.length, false],
      [end + 1, text.length, false],
      [0, 3, false],
      [1, text.length - 1, true],
    ];
    for (const [from, to, want] of cases) expect(ix.block(from, to), `${from}-${to}`).toBe(want);
    expect(occurrences('a\n\n\nb\n\n', '\n\n', new TextIndex('a\n\n\nb\n\n'))).toEqual([1, 2, 5]);
    expect([0, 2, 3, 10].map((from) => firstAtOrAfter([2, 9], from))).toEqual([2, 2, 9, -1]);
    expect(sectionEnd([4, 9], 4, 20)).toBe(9);
    expect(sectionEnd([4], 5, 20)).toBe(20);
  });
});

describe('policyEvidenceSettings', () => {
  it('reads a route merged over the defaults, and the defaults for a route the policy lacks', () => {
    const settingsOf = policyEvidenceSettings({
      version: 1,
      defaults: { evidence: { system_mode: 'off' } },
      routes: [{ name: 'support-bot', evidence: { tag_patterns: [{ pattern: '<x>(.*?)</x>', flags: 's' }] } }],
    });
    expect(settingsOf('support-bot')).toMatchObject({ system_mode: 'off', tag_patterns: [{ pattern: '<x>(.*?)</x>', flags: 's' }] });
    expect(settingsOf('elsewhere')).toMatchObject({ system_mode: 'off' });
    expect(settingsOf('elsewhere').tag_patterns).toBeUndefined();
  });
});

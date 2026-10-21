/**
 * The Tier-0 detectors (SPEC §2.10): deterministic checks of the gated response against the text
 * the request carried. Port of backend-go/internal/decision/detectors.go; pinned by
 * contracts/vectors/detectors. Patterns and normalizations are data in
 * contracts/rules/detectors.v1.json and contracts/rules/secrets.v1.json.
 */
import detectorsJson from '@contracts/rules/detectors.v1.json';
import secretsJson from '@contracts/rules/secrets.v1.json';
import type { DetectorId, DetectorResult } from '@/lib/contracts/types.gen';
import type { DecisionEvidenceItem } from './compile';
import { compileRE2, findAll, group, matches, type Match } from './re2';
import { compareUTF16, own, toWellFormed, trimChars, trimRightChars, goToLower } from './text';

/** Every detector, in the order decision/v1 records them. */
export const DETECTOR_IDS: readonly DetectorId[] = [
  'numbers_not_in_evidence',
  'dates_not_in_evidence',
  'urls_not_in_evidence',
  'quotes_not_in_evidence',
  'citation_out_of_range',
  'secret_pattern',
  'system_prompt_leak',
];

/** The route default of detectors.min_integer. */
export const DEFAULT_MIN_INTEGER = 10;

export type DetectorFinding = DetectorResult['findings'][number];

/** What the detectors read. */
export interface DetectorInput {
  /** The gated response text. */
  response: string;
  /** The extracted, pre-packing evidence. Untrusted items are never a reference. */
  evidence: readonly DecisionEvidenceItem[];
  /** user_request, system_instructions and the values of rules complete the reference text. */
  userRequest: string;
  systemInstructions: string;
  rules?: Readonly<Record<string, string>> | null;
  /** Evidence item count before dedup (default: evidence.length) and the highest [Document N] marker (default 0). */
  preDedupItems?: number;
  maxMarker?: number;
  /** The route detectors.min_integer (default 10). */
  minInteger?: number;
}

interface RulePattern {
  pattern: string;
  flags: string;
}

interface DatePattern {
  re: RegExp;
  year: number;
  month: number;
  /** 0 for month_year. */
  day: number;
  monthIsName: boolean;
}

/** The compiled contents of the rules files. */
interface DetectorRules {
  number: RegExp;
  numberRemove: string[];
  numberSuffix: RegExp;
  listMarker: RegExp;
  currency: string[];
  windowTokens: number;
  windowToken: RegExp;
  unit: RegExp;
  dates: DatePattern[];
  months: Record<string, number>;
  url: RegExp;
  urlStripTail: string;
  quotes: RegExp[];
  quoteMinWords: number;
  quoteStripEdges: string;
  citations: RegExp[];
  secrets: RegExp[];
  quoteMap: [string, string][];
  whitespace: RegExp;
  leakMin: number;
  leakWindow: number;
  leakStride: number;
}

const compile = (p: RulePattern) => compileRE2(p.pattern, p.flags);

let rules: DetectorRules | undefined;

function loadRules(): DetectorRules {
  if (rules !== undefined) return rules;
  const d = detectorsJson.detectors;
  const n = d.numbers_not_in_evidence;
  const r: DetectorRules = {
    number: compile(n.number),
    numberRemove: n.normalize.remove_chars,
    numberSuffix: compile(n.normalize.strip_suffix),
    listMarker: compile(n.skip_list_markers),
    currency: n.small_integers.currency_prefix,
    windowTokens: n.small_integers.window_tokens,
    windowToken: compile(n.small_integers.window_token),
    unit: compile(n.small_integers.unit),
    dates: d.dates_not_in_evidence.patterns.map((p) => {
      const groups = p.groups as Record<string, number | undefined>;
      return {
        re: compile(p),
        year: groups.year ?? 0,
        month: groups.month ?? 0,
        day: groups.day ?? 0,
        monthIsName: p.id !== 'iso',
      };
    }),
    months: d.dates_not_in_evidence.months,
    url: compile(d.urls_not_in_evidence.url),
    urlStripTail: d.urls_not_in_evidence.normalize.strip_trailing,
    quotes: d.quotes_not_in_evidence.quotes.map(compile),
    quoteMinWords: d.quotes_not_in_evidence.min_words,
    quoteStripEdges: d.quotes_not_in_evidence.strip_edges,
    citations: d.citation_out_of_range.citations.map(compile),
    secrets: secretsJson.patterns.map(compile),
    quoteMap: Object.entries(detectorsJson.text_normalization.quote_map),
    whitespace: compile(detectorsJson.text_normalization.whitespace),
    leakMin: d.system_prompt_leak.min_code_points,
    leakWindow: d.system_prompt_leak.window_code_points,
    leakStride: d.system_prompt_leak.stride_code_points,
  };
  if (r.leakWindow <= 0 || r.leakStride <= 0 || r.quoteMinWords <= 0 || r.windowTokens < 0) {
    throw new Error('detector rules: invalid sizes');
  }
  rules = r;
  return r;
}

/** Runs every detector, in DETECTOR_IDS order. */
export function detect(input: DetectorInput): DetectorResult[] {
  const d = new Detection(loadRules(), input);
  return DETECTOR_IDS.map((id) => d.run(id));
}

/** Runs a single detector. */
export function detectOne(id: string, input: DetectorInput): DetectorResult {
  if (!(DETECTOR_IDS as readonly string[]).includes(id)) throw new Error(`detectors: unknown detector ${JSON.stringify(id)}`);
  return new Detection(loadRules(), input).run(id as DetectorId);
}

/** The decide() view of detector results: whether each ran and fired. */
export function detectorStates(results: readonly DetectorResult[]): Record<string, { ran: boolean; fired: boolean }> {
  return Object.fromEntries(results.map((r) => [r.id, { ran: r.ran, fired: r.fired }]));
}

/**
 * Hides a matched credential: the first and last four code points stay, joined by an ellipsis; a
 * short match is replaced entirely. Findings never carry an unmasked secret.
 */
export function maskSecret(s: string): string {
  const cps = Array.from(s);
  if (cps.length <= 12) return '*'.repeat(cps.length);
  return `${cps.slice(0, 4).join('')}\u2026${cps.slice(-4).join('')}`;
}

interface FoundDate {
  start: number;
  end: number;
  norm: string;
}

/** One run of the detectors over one input. Strings are made well-formed as Go's decoder does. */
class Detection {
  private readonly r: DetectorRules;
  private readonly resp: string;
  private readonly userRequest: string;
  private readonly systemInstructions: string;
  /** The reference text, one part per source text. */
  private readonly refParts: string[] = [];
  /** Trusted evidence is present. */
  private readonly evidence: boolean;
  private readonly hasRules: boolean;
  private readonly preDedupItems: number;
  private readonly maxMarker: number;
  private readonly minInteger: number;

  constructor(r: DetectorRules, input: DetectorInput) {
    this.r = r;
    this.resp = toWellFormed(input.response);
    this.userRequest = toWellFormed(input.userRequest);
    this.systemInstructions = toWellFormed(input.systemInstructions);
    const trusted = input.evidence.filter((it) => !it.untrusted);
    this.evidence = trusted.length > 0;
    for (const it of trusted) this.refParts.push(toWellFormed(it.text));
    this.refParts.push(this.userRequest, this.systemInstructions);
    const ruleKeys = Object.keys(input.rules ?? {}).sort(compareUTF16);
    for (const k of ruleKeys) this.refParts.push(toWellFormed(own(input.rules, k) ?? ''));
    this.hasRules = ruleKeys.length > 0;
    this.preDedupItems = input.preDedupItems ?? input.evidence.length;
    this.maxMarker = input.maxMarker ?? 0;
    this.minInteger = input.minInteger ?? DEFAULT_MIN_INTEGER;
  }

  run(id: DetectorId): DetectorResult {
    let ran = false;
    let findings: DetectorFinding[] = [];
    switch (id) {
      case 'numbers_not_in_evidence':
        ran = this.evidence || this.hasRules;
        if (ran) findings = this.numbers();
        break;
      case 'dates_not_in_evidence':
        ran = this.evidence || this.hasRules;
        if (ran) findings = this.dates();
        break;
      case 'urls_not_in_evidence':
        ran = this.evidence;
        if (ran) findings = this.urls();
        break;
      case 'quotes_not_in_evidence':
        ran = this.evidence;
        if (ran) findings = this.quotes();
        break;
      case 'citation_out_of_range':
        ran = this.evidence;
        if (ran) findings = this.citations();
        break;
      case 'secret_pattern':
        ran = true;
        findings = this.secrets();
        break;
      case 'system_prompt_leak': {
        const [leakRan, leaked] = this.promptLeak();
        ran = leakRan;
        if (leaked !== '') findings = [{ text: leaked, start: -1, end: -1 }];
        break;
      }
    }
    return { id, ran, fired: findings.length > 0, findings };
  }

  // numbers_not_in_evidence

  private normNumber(raw: string): string {
    let s = raw;
    for (const c of this.r.numberRemove) s = s.split(c).join('');
    this.r.numberSuffix.lastIndex = 0;
    return s.replace(this.r.numberSuffix, '');
  }

  private numbers(): DetectorFinding[] {
    const known = new Set<string>();
    for (const p of this.refParts) for (const m of findAll(this.r.number, p)) known.add(this.normNumber(m.text));
    const markers = findAll(this.r.listMarker, this.resp);
    const out: DetectorFinding[] = [];
    for (const m of findAll(this.r.number, this.resp)) {
      if (markers.some((s) => s.start <= m.start && m.start < s.end)) continue;
      const norm = this.normNumber(m.text);
      if (this.smallInteger(norm) && !this.unitAdjacent(m)) continue;
      if (!known.has(norm)) out.push({ text: m.text, start: m.start, end: m.end });
    }
    return out;
  }

  /** A normalized number without a fraction whose value is <= min_integer. */
  private smallInteger(norm: string): boolean {
    if (norm.includes('.') || !/^[0-9]+$/.test(norm)) return false;
    // strconv.ParseUint(norm, 10, 64) fails above 2^64-1, which is never small.
    const v = BigInt(norm);
    return v <= 0xffffffffffffffffn && this.minInteger >= 0 && v <= BigInt(Math.floor(this.minInteger));
  }

  /**
   * A number that must be checked although it is small: a percentage, a number right after a
   * currency sign, or one followed within window_tokens tokens by a unit.
   */
  private unitAdjacent(m: Match): boolean {
    if (m.text.endsWith('%')) return true;
    if (m.start > 0 && this.r.currency.includes(this.resp[m.start - 1])) return true;
    for (const tok of findAll(this.r.windowToken, this.resp.slice(m.end), this.r.windowTokens)) {
      if (matches(this.r.unit, tok.text)) return true;
    }
    return false;
  }

  // dates_not_in_evidence

  /**
   * Applies the date patterns in order; a match overlapping a date already found in the same text
   * is ignored, as is one with an impossible month or day.
   */
  private findDates(text: string): FoundDate[] {
    const found: FoundDate[] = [];
    for (const p of this.r.dates) {
      for (const m of findAll(p.re, text)) {
        if (found.some((f) => m.start < f.end && f.start < m.end)) continue;
        const norm = this.normDate(text, p, m);
        if (norm !== null) found.push({ start: m.start, end: m.end, norm });
      }
    }
    return found.sort((a, b) => a.start - b.start);
  }

  private normDate(text: string, p: DatePattern, m: Match): string | null {
    const year = atoi(group(m, text, p.year));
    if (year === null) return null;
    const month = p.monthIsName ? (own(this.r.months, goToLower(group(m, text, p.month))) ?? 0) : atoi(group(m, text, p.month));
    if (month === null || month < 1 || month > 12) return null;
    const ym = `${pad(year, 4)}-${pad(month, 2)}`;
    if (p.day === 0) return ym;
    const day = atoi(group(m, text, p.day));
    if (day === null || day < 1 || day > 31) return null;
    return `${ym}-${pad(day, 2)}`;
  }

  private dates(): DetectorFinding[] {
    const known: string[] = [];
    for (const p of this.refParts) for (const f of this.findDates(p)) known.push(f.norm);
    const out: DetectorFinding[] = [];
    for (const f of this.findDates(this.resp)) {
      // A YYYY-MM date is known by prefix; a full date needs the same full date.
      const ok = known.some((k) => k === f.norm || (f.norm.length === 7 && k.startsWith(`${f.norm}-`)));
      if (!ok) out.push({ text: this.resp.slice(f.start, f.end), start: f.start, end: f.end });
    }
    return out;
  }

  // urls_not_in_evidence

  private stripURL(u: string): string {
    return trimRightChars(u, this.r.urlStripTail);
  }

  private urls(): DetectorFinding[] {
    const known = new Set<string>();
    for (const p of this.refParts) for (const m of findAll(this.r.url, p)) known.add(normURL(this.stripURL(m.text)));
    const out: DetectorFinding[] = [];
    for (const m of findAll(this.r.url, this.resp)) {
      const u = this.stripURL(m.text);
      if (u === '' || known.has(normURL(u))) continue;
      out.push({ text: u, start: m.start, end: m.start + u.length });
    }
    return out;
  }

  // quotes_not_in_evidence

  /**
   * The rules' text_normalization: curly quotes to straight ones, ASCII A-Z lowercased, whitespace
   * runs collapsed to one space, trimmed.
   */
  private normalizeText(s: string): string {
    let t = s;
    for (const [from, to] of this.r.quoteMap) t = t.split(from).join(to);
    t = t.replace(/[A-Z]+/g, (x) => x.toLowerCase());
    this.r.whitespace.lastIndex = 0;
    return trimChars(t.replace(this.r.whitespace, ' '), ' ');
  }

  private quotes(): DetectorFinding[] {
    const reference = this.refParts.map((p) => this.normalizeText(p)).join('\n');
    const out: DetectorFinding[] = [];
    for (const re of this.r.quotes) {
      for (const m of findAll(re, this.resp)) {
        const span = m.groups[1];
        if (span === undefined) continue;
        const raw = this.resp.slice(span[0], span[1]);
        const q = trimChars(this.normalizeText(raw), this.r.quoteStripEdges);
        const words = q.split(' ').filter((w) => w !== '').length;
        if (words < this.r.quoteMinWords || reference.includes(q)) continue;
        out.push({ text: raw, start: span[0], end: span[1] });
      }
    }
    return out.sort((a, b) => a.start - b.start);
  }

  // citation_out_of_range

  private citations(): DetectorFinding[] {
    const limit = Math.max(this.maxMarker, this.preDedupItems);
    const out: DetectorFinding[] = [];
    for (const re of this.r.citations) {
      for (const m of findAll(re, this.resp)) {
        const n = atoi(group(m, this.resp, 1));
        if (n === null || (n !== 0 && n <= limit)) continue;
        out.push({ text: m.text, start: m.start, end: m.end });
      }
    }
    return out.sort((a, b) => a.start - b.start);
  }

  // secret_pattern

  private secrets(): DetectorFinding[] {
    const seen: [number, number][] = [];
    const out: DetectorFinding[] = [];
    for (const re of this.r.secrets) {
      for (const m of findAll(re, this.resp)) {
        if (seen.some(([s, e]) => m.start < e && s < m.end)) continue;
        seen.push([m.start, m.end]);
        if (!this.userRequest.includes(m.text)) out.push({ text: maskSecret(m.text), start: m.start, end: m.end });
      }
    }
    return out.sort((a, b) => a.start - b.start);
  }

  // system_prompt_leak

  /**
   * Whether the detector ran (normalized system instructions of at least leakMin code points) and,
   * when it fired, the first window of them found in the response. Windows start every stride code
   * points, plus the final window. The Go reference finds them with a rolling hash; a substring
   * search of the same windows gives the same answer.
   */
  private promptLeak(): [boolean, string] {
    const s = Array.from(this.normalizeText(this.systemInstructions));
    const w = this.r.leakWindow;
    if (s.length < this.r.leakMin || s.length < w) return [s.length >= this.r.leakMin, ''];
    const response = this.normalizeText(this.resp);
    if (Array.from(response).length < w) return [true, ''];
    const starts: number[] = [];
    for (let i = 0; i + w <= s.length; i += this.r.leakStride) starts.push(i);
    if (starts[starts.length - 1] !== s.length - w) starts.push(s.length - w);
    for (const i of starts) {
      const win = s.slice(i, i + w).join('');
      if (response.includes(win)) return [true, win];
    }
    return [true, ''];
  }
}

/** strconv.Atoi for the ASCII digit groups the patterns capture; null when not a number. */
function atoi(s: string): number | null {
  return /^[0-9]+$/.test(s) ? Number(s) : null;
}

const pad = (n: number, width: number) => String(n).padStart(width, '0');

/** Lowercases the scheme and the authority (the text between "://" and the first "/", "?" or "#"). */
function normURL(u: string): string {
  const i = u.indexOf('://');
  if (i < 0) return u;
  const rest = u.slice(i + 3);
  let end = rest.search(/[/?#]/);
  if (end < 0) end = rest.length;
  return goToLower(u.slice(0, i + 3 + end)) + rest.slice(end);
}

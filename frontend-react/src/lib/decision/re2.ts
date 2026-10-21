/**
 * RE2 patterns from contracts/rules (and route evidence.tag_patterns) as JavaScript RegExps that
 * match the same text as Go's regexp package.
 *
 * The rules files keep to the syntax both engines share (no lookaround, no backreferences, no inline
 * flags; validate.mjs checks), but a few constructs mean different things:
 *
 * - With the m flag, RE2's ^ and $ break lines at \n only; JavaScript also breaks at \r, U+2028
 *   and U+2029. ^ and $ become (?<![^\n]) and (?![^\n]) and the m flag is dropped.
 * - Without the s flag, RE2's . excludes only \n; JavaScript's also excludes \r, U+2028 and U+2029.
 *   . becomes [^\n].
 * - RE2's \s is [\t\n\f\r ]; JavaScript's includes \v and the Unicode spaces. \s and \S are spelled
 *   out (the rules files already write [\t\n\f\r ]).
 * - A ] right after [ or [^ is a literal in RE2 and closes an empty class in JavaScript.
 * - (?P<name>, \A and \z are RE2 spellings of (?<name>, input start and input end.
 *
 * Patterns are compiled without the u flag, so \w, \d and \b stay ASCII as in RE2. Case-insensitive
 * matching differs only for U+017F and U+212A, which RE2 folds to s and k.
 */

const SPACE = '\\t\\n\\f\\r ';

/** Translates an RE2 pattern to JavaScript syntax for the given RE2 flags (a subset of "ims"). */
export function translateRE2(pattern: string, flags: string): string {
  const multiline = flags.includes('m');
  const dotAll = flags.includes('s');
  let out = '';
  let inClass = false;
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern[i];
    if (c === '\\') {
      const next = pattern[i + 1] ?? '';
      i++;
      if (next === 's') out += inClass ? SPACE : `[${SPACE}]`;
      else if (next === 'S' && !inClass) out += `[^${SPACE}]`;
      else if (next === 'A' && !inClass) out += '(?<![\\s\\S])';
      else if (next === 'z' && !inClass) out += '(?![\\s\\S])';
      else out += `\\${next}`;
      continue;
    }
    if (inClass) {
      if (c === ']') inClass = false;
      out += c;
      continue;
    }
    switch (c) {
      case '[': {
        inClass = true;
        out += c;
        if (pattern[i + 1] === '^') {
          out += '^';
          i++;
        }
        if (pattern[i + 1] === ']') {
          out += '\\]';
          i++;
        }
        break;
      }
      case '(':
        if (pattern.startsWith('(?P<', i)) {
          out += '(?<';
          i += 3;
        } else {
          out += c;
        }
        break;
      case '^':
        out += multiline ? '(?<![^\\n])' : '^';
        break;
      case '$':
        out += multiline ? '(?![^\\n])' : '$';
        break;
      case '.':
        out += dotAll ? '.' : '[^\\n]';
        break;
      default:
        out += c;
    }
  }
  return out;
}

/**
 * Compiles an RE2 pattern with RE2 flags into a global RegExp with match indices (flags "gd", plus
 * i and s when given). Throws for flags outside "ims" and for a pattern JavaScript rejects.
 */
export function compileRE2(pattern: string, flags = ''): RegExp {
  if (!/^[ims]*$/.test(flags)) throw new SyntaxError(`regexp flags ${JSON.stringify(flags)} must be a subset of "ims"`);
  const js = [...new Set(flags.replace('m', ''))].join('');
  return new RegExp(translateRE2(pattern, flags), `${js}dg`);
}

/** A match with its UTF-16 span and the spans of its groups (undefined when a group did not take part). */
export interface Match {
  start: number;
  end: number;
  text: string;
  groups: (readonly [number, number] | undefined)[];
}

/** Every non-overlapping match of re (compiled by compileRE2) in text, like Go's FindAll...Index(-1). */
export function findAll(re: RegExp, text: string, limit = -1): Match[] {
  const out: Match[] = [];
  re.lastIndex = 0;
  for (const m of text.matchAll(re)) {
    if (limit >= 0 && out.length >= limit) break;
    const indices = m.indices ?? [];
    out.push({
      start: m.index,
      end: m.index + m[0].length,
      text: m[0],
      groups: indices.map((span) => (span === undefined ? undefined : ([span[0], span[1]] as const))),
    });
  }
  return out;
}

/** Whether re (compiled by compileRE2) matches anywhere in text. */
export function matches(re: RegExp, text: string): boolean {
  re.lastIndex = 0;
  const found = re.test(text);
  re.lastIndex = 0;
  return found;
}

/** The text of group g of m, or "" when it did not take part (Go's empty submatch). */
export function group(m: Match, text: string, g: number): string {
  const span = m.groups[g];
  return span === undefined ? '' : text.slice(span[0], span[1]);
}

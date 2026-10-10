// @vitest-environment node
/**
 * CO-6 (D86): the prefixed words modules take their words from contracts/ui/copy.v1.json. A string
 * literal of three or more words in src/domain/words/** or components/studio/studioWords.ts is a
 * sentence someone reads, so it belongs in the copy contract (reached through `copy`), not in code.
 * Template literals count by their fixed text; module specifiers, regular expressions and type
 * positions do not count.
 */
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';
import { describe, expect, it } from 'vitest';

const web = fileURLToPath(new URL('../../..', import.meta.url));
const wordsDir = join(web, 'src/domain/words');

const FILES = [
  ...readdirSync(wordsDir)
    .filter((f) => f.endsWith('.ts') && !f.endsWith('.test.ts'))
    .map((f) => join(wordsDir, f)),
  join(web, 'src/components/studio/studioWords.ts'),
];

/**
 * Files whose rewire is still open, with the task that closes it. WEB-STUDIO rewires studio.ts and
 * studioWords.ts in SS-6; the lead drops these entries when that lands (I-3).
 */
const PENDING: Readonly<Record<string, string>> = {
  'src/domain/words/studio.ts': 'SS-6',
  'src/components/studio/studioWords.ts': 'SS-6',
};

const WORD = /[A-Za-z][A-Za-z'’-]*/g;
/** Words outside `{placeholders}`, which fillTemplate fills and nobody reads as written. */
const wordCount = (text: string) => (text.replace(/\{[a-z0-9_:]+\}/g, ' ').match(WORD) ?? []).length;

/** The user-facing literals of a file: [line, text] for every string of three or more words. */
export function sentencesIn(fileName: string, source: string): [number, string][] {
  const sf = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const out: [number, string][] = [];
  const add = (node: ts.Node, text: string) => {
    // An identifier or a path (no space) is not a sentence.
    if (/\s/.test(text.trim()) && wordCount(text) >= 3) out.push([sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1, text]);
  };
  const visit = (node: ts.Node): void => {
    if (ts.isImportDeclaration(node) || ts.isExportDeclaration(node) || ts.isTypeNode(node)) return;
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) add(node, node.text);
    else if (ts.isTemplateExpression(node)) add(node, [node.head.text, ...node.templateSpans.map((s) => s.literal.text)].join(' '));
    ts.forEachChild(node, visit);
  };
  visit(sf);
  return out;
}

describe('words modules take their sentences from the copy contract (CO-6)', () => {
  it('finds a sentence, and ignores imports, types and short labels', () => {
    const src = [
      "import { copy } from '../contracts/with three words';",
      "type Kind = 'one two three' | 'x';",
      "export const a = 'Not valid JSON';",
      'export const b = `${1} of ${2} calls to OpenRouter`;',
      "export const c = 'pass sample';",
      'export const d = copy.studio.lint;',
    ].join('\n');
    expect(sentencesIn('x.ts', src)).toEqual([
      [3, 'Not valid JSON'],
      [4, '  of   calls to OpenRouter'],
    ]);
  });

  it.each(FILES.map((f) => [relative(web, f).split('\\').join('/'), f]))('%s', (rel, file) => {
    const found = sentencesIn(rel, readFileSync(file, 'utf8'));
    if (Object.hasOwn(PENDING, rel)) return;
    expect(found, 'move these words to contracts/ui/copy.v1.json and read them through copy').toEqual([]);
  });
});

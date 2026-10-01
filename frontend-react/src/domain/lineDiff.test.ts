import { describe, expect, it } from 'vitest';
import { diffTexts, foldDiff, parseUnifiedDiff, summarizeDiff } from './lineDiff';

describe('line diff', () => {
  it('numbers old and new lines', () => {
    expect(diffTexts('a\nb\nc\n', 'a\nB\nc\n')).toEqual([
      { kind: 'same', text: 'a', oldNo: 1, newNo: 1 },
      { kind: 'remove', text: 'b', oldNo: 2, newNo: null },
      { kind: 'add', text: 'B', oldNo: null, newNo: 2 },
      { kind: 'same', text: 'c', oldNo: 3, newNo: 3 },
    ]);
  });

  it('folds long unchanged runs, keeping context around changes', () => {
    const before = Array.from({ length: 20 }, (_, i) => `line ${i}`).join('\n');
    const after = before.replace('line 10', 'line ten');
    const rows = foldDiff(diffTexts(before, after), 2);
    expect(rows[0]).toMatchObject({ kind: 'fold', hidden: 8 });
    expect(rows.flatMap((r) => (r.kind === 'fold' ? [] : [r.text]))).toEqual([
      'line 8',
      'line 9',
      'line 10',
      'line ten',
      'line 11',
      'line 12',
    ]);
    expect(rows[rows.length - 1]).toMatchObject({ kind: 'fold', hidden: 7 });
    expect(summarizeDiff(rows)).toEqual({ added: 1, removed: 1 });
  });

  it('does not fold a single line', () => {
    const rows = foldDiff(diffTexts('a\nb\nc\nd\n', 'a\nb\nc\nD\n'), 2);
    expect(rows.every((r) => r.kind !== 'fold')).toBe(true);
  });

  it('reads a unified diff, with folds for the lines between hunks', () => {
    const rows = parseUnifiedDiff(
      ['--- current', '+++ proposed', '@@ -3,2 +3,2 @@', ' routes:', '-  mode: flag', '+  mode: block', '@@ -20,1 +20,2 @@', ' end', '+tail', '\\ No newline at end of file', ''].join('\n'),
    );
    expect(rows[0]).toEqual({ kind: 'fold', lines: [], hidden: 2 });
    expect(rows[1]).toEqual({ kind: 'same', text: 'routes:', oldNo: 3, newNo: 3 });
    expect(rows[2]).toEqual({ kind: 'remove', text: '  mode: flag', oldNo: 4, newNo: null });
    expect(rows[3]).toEqual({ kind: 'add', text: '  mode: block', oldNo: null, newNo: 4 });
    expect(rows[4]).toEqual({ kind: 'fold', lines: [], hidden: 15 });
    expect(rows.slice(5)).toEqual([
      { kind: 'same', text: 'end', oldNo: 20, newNo: 20 },
      { kind: 'add', text: 'tail', oldNo: null, newNo: 21 },
    ]);
  });

  it('reads an empty diff as no rows', () => {
    expect(parseUnifiedDiff('')).toEqual([]);
  });
});

/**
 * Line diffs for reviewing a change before it is saved (a policy, an evaluator version): which
 * lines were added, removed or kept, with long unchanged runs folded to a few lines of context.
 * Works from two texts (the `diff` package) or from a unified diff the server sent
 * (`ValidateResult.diff`, SPEC §2.4 #22).
 */
import { diffLines } from 'diff';

export type DiffLineKind = 'add' | 'remove' | 'same';

export interface DiffLine {
  kind: DiffLineKind;
  text: string;
  /** 1-based line number in the old text (remove/same). */
  oldNo: number | null;
  /** 1-based line number in the new text (add/same). */
  newNo: number | null;
}

export interface DiffFold {
  kind: 'fold';
  /** The unchanged lines folded away (empty when a unified diff left them out). */
  lines: DiffLine[];
  /** How many unchanged lines the fold stands for. */
  hidden: number;
}

export type DiffRow = DiffLine | DiffFold;

export interface DiffSummary {
  added: number;
  removed: number;
}

function splitLines(value: string): string[] {
  const lines = value.split('\n');
  if (lines.length > 0 && lines[lines.length - 1] === '') lines.pop();
  return lines;
}

/** Every line of `before` → `after`, in order. */
export function diffTexts(before: string, after: string): DiffLine[] {
  const out: DiffLine[] = [];
  let oldNo = 1;
  let newNo = 1;
  for (const part of diffLines(before, after)) {
    for (const text of splitLines(part.value)) {
      if (part.added) out.push({ kind: 'add', text, oldNo: null, newNo: newNo++ });
      else if (part.removed) out.push({ kind: 'remove', text, oldNo: oldNo++, newNo: null });
      else out.push({ kind: 'same', text, oldNo: oldNo++, newNo: newNo++ });
    }
  }
  return out;
}

/**
 * Reads a unified diff (`--- a`, `+++ b`, `@@ -l,s +l,s @@` hunks) into rows. The unchanged lines
 * a unified diff leaves out between hunks become folds that can say how many there are but not
 * show them. File headers and "\ No newline at end of file" markers are skipped.
 */
export function parseUnifiedDiff(diff: string): DiffRow[] {
  const rows: DiffRow[] = [];
  let oldNo = 0;
  let newNo = 0;
  let inHunk = false;
  for (const raw of splitLines(diff)) {
    const hunk = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(raw);
    if (hunk) {
      const nextOld = Number(hunk[1]);
      const gap = inHunk ? nextOld - oldNo : nextOld - 1;
      if (gap > 0) rows.push({ kind: 'fold', lines: [], hidden: gap });
      oldNo = nextOld;
      newNo = Number(hunk[2]);
      inHunk = true;
      continue;
    }
    if (!inHunk || raw.startsWith('\\')) continue;
    const sign = raw.charAt(0);
    const text = raw.slice(1);
    if (sign === '+') rows.push({ kind: 'add', text, oldNo: null, newNo: newNo++ });
    else if (sign === '-') rows.push({ kind: 'remove', text, oldNo: oldNo++, newNo: null });
    else rows.push({ kind: 'same', text, oldNo: oldNo++, newNo: newNo++ });
  }
  return rows;
}

export function summarizeDiff(rows: readonly DiffRow[]): DiffSummary {
  let added = 0;
  let removed = 0;
  for (const r of rows) {
    if (r.kind === 'add') added += 1;
    else if (r.kind === 'remove') removed += 1;
  }
  return { added, removed };
}

/**
 * Keeps `context` unchanged lines around each change and folds longer unchanged runs into one
 * row. A diff without changes folds entirely.
 */
export function foldDiff(lines: readonly DiffLine[], context = 3): DiffRow[] {
  const changed = lines.map((l) => l.kind !== 'same');
  const keep = lines.map((_, i) => {
    for (let d = -context; d <= context; d += 1) {
      if (changed[i + d]) return true;
    }
    return false;
  });
  const rows: DiffRow[] = [];
  let fold: DiffLine[] = [];
  const flush = () => {
    if (fold.length === 0) return;
    // Folding a single line saves nothing: show it.
    if (fold.length === 1) rows.push(fold[0]);
    else rows.push({ kind: 'fold', lines: fold, hidden: fold.length });
    fold = [];
  };
  lines.forEach((line, i) => {
    if (keep[i]) {
      flush();
      rows.push(line);
    } else {
      fold.push(line);
    }
  });
  flush();
  return rows;
}

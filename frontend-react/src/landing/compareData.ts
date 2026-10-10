/**
 * contracts/claims/compare.v1.json (D113), typed: the comparison's columns and the other kinds of
 * tools, each cell what that vendor's public documentation says, with its link and date.
 */
import compareDoc from '@contracts/claims/compare.v1.json';

export interface CompareCell {
  value: 'yes' | 'no' | 'partial' | 'varies' | 'unverified';
  text: string;
  source_url: string | null;
  as_of: string | null;
  reviewed: boolean;
}

export interface CompareRow {
  id: string;
  name: string;
  examples: string;
  cells: Record<string, CompareCell>;
}

export const compareColumns: readonly { id: string; label: string }[] = compareDoc.columns;
export const compareRows: readonly CompareRow[] = compareDoc.rows as CompareRow[];

/** "docs.aws.amazon.com" for a source link's text. */
export function sourceHost(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, '');
  } catch {
    return url;
  }
}

/** Every source the comparison cites, once, with the cells it supports (the footer's group). */
export function compareSources(): { url: string; asOf: string; cells: string[] }[] {
  const byUrl = new Map<string, { url: string; asOf: string; cells: string[] }>();
  const label = new Map(compareColumns.map((c) => [c.id, c.label]));
  for (const r of compareRows) {
    for (const [col, cell] of Object.entries(r.cells)) {
      if (cell.value === 'unverified' || !cell.source_url || !cell.as_of) continue;
      const entry = byUrl.get(cell.source_url) ?? { url: cell.source_url, asOf: cell.as_of, cells: [] };
      if (cell.as_of > entry.asOf) entry.asOf = cell.as_of;
      entry.cells.push(`${r.name}: ${label.get(col) ?? col}`);
      byUrl.set(cell.source_url, entry);
    }
  }
  return [...byUrl.values()];
}

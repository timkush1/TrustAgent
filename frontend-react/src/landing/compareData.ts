/**
 * contracts/claims/compare.v1.json (D113), typed: the comparison's columns and the other kinds of
 * tools, each cell what that vendor's public documentation says, with its link and date.
 */
import compareDoc from '@contracts/claims/compare.v1.json';

export interface CompareCell {
  value: 'yes' | 'no' | 'partial' | 'varies' | 'not_documented' | 'unverified';
  /** The product whose documentation the cell cites (null only while unverified). */
  vendor: string | null;
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

/** The one date every verified cell was read on, or null when they differ (then each cell shows its own). */
export function commonReadDate(rows: readonly CompareRow[] = compareRows): string | null {
  const dates = new Set(rows.flatMap((r) => Object.values(r.cells)).flatMap((c) => (c.value !== 'unverified' && c.source_url && c.as_of ? [c.as_of] : [])));
  return dates.size === 1 ? [...dates][0] : null;
}

export const COMMON_READ_DATE = commonReadDate();

/** "docs.aws.amazon.com" for a source link's text. */
export function sourceHost(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, '');
  } catch {
    return url;
  }
}

/** Every source the comparison cites, once, with the product whose docs it is and the cells it supports (the footer's group). */
export function compareSources(): { url: string; vendor: string; asOf: string; cells: string[] }[] {
  const byUrl = new Map<string, { url: string; vendor: string; asOf: string; cells: string[] }>();
  const label = new Map(compareColumns.map((c) => [c.id, c.label]));
  for (const r of compareRows) {
    for (const [col, cell] of Object.entries(r.cells)) {
      if (cell.value === 'unverified' || !cell.source_url || !cell.as_of || !cell.vendor) continue;
      const entry = byUrl.get(cell.source_url) ?? { url: cell.source_url, vendor: cell.vendor, asOf: cell.as_of, cells: [] };
      if (cell.as_of > entry.asOf) entry.asOf = cell.as_of;
      entry.cells.push(`${r.name}: ${label.get(col) ?? col}`);
      byUrl.set(cell.source_url, entry);
    }
  }
  return [...byUrl.values()];
}

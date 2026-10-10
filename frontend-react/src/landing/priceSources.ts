/**
 * Where a price the calculator shows comes from: its entry in the footer's source list. Kept apart
 * from the price data so a chip costs no pricing bytes.
 */
import type { PriceEntry } from '@/types/api';

/** The source-list id of a price table entry (LabelChip links to `#source-<id>`). */
export function priceAnchorId(e: Pick<PriceEntry, 'provider' | 'match'>): string {
  const slug = `${e.provider}-${e.match}`
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return `price-${slug}`;
}

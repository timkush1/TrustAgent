/**
 * Words for the Studio server values that other pages may show: the lint and hard-rule codes
 * (contracts/ui copy.studio.lint). Owned by WEB-ST. presentation.ts re-exports this module with
 * `export *`, so every export here starts with `studio` (D86) and can never collide with
 * another plane's words.
 *
 * presentation.ts sits in a chunk the landing page loads too (its 110 kB budget), so the words only
 * Studio shows (test-bench rows, sample filters, dry-run texts, cost labels) live with Studio in
 * components/studio/studioWords.ts and load with its route.
 */
import { copy } from '../contracts';
import { humanizeId } from '../presentation';

/** The short title of a lint or hard-rule code (copy.studio.lint), or the code in words. */
export function studioLintTitle(code: string): string {
  const titles: Record<string, string> = copy.studio.lint;
  if (code === 'schema') return "Doesn't match the evaluator format";
  if (code === 'syntax') return 'Not valid JSON';
  return Object.hasOwn(titles, code) ? titles[code] : humanizeId(code);
}

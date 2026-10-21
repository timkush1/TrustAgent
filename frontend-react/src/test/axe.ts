/**
 * axe-core in jsdom, for component-level accessibility checks. It runs the same WCAG tags as the
 * Playwright shell spec (wcag2a/aa, 21aa, 22aa) plus best practices, minus the checks jsdom cannot
 * do or that only make sense for a whole page:
 *
 * - color-contrast: jsdom does not compute colors; charts/tokenContrast.test.ts checks the tokens.
 * - region, landmark-one-main, page-has-heading-one: a component is not a page.
 */
import axe, { type Result } from 'axe-core';

export async function axeViolations(root: Element = document.body): Promise<Result[]> {
  const results = await axe.run(root, {
    runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa', 'best-practice'] },
    rules: {
      'color-contrast': { enabled: false },
      region: { enabled: false },
      'landmark-one-main': { enabled: false },
      'page-has-heading-one': { enabled: false },
    },
  });
  return results.violations;
}

/** "rule-id: help (selector, …)" per violation, for readable assertion failures. */
export function formatViolations(violations: readonly Result[]): string[] {
  return violations.map((v) => `${v.id}: ${v.help} (${v.nodes.map((n) => n.target.join(' ')).join(', ')})`);
}

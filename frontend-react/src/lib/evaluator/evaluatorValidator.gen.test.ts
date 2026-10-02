// @vitest-environment node
/**
 * Drift check for evaluatorValidator.gen.ts: compiling contracts/schema/evaluator.v1 and
 * evaluator-draft.v1 again must reproduce the committed file byte for byte. On failure, run
 * `node scripts/gen-evaluator-validator.ts` in frontend-react and commit the result.
 */
import { describe, expect, it } from 'vitest';
import committed from './evaluatorValidator.gen.ts?raw';
import { DRAFT_SCHEMA, EVALUATOR_SCHEMA, generateEvaluatorValidator, VALIDATOR_EXPORTS } from './generateValidator';

const schemaFiles = import.meta.glob<unknown>('@contracts/schema/*.schema.json', { eager: true, import: 'default' });
const byName = new Map(Object.entries(schemaFiles).map(([path, doc]) => [path.slice(path.lastIndexOf('/') + 1), doc]));

describe('evaluatorValidator.gen.ts', () => {
  it('reads schema files that exist', () => {
    expect(byName.has(EVALUATOR_SCHEMA)).toBe(true);
    expect(byName.has(DRAFT_SCHEMA)).toBe(true);
  });

  it('matches a fresh compilation of evaluator.v1 and evaluator-draft.v1', () => {
    const fresh = generateEvaluatorValidator((file) => {
      if (!byName.has(file)) throw new Error(`unknown schema file ${file}`);
      return byName.get(file);
    });
    expect(fresh === committed, 'src/lib/evaluator/evaluatorValidator.gen.ts is stale: run `node scripts/gen-evaluator-validator.ts`').toBe(true);
  }, 30_000);

  it('exports one validator per schema and needs no eval and no ajv at runtime', () => {
    for (const name of Object.keys(VALIDATOR_EXPORTS)) expect(committed).toMatch(new RegExp(`export const ${name} = `));
    expect(committed).not.toMatch(/\brequire\(|new Function|\beval\(/);
    expect(committed).not.toMatch(/from ['"]ajv/);
  });
});

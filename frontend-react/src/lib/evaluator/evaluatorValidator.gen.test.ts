// @vitest-environment node
/**
 * Drift check for evaluatorValidator.gen.ts: compiling contracts/schema/evaluator.v1 and
 * evaluator-draft.v1 again must reproduce the committed file byte for byte. On failure, run
 * `node scripts/gen-evaluator-validator.ts` in frontend-react and commit the result.
 */
import { describe, expect, it } from 'vitest';
import { blankSpec } from '@/lib/studio/templates';
import committed from './evaluatorValidator.gen.ts?raw';
import drafterPrompt from '@contracts/prompts/evaluator-drafter.v1.json';
import { validateEvaluatorDraft as generatedDraft, validateEvaluatorSpec as generated } from './evaluatorValidator.gen';
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

  it('is one module for both validators: each ajv helper and each schema appears once (D108)', () => {
    const count = (re: RegExp) => committed.match(re)?.length ?? 0;
    expect(count(/^function ucs2length\(/gm)).toBe(1);
    expect(count(/^function equal\(/gm)).toBe(1);
    expect(count(/"\$id":"[^"]*\/evaluator\.v1\.schema\.json"/g)).toBe(1);
    expect(count(/"\$id":"[^"]*\/evaluator-draft\.v1\.schema\.json"/g)).toBe(1);
  });

  it('validates an AI draft with the draft schema: the drafter prompt example passes, an id does not', () => {
    const validateEvaluatorDraft = generatedDraft as unknown as ((data: unknown) => boolean) & { errors?: unknown[] | null };
    const example = drafterPrompt.examples[0].output;
    expect(validateEvaluatorDraft(example)).toBe(true);
    expect(validateEvaluatorDraft({ ...example, id: 'competitor_mention' })).toBe(false);
    expect(validateEvaluatorDraft.errors).toContainEqual(expect.objectContaining({ instancePath: '', keyword: 'additionalProperties' }));
  });

  it('marks for the forbidden-identifier scan exactly the lines that test the spec field escalation.numeric_or_date', () => {
    const asValue = /(?<![A-Za-z]:)["']numeric_or_date["'](?!\s*:)/;
    const lines = committed.split('\n');
    const marked = lines.filter((l) => l.includes('check-forbidden: ignore'));
    expect(marked.length).toBeGreaterThan(0);
    for (const line of marked) expect(line).toMatch(asValue);
    for (const line of lines) if (asValue.test(line)) expect(line).toContain('check-forbidden: ignore (the spec field escalation.numeric_or_date)');
  });

  it('still checks the marked field: a boolean, next to the other escalation keys only', () => {
    // The generated file is @ts-nocheck: ajv sets `errors` on the function after each call.
    const validateEvaluatorSpec = generated as unknown as ((data: unknown) => boolean) & { errors?: unknown[] | null };
    const spec = blankSpec([]);
    expect(validateEvaluatorSpec({ ...spec, escalation: { numeric_or_date: true } })).toBe(true);
    expect(validateEvaluatorSpec({ ...spec, escalation: { numeric_or_date: 'yes' } })).toBe(false);
    expect(validateEvaluatorSpec.errors).toContainEqual(expect.objectContaining({ instancePath: '/escalation/numeric_or_date', keyword: 'type' }));
    expect(validateEvaluatorSpec({ ...spec, escalation: { numeric_or_dates: true } })).toBe(false);
    expect(validateEvaluatorSpec.errors).toContainEqual(expect.objectContaining({ instancePath: '/escalation', keyword: 'additionalProperties' }));
  });
});

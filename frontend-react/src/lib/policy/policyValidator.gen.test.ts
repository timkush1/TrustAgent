// @vitest-environment node
/**
 * Drift check for policyValidator.gen.ts: compiling contracts/schema/policy.v1.schema.json again
 * must reproduce the committed file byte for byte. On failure, run `make gen-policy-validator`
 * and commit the result.
 */
import { describe, expect, it } from 'vitest';
import { COMMON_SCHEMA, generatePolicyValidator, POLICY_SCHEMA } from './generateValidator';
import committed from './policyValidator.gen.ts?raw';

const schemaFiles = import.meta.glob<unknown>('@contracts/schema/*.schema.json', { eager: true, import: 'default' });
const byName = new Map(Object.entries(schemaFiles).map(([path, doc]) => [path.slice(path.lastIndexOf('/') + 1), doc]));

describe('policyValidator.gen.ts', () => {
  it('reads schema files that exist', () => {
    expect(byName.has(POLICY_SCHEMA)).toBe(true);
    expect(byName.has(COMMON_SCHEMA)).toBe(true);
  });

  it('matches a fresh compilation of policy.v1', () => {
    const fresh = generatePolicyValidator((file) => {
      if (!byName.has(file)) throw new Error(`unknown schema file ${file}`);
      return byName.get(file);
    });
    expect(fresh === committed, 'src/lib/policy/policyValidator.gen.ts is stale: run `make gen-policy-validator`').toBe(true);
  }, 30_000);

  it('needs no eval and no ajv at runtime', () => {
    expect(committed).not.toMatch(/\brequire\(|new Function|\beval\(/);
    expect(committed).not.toMatch(/from ['"]ajv/);
  });
});

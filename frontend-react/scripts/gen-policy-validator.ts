/**
 * Writes src/lib/policy/policyValidator.gen.ts from contracts/schema (`make gen-policy-validator`).
 *
 *   node scripts/gen-policy-validator.ts          regenerate the file
 *   node scripts/gen-policy-validator.ts --check  exit 1 when the committed file is stale
 *
 * Runs on Node >= 22.18 (built-in TypeScript type stripping). The drift test
 * (src/lib/policy/policyValidator.gen.test.ts) runs the same generator under vitest.
 */
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { generatePolicyValidator } from '../src/lib/policy/generateValidator.ts';

const schemaDir = fileURLToPath(new URL('../../contracts/schema/', import.meta.url));
const target = fileURLToPath(new URL('../src/lib/policy/policyValidator.gen.ts', import.meta.url));

const text = generatePolicyValidator((file) => JSON.parse(readFileSync(schemaDir + file, 'utf8')));

if (process.argv.includes('--check')) {
  let current = '';
  try {
    current = readFileSync(target, 'utf8');
  } catch {
    // A missing file is stale.
  }
  if (current !== text) {
    console.error('src/lib/policy/policyValidator.gen.ts is stale: run `make gen-policy-validator` and commit the result.');
    process.exit(1);
  }
  console.log('src/lib/policy/policyValidator.gen.ts is up to date.');
} else {
  writeFileSync(target, text);
  console.log(`wrote src/lib/policy/policyValidator.gen.ts (${text.split('\n').length} lines)`);
}

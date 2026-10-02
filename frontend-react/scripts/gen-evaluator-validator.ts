/**
 * Writes src/lib/evaluator/evaluatorValidator.gen.ts from contracts/schema.
 *
 *   node scripts/gen-evaluator-validator.ts          regenerate the file
 *   node scripts/gen-evaluator-validator.ts --check  exit 1 when the committed file is stale
 *
 * Runs on Node >= 22.18 (built-in TypeScript type stripping). The drift test
 * (src/lib/evaluator/evaluatorValidator.gen.test.ts) runs the same generator under vitest.
 */
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { generateEvaluatorValidator } from '../src/lib/evaluator/generateValidator.ts';

const schemaDir = fileURLToPath(new URL('../../contracts/schema/', import.meta.url));
const target = fileURLToPath(new URL('../src/lib/evaluator/evaluatorValidator.gen.ts', import.meta.url));
const name = 'src/lib/evaluator/evaluatorValidator.gen.ts';

const text = generateEvaluatorValidator((file) => JSON.parse(readFileSync(schemaDir + file, 'utf8')));

if (process.argv.includes('--check')) {
  let current = '';
  try {
    current = readFileSync(target, 'utf8');
  } catch {
    // A missing file is stale.
  }
  if (current !== text) {
    console.error(`${name} is stale: run \`node scripts/gen-evaluator-validator.ts\` in frontend-react and commit the result.`);
    process.exit(1);
  }
  console.log(`${name} is up to date.`);
} else {
  writeFileSync(target, text);
  console.log(`wrote ${name} (${text.split('\n').length} lines)`);
}

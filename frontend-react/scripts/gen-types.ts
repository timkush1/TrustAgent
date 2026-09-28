/**
 * Writes src/lib/contracts/types.gen.ts from contracts/schema (`make gen-types`).
 *
 *   node scripts/gen-types.ts          regenerate the file
 *   node scripts/gen-types.ts --check  exit 1 when the committed file is stale
 *
 * Runs on Node >= 22.18 (built-in TypeScript type stripping). The drift test
 * (src/lib/contracts/types.gen.test.ts) runs the same generator under vitest.
 */
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { generateTypesGen } from '../src/lib/contracts/generate.ts';

const schemaDir = fileURLToPath(new URL('../../contracts/schema/', import.meta.url));
const target = fileURLToPath(new URL('../src/lib/contracts/types.gen.ts', import.meta.url));

const text = await generateTypesGen((file) => JSON.parse(readFileSync(schemaDir + file, 'utf8')));

if (process.argv.includes('--check')) {
  let current = '';
  try {
    current = readFileSync(target, 'utf8');
  } catch {
    // A missing file is stale.
  }
  if (current !== text) {
    console.error('src/lib/contracts/types.gen.ts is stale: run `make gen-types` and commit the result.');
    process.exit(1);
  }
  console.log('src/lib/contracts/types.gen.ts is up to date.');
} else {
  writeFileSync(target, text);
  console.log(`wrote src/lib/contracts/types.gen.ts (${text.split('\n').length} lines)`);
}

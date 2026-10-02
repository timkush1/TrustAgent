/**
 * Generates src/lib/evaluator/evaluatorValidator.gen.ts: the evaluator.v1 and evaluator-draft.v1
 * JSON Schemas (SPEC §2.8) compiled ahead of time by ajv into plain validation code (ajv
 * "standalone" mode), with one export per schema.
 *
 * Why generated: ajv compiles schemas with `new Function`, which the dashboard's Content Security
 * Policy (`script-src 'self'`, no 'unsafe-eval') forbids. Compiled at build time, Studio's JSON tab
 * checks a spec as the gateway does (Go `decision.ValidateSpec` runs the same schemas, D54) without
 * eval, and ajv stays out of the bundle (its two runtime helpers are inlined below).
 *
 * The same approach as src/lib/policy/generateValidator.ts. Pure: it takes a schema reader and
 * returns the file text, so the command line (scripts/gen-evaluator-validator.ts) and the drift test
 * (evaluatorValidator.gen.test.ts) share it. Build tooling only: app code imports the generated file.
 */
import { Ajv2020 } from 'ajv/dist/2020.js';
import standaloneCode from 'ajv/dist/standalone/index.js';

export const EVALUATOR_SCHEMA = 'evaluator.v1.schema.json';
export const DRAFT_SCHEMA = 'evaluator-draft.v1.schema.json';

/** Reads one parsed schema file by name (for example "evaluator.v1.schema.json"). */
export type SchemaReader = (fileName: string) => unknown;

/** The generated file's exports, by the schema each one validates. */
export const VALIDATOR_EXPORTS = {
  validateEvaluatorSpec: EVALUATOR_SCHEMA,
  validateEvaluatorDraft: DRAFT_SCHEMA,
} as const;

const BANNER = `/* eslint-disable */
// @ts-nocheck
/**
 * GENERATED FILE: do not edit. The evaluator.v1 and evaluator-draft.v1 schemas
 * (contracts/schema/evaluator*.v1.schema.json) compiled by ajv standalone
 * (src/lib/evaluator/generateValidator.ts). Regenerate with
 * \`node scripts/gen-evaluator-validator.ts\`; evaluatorValidator.gen.test.ts fails when this file is stale.
 */
`;

// ajv's runtime helpers (ajv/dist/runtime/ucs2length and equal), inlined so the bundle needs no ajv.
const HELPERS = `
function ucs2length(str) {
  const len = str.length;
  let length = 0;
  let pos = 0;
  let value;
  while (pos < len) {
    length++;
    value = str.charCodeAt(pos++);
    if (value >= 0xd800 && value <= 0xdbff && pos < len) {
      value = str.charCodeAt(pos);
      if ((value & 0xfc00) === 0xdc00) pos++;
    }
  }
  return length;
}

function equal(a, b) {
  if (a === b) return true;
  if (a && b && typeof a === 'object' && typeof b === 'object') {
    if (Array.isArray(a) !== Array.isArray(b)) return false;
    if (Array.isArray(a)) return a.length === b.length && a.every((v, i) => equal(v, b[i]));
    const keys = Object.keys(a);
    if (keys.length !== Object.keys(b).length) return false;
    return keys.every((k) => Object.prototype.hasOwnProperty.call(b, k) && equal(a[k], b[k]));
  }
  return a !== a && b !== b;
}
`;

function schemaId(schema: unknown, file: string): string {
  const id = (schema as { $id?: unknown } | null)?.$id;
  if (typeof id !== 'string' || id === '') throw new Error(`${file} has no $id`);
  return id;
}

export function generateEvaluatorValidator(read: SchemaReader): string {
  const ajv = new Ajv2020({
    strict: true,
    strictTypes: false,
    strictTuples: false,
    strictRequired: false,
    allowUnionTypes: true,
    allErrors: true,
    validateFormats: false,
    code: { source: true, esm: true, lines: true },
  });
  const refs: Record<string, string> = {};
  for (const [name, file] of Object.entries(VALIDATOR_EXPORTS)) {
    const schema = read(file) as object;
    ajv.addSchema(schema);
    refs[name] = schemaId(schema, file);
  }
  const code = standaloneCode(ajv, refs)
    .replace(/const (\w+) = require\("ajv\/dist\/runtime\/ucs2length"\)\.default;?/g, 'const $1 = ucs2length;')
    .replace(/const (\w+) = require\("ajv\/dist\/runtime\/equal"\)\.default;?/g, 'const $1 = equal;');
  if (/\brequire\(/.test(code)) throw new Error('The standalone evaluator validator needs an ajv runtime helper that is not inlined.');
  return `${BANNER}${HELPERS}\n${code.trim()}\n`;
}

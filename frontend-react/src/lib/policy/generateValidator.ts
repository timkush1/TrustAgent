/**
 * Generates src/lib/policy/policyValidator.gen.ts: the policy.v1 JSON Schema (SPEC §2.7) compiled
 * ahead of time by ajv into plain validation code (ajv "standalone" mode).
 *
 * Why generated: ajv compiles schemas with `new Function`, which the dashboard's Content Security
 * Policy (`script-src 'self'`, no 'unsafe-eval') forbids. Compiled at build time, the advanced
 * policy editor validates as the gateway does without eval, and ajv itself stays out of the bundle
 * (its two runtime helpers are inlined below).
 *
 * Pure like src/lib/contracts/generate.ts: it takes a schema reader and returns the file text, so
 * the command line (scripts/gen-policy-validator.ts, `make gen-policy-validator`) and the drift test
 * (policyValidator.gen.test.ts) share it. Build tooling only: app code imports the generated file.
 */
import { Ajv2020 } from 'ajv/dist/2020.js';
import standaloneCode from 'ajv/dist/standalone/index.js';

export const POLICY_SCHEMA = 'policy.v1.schema.json';
export const COMMON_SCHEMA = 'common.v1.schema.json';

/** Reads one parsed schema file by name (for example "policy.v1.schema.json"). */
export type SchemaReader = (fileName: string) => unknown;

const BANNER = `/* eslint-disable */
// @ts-nocheck
/**
 * GENERATED FILE: do not edit. The policy.v1 schema (contracts/schema/policy.v1.schema.json)
 * compiled by ajv standalone (src/lib/policy/generateValidator.ts). Regenerate with
 * \`make gen-policy-validator\`; policyValidator.gen.test.ts fails when this file is stale.
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

export function generatePolicyValidator(read: SchemaReader): string {
  const ajv = new Ajv2020({
    strict: true,
    strictTypes: false,
    strictTuples: false,
    strictRequired: false,
    allowUnionTypes: true,
    allErrors: true,
    // Formats are annotations here: the gateway does not reject a policy for them either.
    validateFormats: false,
    code: { source: true, esm: true, lines: true },
  });
  ajv.addSchema(read(COMMON_SCHEMA) as object);
  const validate = ajv.compile(read(POLICY_SCHEMA) as object);
  const code = standaloneCode(ajv, validate)
    .replace(/const (\w+) = require\("ajv\/dist\/runtime\/ucs2length"\)\.default;?/g, 'const $1 = ucs2length;')
    .replace(/const (\w+) = require\("ajv\/dist\/runtime\/equal"\)\.default;?/g, 'const $1 = equal;');
  if (/\brequire\(/.test(code)) throw new Error('The standalone policy validator needs an ajv runtime helper that is not inlined.');
  return `${BANNER}${HELPERS}\n${code.trim()}\n`;
}

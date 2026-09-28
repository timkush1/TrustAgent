#!/usr/bin/env node
/**
 * Generates contracts/schema/api.v1.schema.json from frontend-react/src/types/api.ts (SPEC §2.4,
 * §2.6): api.ts is the source, the schema is derived, and the two must never drift
 * (frontend-react/src/types/api.test.ts checks them against each other).
 *
 *   npm ci && node gen-api-schema.mjs           rewrite the schema
 *   node gen-api-schema.mjs --check             exit 1 when the committed schema is stale
 *
 * Steps (the pipeline the P1 types stream ran by hand, plus the contract types):
 *   1. Copy api.ts into a scratch directory and append a scratch type that instantiates the generic
 *      pages the API serves (Page<TraceSummary>, Page<GateDecisionRow>, Page<AuditSummary>).
 *   2. The types api.ts takes from src/lib/contracts/types.gen.ts (SPEC §2.6) are schemas of their
 *      own in contracts/schema, and their enums share names with api.ts (PolicyMode, ...), which
 *      the generator rejects as duplicate definitions. The copy therefore imports them from a stub
 *      module, and each stub definition becomes a $ref to its contract schema (CONTRACT_REFS). The
 *      generator has no parser for inline import types either, so every
 *      import('@/lib/contracts/types.gen').X becomes X with a matching `import type`.
 *   3. Run ts-json-schema-generator over every exported type (--type '*' --expose export
 *      --jsDoc extended).
 *   4. Sort keys as the generator's CLI prints them, prune to ROOTS plus everything they
 *      reference, and convert draft-07 to 2020-12 (definitions -> $defs; array-form items ->
 *      prefixItems with items: false).
 */
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const { createGenerator } = require('ts-json-schema-generator');

const repo = fileURLToPath(new URL('../../../', import.meta.url));
const API_TS = join(repo, 'frontend-react/src/types/api.ts');
const OUT = join(repo, 'contracts/schema/api.v1.schema.json');

/** The payload types the schema describes; everything they reference comes along. */
export const ROOTS = [
  // errors
  'ApiErrorBody',
  // health / status
  'HealthStatus',
  'ReadyStatus',
  'SystemStatus',
  'SystemStatusLite',
  // traces
  'TraceSummary',
  'TraceDetail',
  'Page<TraceSummary>',
  // gate decisions
  'GateDecisionView',
  'GateDecisionRow',
  'Page<GateDecisionRow>',
  // audits (claims with UTF-16 spans)
  'AuditSummary',
  'AuditDetail',
  'Page<AuditSummary>',
  'ClaimView',
  'DirectAuditRequest',
  // WebSocket v2
  'WsEvent',
  'WsHello',
];

const SCRATCH_PAGES = `
// scratch-only: instantiate the generic pages the core subset needs
export interface __CorePages {
  traces: Page<TraceSummary>;
  decisions: Page<GateDecisionRow>;
  audits: Page<AuditSummary>;
}
`;

const DESCRIPTION =
  'Contract-owned JSON Schema for the TrustAgent REST and WebSocket v2 payloads (docs/v2/SPEC.md sections 2.1, 2.4, ' +
  '2.5 and 2.6). Every $defs entry has the name, fields and enum values of the same type in ' +
  "frontend-react/src/types/api.ts; Go handler tests and the golden fixtures in contracts/fixtures/api/ validate against " +
  "'api.v1.schema.json#/$defs/<TypeName>'. This is the S0 core subset: health and status, traces (list and detail), " +
  'gate decisions, audits with claims and UTF-16 spans, the WebSocket v2 envelope and events, and the error body.';

const COMMENT =
  "Generated from api.ts with ts-json-schema-generator 2.9.0 (--type '*' --expose export --jsDoc extended, plus a " +
  'scratch type instantiating Page<TraceSummary>, Page<GateDecisionRow> and Page<AuditSummary>), converted from ' +
  'draft-07 to 2020-12 (definitions -> $defs; array-form items -> prefixItems) and pruned to the roots named in the ' +
  "description plus everything they reference. Page<T> keeps the generator's name, percent-encoded in $ref. " +
  'Regenerate instead of hand-editing whenever api.ts changes.';

const isObject = (v) => typeof v === 'object' && v !== null && !Array.isArray(v);

/** Deep copy with object keys sorted, as the generator's CLI (safe-stable-stringify) prints them. */
function sortKeys(v) {
  if (Array.isArray(v)) return v.map(sortKeys);
  if (!isObject(v)) return v;
  return Object.fromEntries(
    Object.keys(v)
      .sort()
      .map((k) => [k, sortKeys(v[k])]),
  );
}

/** The definition names a node references through #/definitions/ (percent-decoded). */
function refs(node, acc) {
  if (Array.isArray(node)) node.forEach((v) => refs(v, acc));
  else if (isObject(node)) {
    if (typeof node.$ref === 'string' && node.$ref.startsWith('#/definitions/')) {
      acc.add(node.$ref.slice('#/definitions/'.length).replaceAll('%3C', '<').replaceAll('%3E', '>'));
    }
    Object.values(node).forEach((v) => refs(v, acc));
  }
  return acc;
}

/** draft-07 -> 2020-12: the $ref prefix and tuple-form items. */
function convert(node) {
  if (Array.isArray(node)) return node.map(convert);
  if (!isObject(node)) return node;
  const out = {};
  for (const [k, v] of Object.entries(node)) {
    if (k === '$ref') out[k] = v.replace('#/definitions/', '#/$defs/');
    else if (k === 'items' && Array.isArray(v)) {
      out.prefixItems = convert(v);
      out.items = false;
    } else out[k] = convert(v);
  }
  return out;
}

const TYPES_GEN = '@/lib/contracts/types.gen';
const INLINE_IMPORT = /import\((['"])@\/lib\/contracts\/types\.gen\1\)\.([A-Za-z_$][\w$]*)/g;
const RE_EXPORT = /export\s+type\s*\{([^}]*)\}\s*from\s*(['"])@\/lib\/contracts\/types\.gen\2/g;

/** Where each type api.ts takes from types.gen.ts is defined: its own contract schema. */
export const CONTRACT_REFS = {
  PolicyDoc: 'policy.v1.schema.json',
  RouteSettings: 'policy.v1.schema.json#/$defs/RouteSettings',
  RouteEvaluatorRef: 'policy.v1.schema.json#/$defs/RouteEvaluatorRef',
  EvaluatorSpec: 'evaluator.v1.schema.json',
  EvaluatorDraft: 'evaluator-draft.v1.schema.json',
  CalibrationFile: 'calibration.v1.schema.json',
  CalibrationEntry: 'calibration.v1.schema.json#/$defs/CalibrationEntry',
  BenchmarkRun: 'benchmark-run.v1.schema.json',
};

/**
 * The scratch copy of api.ts: inline import types turned into named type imports, the scratch
 * pages appended, and the names taken from types.gen.ts (which the stub module declares).
 */
export function scratchSource(source) {
  const names = new Set();
  for (const m of source.matchAll(RE_EXPORT)) {
    for (const name of m[1].split(',').map((n) => n.trim()).filter(Boolean)) names.add(name);
  }
  const inline = new Set();
  const body = source.replace(INLINE_IMPORT, (_m, _q, name) => {
    inline.add(name);
    names.add(name);
    return name;
  });
  const header = inline.size > 0 ? `import type { ${[...inline].sort().join(', ')} } from '${TYPES_GEN}';\n` : '';
  for (const name of names) {
    if (!Object.hasOwn(CONTRACT_REFS, name)) throw new Error(`api.ts takes ${name} from types.gen.ts: add it to CONTRACT_REFS`);
  }
  return { text: header + body + SCRATCH_PAGES, contractTypes: [...names].sort() };
}

/** Runs the generator over a scratch copy of api.ts and returns its definitions. */
function generateDefinitions() {
  const dir = mkdtempSync(join(tmpdir(), 'trustagent-api-schema-'));
  try {
    const { text, contractTypes } = scratchSource(readFileSync(API_TS, 'utf8'));
    const api = join(dir, 'api.ts');
    writeFileSync(api, text);
    const stub = join(dir, 'types.gen.ts');
    writeFileSync(stub, contractTypes.map((name) => `export interface ${name} { [key: string]: unknown }\n`).join(''));
    const tsconfig = join(dir, 'tsconfig.json');
    writeFileSync(
      tsconfig,
      JSON.stringify({
        compilerOptions: {
          strict: true,
          noEmit: true,
          module: 'ESNext',
          moduleResolution: 'bundler',
          target: 'ES2022',
          skipLibCheck: true,
          paths: { [TYPES_GEN]: [stub] },
        },
        include: ['api.ts'],
      }),
    );
    const schema = createGenerator({ path: api, tsconfig, type: '*', expose: 'export', jsDoc: 'extended' }).createSchema('*');
    const definitions = sortKeys(schema).definitions;
    for (const name of contractTypes) definitions[name] = { $ref: CONTRACT_REFS[name] };
    return definitions;
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

/** The api.v1.schema.json document. */
export function buildSchema(definitions) {
  const keep = new Set();
  const todo = [...ROOTS];
  while (todo.length > 0) {
    const name = todo.pop();
    if (keep.has(name)) continue;
    if (!Object.hasOwn(definitions, name)) throw new Error(`missing definition: ${name}`);
    keep.add(name);
    for (const r of refs(definitions[name], new Set())) if (!keep.has(r)) todo.push(r);
  }
  const $defs = Object.fromEntries([...keep].sort().map((name) => [name, convert(definitions[name])]));
  return {
    $schema: 'https://json-schema.org/draft/2020-12/schema',
    $id: 'https://github.com/timkush1/TrustAgent/contracts/schema/api.v1.schema.json',
    title: 'TrustAgent API v1 payloads (REST and WebSocket v2)',
    description: DESCRIPTION,
    $comment: COMMENT,
    $defs,
  };
}

const text = `${JSON.stringify(buildSchema(generateDefinitions()), null, 2)}\n`;
if (process.argv.includes('--check')) {
  if (readFileSync(OUT, 'utf8') !== text) {
    console.error('contracts/schema/api.v1.schema.json is stale: run `make gen-api-schema` and commit the result.');
    process.exit(1);
  }
  console.log('contracts/schema/api.v1.schema.json is up to date.');
} else {
  writeFileSync(OUT, text);
  console.log(`wrote contracts/schema/api.v1.schema.json (${Object.keys(JSON.parse(text).$defs).length} definitions)`);
}

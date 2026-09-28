#!/usr/bin/env node
// Validates every file in contracts/ against its JSON Schema (chosen by file-name convention) and runs
// the cross-file checks that JSON Schema cannot express. Exits non-zero on any error.
//
//   node validate.mjs            errors fail the run, warnings are printed
//   node validate.mjs --strict   warnings fail the run too
//   node validate.mjs --quiet    print problems only
//   node validate.mjs --root DIR validate another contracts tree (tests); its parent is the repo root
//
// Repo files that follow a contract (config/policies.yaml, config/calibration/*.json,
// docs/benchmarks/results/*.json) are validated too when they exist.

import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { basename, dirname, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import Ajv2020 from 'ajv/dist/2020.js';
import addFormats from 'ajv-formats';
import YAML from 'yaml';

const argv = process.argv.slice(2);
const rootAt = argv.indexOf('--root');
if (rootAt >= 0 && !argv[rootAt + 1]) {
  console.error('usage: node validate.mjs [--strict] [--quiet] [--root DIR]');
  process.exit(2);
}
const ROOT = rootAt >= 0 ? resolve(argv[rootAt + 1]) : dirname(fileURLToPath(import.meta.url));
const REPO = join(ROOT, '..');
const SCHEMA_BASE = 'https://github.com/timkush1/TrustAgent/contracts/schema/';
const args = new Set(argv);
const STRICT = args.has('--strict');
const QUIET = args.has('--quiet');
const EPS = 1e-9;

// ------------------------------------------------------------------------------------------ reporting

const errors = [];
const warnings = [];
const error = (file, msg) => errors.push(`${file}: ${msg}`);
const warn = (file, msg) => warnings.push(`${file}: ${msg}`);
const info = (msg) => QUIET || console.log(msg);

// ------------------------------------------------------------------------------------------ file map

// Each contract file maps to a schema by path. schema: null means parse-only (no schema yet);
// every file must match exactly one rule, so a new kind of file cannot slip in unvalidated.
const RULES = [
  { re: /^schema\/[a-z0-9-]+\.v[0-9]+\.schema\.json$/, schema: '$schema' },
  { re: /^config\/keys\.v1\.json$/, schema: 'keys.v1' },
  { re: /^evaluators\/[a-z][a-z0-9_]{1,47}\.v[0-9]+\.json$/, schema: 'evaluator.v1' },
  { re: /^question-sets\/[a-z][a-z0-9-]{1,47}\.v[0-9]+\.json$/, schema: 'question-set.v1' },
  { re: /^cascade\/[a-z0-9-]+\.v[0-9]+\.json$/, schema: null },
  { re: /^policies\/presets\.v1\.json$/, schema: 'presets.v1' },
  { re: /^policies\/[a-z0-9.-]+\.ya?ml$/, schema: 'policy.v1' },
  { re: /^rules\/[a-z0-9-]+\.v[0-9]+\.json$/, schema: null },
  { re: /^prompts\/[a-z0-9-]+\.v[0-9]+\.json$/, schema: null },
  { re: /^pricing\/prices\.v1\.json$/, schema: 'pricing.v1' },
  { re: /^pricing\/assumptions\.v1\.json$/, schema: 'pricing-assumptions.v1' },
  { re: /^claims\/value-claims\.v1\.json$/, schema: 'value-claims.v1' },
  { re: /^claims\/measured\.json$/, schema: 'measured.v1' },
  { re: /^demo\/scenarios\.v1\.json$/, schema: 'scenarios.v1' },
  { re: /^ui\/outcomes\.v1\.json$/, schema: 'ui-outcomes.v1' },
  { re: /^ui\/copy\.v1\.json$/, schema: 'ui-copy.v1' },
  { re: /^ui\/glossary\.v1\.json$/, schema: 'ui-glossary.v1' },
  { re: /^ui\/commands\.v1\.json$/, schema: 'ui-commands.v1' },
  { re: /^fixtures\/(api|vendor)\/[A-Za-z0-9._-]+\.json$/, schema: null },
  { re: /^vectors\/[^/]+\/[^/]+\.json$/, schema: 'vector.v1' },
  { re: /^superseded\.v1\.json$/, schema: 'superseded.v1' },
];

// Files in contracts/ that are not contract data.
// tools/ holds generators (contracts/tools/api-schema), not contract data.
const NOT_DATA = /^(README\.md|vectors\/README\.md|go\.mod|go\.sum|[a-z_]+\.go|validate(\.test)?\.mjs|package\.json|package-lock\.json)$|^tools\/|(^|\/)\.gitkeep$/;

const EXTERNAL = [
  { file: 'config/policies.yaml', schema: 'policy.v1' },
  { dir: 'config/calibration', re: /\.json$/, schema: 'calibration.v1' },
  { dir: 'docs/benchmarks/results', re: /\.json$/, schema: 'benchmark-run.v1' },
];

function walk(dir) {
  const out = [];
  for (const name of readdirSync(dir).sort()) {
    if (name === 'node_modules' || (name.startsWith('.') && name !== '.gitkeep')) continue;
    const full = join(dir, name);
    if (statSync(full).isDirectory()) out.push(...walk(full));
    else out.push(full);
  }
  return out;
}

const posix = (p) => p.split(sep).join('/');

function parse(full, rel) {
  const text = readFileSync(full, 'utf8');
  try {
    if (/\.ya?ml$/.test(full)) {
      const doc = YAML.parseDocument(text, { uniqueKeys: true });
      if (doc.errors.length) throw new Error(doc.errors.map((e) => e.message).join('; '));
      return { text, data: doc.toJS() };
    }
    return { text, data: JSON.parse(text) };
  } catch (e) {
    error(rel, `does not parse: ${e.message}`);
    return { text, data: undefined };
  }
}

// ------------------------------------------------------------------------------------------ schemas

// strictRequired is off: `required` inside if/then refers to properties declared by the parent schema.
const ajv = new Ajv2020({
  strict: true, strictTypes: false, strictTuples: false, strictRequired: false, allowUnionTypes: true, allErrors: true,
});
addFormats(ajv);

const docs = new Map(); // rel -> { data, text, schema }
const schemaNames = new Set();
let parseOnly = 0;

for (const full of walk(ROOT)) {
  const rel = posix(relative(ROOT, full));
  if (NOT_DATA.test(rel)) continue;
  const rule = RULES.find((r) => r.re.test(rel));
  if (!rule) {
    error(rel, 'no schema mapping for this path; add a rule to RULES in validate.mjs (or fix the file name)');
    continue;
  }
  const { data, text } = parse(full, rel);
  if (data === undefined) continue;
  docs.set(rel, { data, text, schema: rule.schema });
  if (rule.schema === '$schema') {
    const name = basename(rel).replace(/\.schema\.json$/, '');
    if (data.$id !== `${SCHEMA_BASE}${basename(rel)}`) error(rel, `$id must be ${SCHEMA_BASE}${basename(rel)}`);
    if (data.$schema !== 'https://json-schema.org/draft/2020-12/schema') error(rel, '$schema must be draft 2020-12');
    try {
      ajv.addSchema(data);
      schemaNames.add(name);
    } catch (e) {
      error(rel, `cannot add schema: ${e.message}`);
    }
  } else if (rule.schema === null) {
    parseOnly++;
  }
}

for (const name of schemaNames) {
  try {
    ajv.getSchema(`${SCHEMA_BASE}${name}.schema.json`);
  } catch (e) {
    error(`schema/${name}.schema.json`, `does not compile: ${e.message}`);
  }
}

function validate(rel, data, schemaName) {
  if (!schemaNames.has(schemaName)) {
    error(rel, `schema ${schemaName} is missing`);
    return false;
  }
  let fn;
  try {
    fn = ajv.getSchema(`${SCHEMA_BASE}${schemaName}.schema.json`);
  } catch {
    return false; // compile error already reported
  }
  if (fn(data)) return true;
  for (const e of fn.errors.slice(0, 20)) {
    const where = e.instancePath || '/';
    const extra = e.params && Object.keys(e.params).length ? ` ${JSON.stringify(e.params)}` : '';
    error(rel, `${where} ${e.message}${extra} [${schemaName}]`);
  }
  if (fn.errors.length > 20) error(rel, `…and ${fn.errors.length - 20} more schema errors`);
  return false;
}

let validated = 0;
const valid = new Map(); // rel -> data, only documents that passed their schema
for (const [rel, doc] of docs) {
  if (!doc.schema || doc.schema === '$schema') continue;
  validated++;
  if (validate(rel, doc.data, doc.schema)) valid.set(rel, doc.data);
}

const external = new Map(); // repo-relative path -> { data, schema }
for (const ext of EXTERNAL) {
  const files = ext.file
    ? [ext.file].filter((f) => existsSync(join(REPO, f)))
    : existsSync(join(REPO, ext.dir))
      ? readdirSync(join(REPO, ext.dir)).filter((f) => ext.re.test(f)).map((f) => `${ext.dir}/${f}`)
      : [];
  for (const f of files) {
    const label = `../${f}`;
    const { data } = parse(join(REPO, f), label);
    if (data === undefined) continue;
    validated++;
    if (validate(label, data, ext.schema)) external.set(label, { data, schema: ext.schema });
  }
}

// ------------------------------------------------------------------------------------------ helpers

const isObj = (v) => v !== null && typeof v === 'object' && !Array.isArray(v);
const rawSchema = (name) => docs.get(`schema/${name}.schema.json`)?.data; // the JSON, without compiling
const byPrefix = (prefix) => [...valid].filter(([rel]) => rel.startsWith(prefix));
const get = (rel) => valid.get(rel);

function strings(v, out = []) {
  if (typeof v === 'string') out.push(v);
  else if (Array.isArray(v)) v.forEach((x) => strings(x, out));
  else if (isObj(v)) Object.values(v).forEach((x) => strings(x, out));
  return out;
}

function checkUnique(rel, items, key, what) {
  const seen = new Set();
  for (const it of items) {
    const k = typeof key === 'function' ? key(it) : it[key];
    if (seen.has(k)) error(rel, `duplicate ${what} ${JSON.stringify(k)}`);
    seen.add(k);
  }
  return seen;
}

// Objects merge key by key, arrays and scalars replace (SPEC §2.7).
function merge(base, over) {
  if (!isObj(base) || !isObj(over)) return over === undefined ? base : over;
  const out = { ...base };
  for (const [k, v] of Object.entries(over)) out[k] = merge(base[k], v);
  return out;
}

// ------------------------------------------------------------------------------------------ evaluators

const DETECTORS = new Set(['numbers_not_in_evidence', 'dates_not_in_evidence', 'urls_not_in_evidence',
  'quotes_not_in_evidence', 'citation_out_of_range', 'secret_pattern', 'system_prompt_leak']);
const registry = new Map(); // "<id>@<v>" -> spec
const evaluatorIds = new Set();
for (const [rel, spec] of byPrefix('evaluators/')) {
  const m = basename(rel).match(/^(.+)\.v([0-9]+)\.json$/);
  if (m[1] !== spec.id || Number(m[2]) !== spec.version) error(rel, `file name must be ${spec.id}.v${spec.version}.json`);
  if (spec.builtin !== true) error(rel, 'built-in templates must set "builtin": true');
  registry.set(`${spec.id}@${spec.version}`, spec);
  evaluatorIds.add(spec.id);
}
const registryEmpty = registry.size === 0;

function checkEvaluatorHardRules(rel, spec) {
  const q = spec.question;
  // risk_weights keys are option keys; risk_levels are below the number of criteria.
  if (q.type === 'choice') {
    for (const k of Object.keys(spec.signal.risk_weights ?? {})) {
      if (!(k in q.criteria)) error(rel, `signal.risk_weights key "${k}" is not an option of the question`);
    }
  }
  if (q.type === 'score') {
    for (const l of spec.signal.risk_levels ?? []) {
      if (l >= q.criteria.length) error(rel, `signal.risk_levels value ${l} is not below the ${q.criteria.length} criteria`);
    }
  }
  // escalate_from < warn_at <= critical_at (spec values; route overrides are checked by the Go loader).
  const warnAt = spec.outcomes.warn_at;
  const crit = spec.outcomes.critical_at ?? null;
  const ef = spec.escalation?.escalate_from ?? null;
  if (crit !== null && !(warnAt <= crit + EPS)) error(rel, `warn_at ${warnAt} must be <= critical_at ${crit}`);
  if (ef !== null && !(ef < warnAt - EPS)) error(rel, `escalate_from ${ef} must be < warn_at ${warnAt}`);
  // Backticked references resolve to a declared state field or rules.<param>.
  const fields = new Set(spec.state_fields);
  const params = new Set(Object.keys(spec.params ?? {}));
  for (const s of strings(q)) {
    for (const [, ref] of s.matchAll(/`([^`]+)`/g)) {
      const pm = ref.match(/^rules\.([a-z][a-z0-9_]*)$/);
      const ok = pm ? fields.has('rules') && params.has(pm[1]) : fields.has(ref);
      if (!ok) error(rel, `unresolved_state_reference: \`${ref}\``);
    }
    if (/[a-z][a-z0-9_]{1,47}__v[0-9]+/.test(s)) error(rel, `no_id_reference: question text contains a question id: "${s}"`);
  }
  // requires_corroboration and implies name registered evaluators or known detectors.
  for (const x of [...(spec.outcomes.requires_corroboration ?? []), ...(spec.outcomes.implies ?? [])]) {
    if (x.startsWith('det:')) {
      if (!DETECTORS.has(x.slice(4))) error(rel, `unknown detector ${x}`);
    } else if (!evaluatorIds.has(x)) {
      error(rel, `references evaluator "${x}", which is not in the registry`);
    }
  }
}
for (const [rel, spec] of byPrefix('evaluators/')) checkEvaluatorHardRules(rel, spec);

// Evaluator refs from policies, presets and scenarios. While the registry is still empty (contracts
// bootstrap), unresolved refs are warnings; once any evaluator exists they are errors.
const pendingRefs = new Map(); // rel -> Set of refs, reported once per file
function checkRef(rel, ref, where) {
  if (registry.has(ref)) return registry.get(ref);
  if (registryEmpty) {
    if (!pendingRefs.has(rel)) pendingRefs.set(rel, new Set());
    pendingRefs.get(rel).add(ref);
  } else {
    error(rel, `${where}: evaluator ${ref} is not in the registry (contracts/evaluators/)`);
  }
  return undefined;
}

// ------------------------------------------------------------------------------------------ question sets

for (const [rel, qs] of byPrefix('question-sets/')) {
  const m = basename(rel).match(/^(.+)\.v([0-9]+)\.json$/);
  if (m[1] !== qs.id || Number(m[2]) !== qs.version) error(rel, `file name must be ${qs.id}.v${qs.version}.json`);
  const ids = new Set();
  for (const ref of qs.evaluators) {
    if (!registry.has(ref)) error(rel, `evaluator ${ref} is not in the registry (contracts/evaluators/)`);
    const id = ref.split('@')[0];
    if (ids.has(id)) error(rel, `evaluator id ${id} appears twice`);
    ids.add(id);
  }
}

// ------------------------------------------------------------------------------------------ policies

// The schema defaults the checks below rely on, read from policy.v1 so they cannot drift.
const policyDefs = rawSchema('policy.v1')?.$defs;
const ROUTE_DEFAULTS = policyDefs
  ? {
      deadline_ms: policyDefs.RouteSettings.properties.deadline_ms.default,
      hedge_after_ms: policyDefs.RouteSettings.properties.hedge_after_ms.default,
      evidence: { sources: policyDefs.EvidenceSettings.properties.sources.default },
      evaluators: policyDefs.RouteSettings.properties.evaluators.default,
    }
  : { evidence: { sources: [] } };

function checkRouteSettings(rel, where, s) {
  if (s.hedge_after_ms && !(s.hedge_after_ms < s.deadline_ms)) {
    error(rel, `${where}: hedge_after_ms ${s.hedge_after_ms} must be 0 or below deadline_ms ${s.deadline_ms}`);
  }
  (s.evidence?.tag_patterns ?? []).forEach((t, i) => {
    checkPortableRegex(rel, `${where}: evidence.tag_patterns[${i}]`, t.pattern, t.flags ?? '');
  });
  const ids = new Set();
  const paramOwner = new Map();
  for (const e of s.evaluators ?? []) {
    const id = e.ref.split('@')[0];
    if (ids.has(id)) error(rel, `${where}: evaluator id ${id} is attached twice`);
    ids.add(id);
    for (const src of e.evidence_sources ?? []) {
      if (!s.evidence.sources.includes(src)) error(rel, `${where}: ${e.ref} evidence_sources ${src} is not in the route evidence.sources`);
    }
    for (const p of Object.keys(e.params ?? {})) {
      if (paramOwner.has(p)) error(rel, `${where}: param ${p} is set by both ${paramOwner.get(p)} and ${e.ref}; names must be unique per route`);
      paramOwner.set(p, e.ref);
    }
    const spec = checkRef(rel, e.ref, where);
    if (!spec) continue;
    for (const p of Object.keys(e.params ?? {})) {
      if (!(p in (spec.params ?? {}))) error(rel, `${where}: ${e.ref} declares no param ${p}`);
    }
    const t = e.thresholds ?? {};
    const warnAt = t.warn_at ?? spec.outcomes.warn_at;
    const crit = 'critical_at' in t ? t.critical_at : spec.outcomes.critical_at ?? null;
    const ef = 'escalate_from' in t ? t.escalate_from : spec.escalation?.escalate_from ?? null;
    if (crit !== null && !(warnAt <= crit + EPS)) error(rel, `${where}: ${e.ref} warn_at ${warnAt} must be <= critical_at ${crit}`);
    if (ef !== null && !(ef < warnAt - EPS)) error(rel, `${where}: ${e.ref} escalate_from ${ef} must be < warn_at ${warnAt}`);
  }
}

function checkPolicy(rel, doc) {
  const defaults = merge(ROUTE_DEFAULTS, doc.defaults ?? {});
  checkRouteSettings(rel, 'defaults', defaults);
  checkUnique(rel, doc.routes ?? [], 'name', 'route name');
  for (const r of doc.routes ?? []) {
    const { name, description, match, ...settings } = r;
    checkRouteSettings(rel, `route ${name}`, merge(defaults, settings));
  }
}

for (const [rel, doc] of byPrefix('policies/')) if (/\.ya?ml$/.test(rel)) checkPolicy(rel, doc);
for (const [label, ext] of external) if (ext.schema === 'policy.v1') checkPolicy(label, ext.data);

const presets = get('policies/presets.v1.json');
if (presets) {
  checkUnique('policies/presets.v1.json', presets.presets, 'id', 'preset id');
  for (const p of presets.presets) {
    checkRouteSettings('policies/presets.v1.json', `preset ${p.id}`, merge(ROUTE_DEFAULTS, p.settings));
  }
}

// ------------------------------------------------------------------------------------------ pricing

const prices = get('pricing/prices.v1.json');
const priceKey = (e) => `${e.provider}\u0000${e.match}`;
const priceEntries = new Map();
if (prices) {
  checkUnique('pricing/prices.v1.json', prices.entries, priceKey, 'entry (provider, match)');
  for (const e of prices.entries) priceEntries.set(priceKey(e), e);
}

const assumptions = get('pricing/assumptions.v1.json');
const figureValues = new Map(); // figure id -> { value, tolerance }
if (assumptions && prices) {
  const REL = 'pricing/assumptions.v1.json';
  const aliases = new Map();
  for (const [alias, ref] of Object.entries(assumptions.prices)) {
    const e = priceEntries.get(priceKey(ref));
    if (!e) error(REL, `prices.${alias} (${ref.provider} ${ref.match}) is not in pricing/prices.v1.json`);
    else aliases.set(alias, e);
  }
  const quantity = (q, where) => {
    if (typeof q === 'number') return q;
    if (typeof q === 'string') {
      if (!(q in assumptions.inputs)) throw new Error(`${where}: unknown input "${q}"`);
      return assumptions.inputs[q].value;
    }
    if ('sum' in q) return q.sum.reduce((a, x) => a + quantity(x, where), 0);
    if ('product' in q) return q.product.reduce((a, x) => a * quantity(x, where), 1);
    return 1 - quantity(q.complement, where);
  };
  const figures = new Map(assumptions.figures.map((f) => [f.id, f]));
  checkUnique(REL, assumptions.figures, 'id', 'figure id');
  const perAnswer = new Map();
  const visiting = new Set();
  const costOf = (id) => {
    if (perAnswer.has(id)) return perAnswer.get(id);
    const f = figures.get(id);
    if (!f) throw new Error(`unknown figure "${id}"`);
    if (!f.terms) throw new Error(`figure "${id}" is a ratio and cannot be used as a cost term`);
    if (visiting.has(id)) throw new Error(`figure "${id}" references itself`);
    visiting.add(id);
    let total = 0;
    for (const t of f.terms) {
      if ('figure' in t) {
        total += quantity(t.weight, id) * costOf(t.figure);
      } else {
        const e = aliases.get(t.price);
        if (!e) throw new Error(`${id}: unknown price alias "${t.price}"`);
        const calls = quantity(t.calls, id);
        total += (calls * (quantity(t.input_tokens, id) * e.input_per_mtok + quantity(t.output_tokens, id) * e.output_per_mtok)) / 1e6;
      }
    }
    visiting.delete(id);
    perAnswer.set(id, total);
    return total;
  };
  for (const f of assumptions.figures) {
    try {
      const tol = f.tolerance ?? 0.005;
      let computed;
      if (f.terms) computed = costOf(f.id) * 1000;
      else computed = costOf(f.ratio.numerator) / costOf(f.ratio.denominator);
      const stated = f.terms ? f.per_1k_usd : f.value;
      if (Math.abs(computed - stated) > tol + EPS) {
        error(REL, `figure ${f.id}: stated ${stated} but the terms compute ${computed.toFixed(4)} (tolerance ${tol})`);
      }
      figureValues.set(f.id, { value: computed, tolerance: tol });
    } catch (e) {
      error(REL, `figure ${f.id}: ${e.message}`);
    }
  }
}

// ------------------------------------------------------------------------------------------ claims

const measured = get('claims/measured.json');
if (measured) {
  const REL = 'claims/measured.json';
  const values = Object.values(measured.values);
  if (measured.status === 'not_measured') {
    if (values.some((v) => v.value !== null || v.run_id !== null) || measured.runs.length) {
      error(REL, 'status not_measured requires every value and run_id to be null and runs to be empty');
    }
  } else {
    for (const [k, v] of Object.entries(measured.values)) {
      if (v.value !== null && v.run_id !== null && !measured.runs.includes(v.run_id)) error(REL, `values.${k}.run_id ${v.run_id} is not in runs`);
    }
  }
}

const claims = get('claims/value-claims.v1.json');
const claimIds = new Set();
if (claims) {
  const REL = 'claims/value-claims.v1.json';
  for (const id of checkUnique(REL, claims.claims, 'id', 'claim id')) claimIds.add(id);
  for (const c of claims.claims) {
    if (['third_party', 'vendor'].includes(c.label) && (!c.source_url || !c.as_of)) {
      error(REL, `${c.id}: ${c.label} claims need source_url and as_of`);
    }
    if (c.label === 'measured' && (c.value !== null || !c.measured_ref || !c.empty_display)) {
      error(REL, `${c.id}: measured claims have value null (read from measured.json), a measured_ref and an empty_display`);
    }
    if (c.label !== 'measured' && c.value === null) error(REL, `${c.id}: only measured claims may have a null value`);
    if (c.assumption_ref !== undefined) {
      const f = figureValues.get(c.assumption_ref);
      if (!f) error(REL, `${c.id}: assumption_ref ${c.assumption_ref} is not a computable figure in pricing/assumptions.v1.json`);
      else if (Math.abs(c.value - f.value) > f.tolerance + EPS) {
        error(REL, `${c.id}: value ${c.value} differs from figure ${c.assumption_ref} (${f.value.toFixed(4)}, tolerance ${f.tolerance})`);
      }
    } else if (c.label === 'estimate' && c.unit.startsWith('usd')) {
      warn(REL, `${c.id}: cost estimate without an assumption_ref cannot be checked against pricing/assumptions.v1.json`);
    }
    if (c.price_ref !== undefined) {
      const e = priceEntries.get(priceKey(c.price_ref));
      if (!e) error(REL, `${c.id}: price_ref (${c.price_ref.provider} ${c.price_ref.match}) is not in pricing/prices.v1.json`);
      else if (Math.abs(e[c.price_ref.field] - c.value) > EPS) {
        error(REL, `${c.id}: value ${c.value} differs from the price table ${c.price_ref.field} ${e[c.price_ref.field]}`);
      }
    }
    if (c.measured_ref !== undefined) {
      const v = measured?.values?.[c.measured_ref];
      if (!v) error(REL, `${c.id}: measured_ref ${c.measured_ref} is not a key of claims/measured.json values`);
      else if (v.unit !== c.unit) error(REL, `${c.id}: unit ${c.unit} differs from measured.json ${c.measured_ref} unit ${v.unit}`);
    }
  }
}

// ------------------------------------------------------------------------------------------ config keys

const DURATION = /^(0|([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+)$/;
const keys = get('config/keys.v1.json');
if (keys) {
  const REL = 'config/keys.v1.json';
  const groupIds = checkUnique(REL, keys.groups, 'id', 'group id');
  const used = new Set();
  const names = new Set();
  for (const k of keys.keys) {
    for (const n of [k.name, ...k.aliases]) {
      if (names.has(n)) error(REL, `name or alias ${n} is declared twice`);
      names.add(n);
    }
    if (!groupIds.has(k.group)) error(REL, `${k.name}: unknown group ${k.group}`);
    used.add(k.group);
    const isBuild = k.services.includes('build');
    if (isBuild !== k.name.startsWith('VITE_')) error(REL, `${k.name}: frontend build keys (services: build) are exactly the VITE_* keys`);
    if (isBuild && k.secret) error(REL, `${k.name}: a build key ends up in the public bundle and cannot be secret`);
    if (k.enum && !['enum', 'list'].includes(k.kind)) error(REL, `${k.name}: enum is only allowed for kind enum or list`);
    if (k.kind === 'enum' && !k.enum) error(REL, `${k.name}: kind enum needs an enum list`);
    if (k.secret && k.default !== '' && k.default !== null) error(REL, `${k.name}: a secret cannot ship a default`);
    if ((k.min !== undefined || k.max !== undefined) && !['int', 'float', 'bytes'].includes(k.kind)) {
      error(REL, `${k.name}: min/max are only allowed for numeric kinds`);
    }
    const d = k.default;
    if (d === null || d === '') continue;
    const bad = (why) => error(REL, `${k.name}: default ${JSON.stringify(d)} ${why}`);
    const inRange = (n) => (k.min === undefined || n >= k.min) && (k.max === undefined || n <= k.max);
    switch (k.kind) {
      case 'int':
      case 'bytes':
        if (!/^-?[0-9]+$/.test(d)) bad(`is not an integer`);
        else if (!inRange(Number(d))) bad(`is outside [${k.min}, ${k.max}]`);
        break;
      case 'float':
        if (!/^-?[0-9]+(\.[0-9]+)?$/.test(d)) bad('is not a number');
        else if (!inRange(Number(d))) bad(`is outside [${k.min}, ${k.max}]`);
        break;
      case 'bool':
        if (d !== 'true' && d !== 'false') bad('is not true|false');
        break;
      case 'duration':
        if (!DURATION.test(d)) bad('is not a Go duration');
        break;
      case 'url':
        try {
          if (!/^https?:$/.test(new URL(d).protocol)) bad('is not an http(s) URL');
        } catch {
          bad('is not a URL');
        }
        break;
      case 'enum':
        if (!k.enum.includes(d)) bad(`is not one of ${k.enum.join('|')}`);
        break;
      case 'list':
        if (k.enum) for (const item of d.split(',')) if (!k.enum.includes(item.trim())) bad(`has item ${item} outside ${k.enum.join('|')}`);
        break;
      default:
        break;
    }
  }
  for (const gid of groupIds) if (!used.has(gid)) warn(REL, `group ${gid} has no keys`);
  const basic = keys.keys.filter((k) => !k.advanced).map((k) => k.name);
  if (basic.length > 15) error(REL, `.env.example would list ${basic.length} keys (advanced: false); at most 15 are allowed`);
}

// ------------------------------------------------------------------------------------------ vectors

const vectorKinds = new Set(rawSchema('vector.v1')?.properties?.kind?.enum ?? []);
for (const [rel, doc] of docs) {
  if (!rel.startsWith('vectors/') || doc.schema !== 'vector.v1') continue;
  const [, dir, file] = rel.split('/');
  if (!vectorKinds.has(dir)) {
    error(rel, `unknown vector kind directory "${dir}" (known: ${[...vectorKinds].join(', ')})`);
    continue;
  }
  const v = valid.get(rel);
  if (!v) continue;
  if (v.kind !== dir) error(rel, `kind "${v.kind}" does not match its directory "${dir}"`);
  if (v.name !== file.replace(/\.json$/, '')) error(rel, `name "${v.name}" must equal the file name without .json`);
  // decide() inputs have a schema of their own; expected may be a subset of decision/v1, so it is not checked.
  if (v.kind === 'decide') validate(`${rel} (input)`, v.input, 'decide-input.v1');
  if (v.kind === 'lint') checkLintVector(rel, v);
}

// lint vectors: input.spec is a schema-valid evaluator (it may break a hard rule); expected is
// {errors, lints}, each a sorted list of distinct codes from SPEC §2.8.
function checkLintVector(rel, v) {
  validate(`${rel} (input.spec)`, v.input?.spec, 'evaluator.v1');
  const codes = {
    errors: ['unresolved_state_reference', 'no_id_reference'],
    lints: ['missing_escape_option', 'non_descriptive_key', 'double_negative', 'counting_or_arithmetic',
      'near_duplicate_options', 'numeric_only_score_levels', 'long_instructions', 'score_used_for_policy'],
  };
  for (const [key, known] of Object.entries(codes)) {
    const list = v.expected?.[key];
    if (!Array.isArray(list)) {
      error(rel, `expected.${key} must be a list of codes`);
      continue;
    }
    for (const c of list) if (!known.includes(c)) error(rel, `expected.${key}: unknown code ${JSON.stringify(c)}`);
    if (list.join('\n') !== [...new Set(list)].sort().join('\n')) error(rel, `expected.${key} must be sorted and distinct`);
  }
}

// ------------------------------------------------------------------------------------------ rules

// rules/*.json have no schema yet, but Go (RE2) and TypeScript both compile every {pattern, flags} in them,
// so each must compile in JS, use only the flags i, m and s, and avoid constructs RE2 lacks. Go compiles the
// same patterns in embed_test.go. Route tag_patterns in policies follow the same rule.
function nonPortableRegex(src) {
  for (let i = 0; i < src.length; i++) {
    if (src[i] === '\\') {
      if (/[1-9]/.test(src[i + 1] ?? '')) return 'a backreference';
      if (src[i + 1] === 'k' && src[i + 2] === '<') return 'a named backreference';
      i++;
    } else if (src[i] === '(' && src[i + 1] === '?') {
      const rest = src.slice(i + 2);
      if (/^[=!]/.test(rest)) return 'a lookahead';
      if (/^<[=!]/.test(rest)) return 'a lookbehind';
      if (/^[a-zA-Z-]+[:)]/.test(rest)) return 'inline flags';
    }
  }
  return null;
}

function checkPortableRegex(rel, where, pattern, flags = '') {
  if (!/^[ims]*$/.test(flags)) error(rel, `${where}: flags ${JSON.stringify(flags)} must be a subset of "ims"`);
  const bad = nonPortableRegex(pattern);
  if (bad) error(rel, `${where}: ${JSON.stringify(pattern)} uses ${bad}, which RE2 does not support`);
  try {
    new RegExp(pattern, `${flags.replace(/[^ims]/g, '')}g`);
  } catch (e) {
    error(rel, `${where}: ${JSON.stringify(pattern)} does not compile: ${e.message}`);
  }
}

function eachPattern(v, path, fn) {
  if (Array.isArray(v)) v.forEach((x, i) => eachPattern(x, `${path}[${i}]`, fn));
  else if (isObj(v)) {
    if (typeof v.pattern === 'string') fn(`${path}.pattern`, v.pattern, typeof v.flags === 'string' ? v.flags : '');
    for (const [k, x] of Object.entries(v)) if (k !== 'pattern') eachPattern(x, `${path}.${k}`, fn);
  }
}

for (const [rel, doc] of docs) {
  if (!rel.startsWith('rules/') || doc.data === undefined) continue;
  let n = 0;
  eachPattern(doc.data, '', (where, pattern, flags) => {
    n++;
    checkPortableRegex(rel, where.slice(1), pattern, flags);
  });
  if (n === 0) error(rel, 'contains no {pattern, flags} rules');
}

// ------------------------------------------------------------------------------------------ scenarios

// A scenario's expectations hold only under the policy the gateway loads: config/policies.yaml (compose
// mounts ./config at /etc/trustagent), or the SPEC example when that file is absent. These checks replay
// the parts of routing, compile and the UI outcome rules that need no evidence extraction; the Go and TS
// runners and trafficgen --expect pin the rest.

const SCENARIO_PATHS = { openai_chat: '/v1/chat/completions', openai_completions: '/v1/completions', anthropic_messages: '/v1/messages' };
const MODE_ACTIONS = { observe: ['observe'], flag: ['pass', 'flag'], block: ['pass', 'flag', 'block'], fallback: ['pass', 'flag', 'block', 'fallback'] };
const JUDGED = ['GROUNDED', 'HALLUCINATED', 'UNVERIFIABLE'];

// Route globs: "*" matches any run of characters, "?" one character.
const matchGlob = (glob, s) => new RegExp(`^${glob.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*').replace(/\?/g, '.')}$`).test(s);

// The first route whose matchers all match (SPEC §2.7), or null for the defaults route. The body extension's
// trustagent.route stands in for a missing X-TrustAgent-Route header (both are match inputs, SPEC §2.12).
// Routes matching on key_labels are skipped: the ingest key is not part of a scenario.
function scenarioRoute(policy, s) {
  const headers = new Map(Object.entries(s.headers ?? {}).map(([k, v]) => [k.toLowerCase(), v]));
  if (!headers.has('x-trustagent-route') && s.request.trustagent?.route) headers.set('x-trustagent-route', s.request.trustagent.route);
  const path = SCENARIO_PATHS[s.api_format ?? 'openai_chat'];
  const found = (policy.routes ?? []).find((r) => {
    const m = r.match ?? {};
    return !m.key_labels
      && Object.entries(m.headers ?? {}).every(([k, v]) => headers.get(k.toLowerCase()) === v)
      && (!m.paths || m.paths.some((p) => matchGlob(p, path)))
      && (!m.models || m.models.some((p) => matchGlob(p, s.request.model ?? '')));
  });
  return found ?? null;
}

// Judge fixtures: decomposer quotes are verbatim parts of the answer, verifier evidence is verbatim from the request.
function checkScenarioJudge(rel, s) {
  const texts = strings(s.request);
  (s.judge?.claims ?? []).forEach((c, i) => {
    if (c.quote !== undefined && s.response !== undefined && !s.response.includes(c.quote)) {
      error(rel, `${s.id}: judge.claims[${i}].quote ${JSON.stringify(c.quote)} is not a verbatim part of response`);
    }
    for (const e of c.evidence ?? []) {
      if (!texts.some((t) => t.includes(e))) error(rel, `${s.id}: judge.claims[${i}].evidence ${JSON.stringify(e)} does not occur in request`);
    }
  });
}

// expect fields that constrain each other, whatever the route.
function checkScenarioExpect(rel, s, outcomeRules) {
  const x = s.expect;
  if (x.mode !== undefined && !MODE_ACTIONS[x.mode].includes(x.action)) error(rel, `${s.id}: action ${x.action} is impossible in ${x.mode} mode`);
  if (x.mode === 'observe' && x.status !== undefined && x.status !== 'deferred') {
    error(rel, `${s.id}: observe routes answer with X-TrustAgent-Status deferred, not ${x.status}`);
  }
  if (x.action === 'pass' && x.would_action && x.would_action !== 'pass') error(rel, `${s.id}: action pass needs would_action pass`);
  if (x.outcome !== undefined && outcomeRules && x.status !== undefined && x.status !== 'deferred') {
    const fields = { gate_status: x.status.split(':')[0], action: x.action, would_action: x.would_action };
    const got = outcomeRules.find((r) => Object.entries(r.when).every(([k, list]) => list.includes(fields[k])))?.outcome;
    if (got !== x.outcome) {
      error(rel, `${s.id}: expect.outcome ${x.outcome}, but ui/outcomes.v1.json gives ${got} for status ${x.status}, action ${x.action}, would_action ${x.would_action}`);
    }
  }
  const claims = s.judge?.claims;
  if (['sampled_out', 'dropped', 'disabled'].includes(x.audit) && x.audit_verdict) error(rel, `${s.id}: audit ${x.audit} runs no audit, so audit_verdict must be null`);
  if (x.audit === 'not_assessed' && x.audit_verdict && x.audit_verdict !== 'NOT_ASSESSED') error(rel, `${s.id}: audit not_assessed needs audit_verdict NOT_ASSESSED`);
  if (JUDGED.includes(x.audit_verdict) && !claims?.length) error(rel, `${s.id}: audit_verdict ${x.audit_verdict} needs judge claims`);
  if (x.audit_verdict === 'NO_CLAIMS' && (!claims || claims.length)) error(rel, `${s.id}: audit_verdict NO_CLAIMS needs judge {claims: []}`);
  if (x.audit_verdict === 'NOT_ASSESSED' && s.judge !== null) error(rel, `${s.id}: audit_verdict NOT_ASSESSED makes no LLM call, so judge must be null`);
}

// Mock answers and the effective mode against the route the request lands on.
function checkScenarioRoute(rel, s, settings, policyName, seedMode) {
  const asked = new Map(); // question id -> {ref, spec, stage, implicit}
  for (const e of settings.evaluators ?? []) {
    if (e.enabled === false) continue;
    const [id, version] = e.ref.split('@');
    asked.set(`${id}__v${version}`, { ref: e.ref, spec: registry.get(e.ref), stage: e.stage ?? 'active' });
  }
  // Compile step 3: factual_claims@1 joins as an internal evaluator when on_no_evidence is not ignore and a
  // required evaluator had no evidence, so it may be answered but is not always asked.
  if ((settings.on_no_evidence ?? 'ignore') !== 'ignore' && ![...asked.values()].some((q) => q.ref.startsWith('factual_claims@'))) {
    asked.set('factual_claims__v1', { ref: 'factual_claims@1', spec: registry.get('factual_claims@1'), stage: 'active', implicit: true });
  }
  for (const [qid, answer] of Object.entries(s.mock_answers)) {
    const q = asked.get(qid);
    if (!q) error(rel, `${s.id}: mock_answers.${qid} is not asked on route ${s.route} (${policyName})`);
    else if (q.spec && answer.type !== q.spec.question.type) error(rel, `${s.id}: mock_answers.${qid} is a ${answer.type} answer to a ${q.spec.question.type} question`);
  }
  // Evaluators that do not require evidence are asked whenever the gate runs (SPEC §2.9 compile step 2),
  // unless the response is too long to send at all (step 1: est > max_response_tokens, with cpt >= 2.5).
  const x = s.expect;
  const maxTokens = settings.decision?.max_response_tokens ?? 6000;
  const mayBeTooLong = [...(s.response ?? '')].length / 2.5 > maxTokens;
  if (['ok', 'partial', 'not_assessed'].includes(x.status) && !mayBeTooLong) {
    for (const [qid, q] of asked) {
      if (q.implicit || !q.spec || q.spec.evidence.requires === 'required') continue;
      if (!(qid in s.mock_answers)) error(rel, `${s.id}: ${q.ref} needs no evidence, so it is always asked on route ${s.route}, but mock_answers has no ${qid}`);
      if (x.status === 'not_assessed' && q.stage === 'active') error(rel, `${s.id}: status not_assessed is impossible on route ${s.route}: ${q.ref} needs no evidence and is always assessed`);
    }
  }
  const mode = settings.mode ?? seedMode;
  const effective = ['block', 'fallback'].includes(mode) && s.stream && settings.streaming !== 'buffer' ? 'flag' : mode;
  if (x.mode !== undefined && x.mode !== effective) error(rel, `${s.id}: expect.mode ${x.mode}, but route ${s.route} runs in ${effective} mode (${policyName})`);
}

const scenarios = get('demo/scenarios.v1.json');
if (scenarios) {
  const REL = 'demo/scenarios.v1.json';
  checkUnique(REL, scenarios.scenarios, 'id', 'scenario id');
  const loaded = external.get('../config/policies.yaml');
  const [policyName, policy] = loaded ? ['config/policies.yaml', loaded.data] : ['policies/example.policies.yaml', get('policies/example.policies.yaml')];
  const seedMode = keys?.keys.find((k) => k.name === 'TRUTHTABLE_GATE_DEFAULT_MODE')?.default ?? 'observe';
  const outcomeRules = get('ui/outcomes.v1.json')?.rules;
  for (const s of scenarios.scenarios) {
    if (s.answer_source === 'scripted' && s.response === undefined) error(REL, `${s.id}: a scripted scenario needs response`);
    for (const qid of Object.keys(s.mock_answers)) {
      const m = qid.match(/^(.+)__v([0-9]+)$/);
      checkRef(REL, `${m[1]}@${m[2]}`, `scenario ${s.id} mock_answers`);
    }
    checkScenarioJudge(REL, s);
    checkScenarioExpect(REL, s, outcomeRules);
    if (!policy) continue;
    if (s.route !== 'default' && !(policy.routes ?? []).some((r) => r.name === s.route)) {
      warn(REL, `${s.id}: route ${s.route} is not defined in ${policyName}`);
      continue;
    }
    const route = scenarioRoute(policy, s);
    const landed = route?.name ?? 'default';
    if (landed !== s.route) {
      error(REL, `${s.id}: the request lands on route ${landed} in ${policyName}, not ${s.route} (check headers and model)`);
      continue;
    }
    const { name, description, match, ...own } = route ?? {};
    checkScenarioRoute(REL, s, merge(merge(ROUTE_DEFAULTS, policy.defaults ?? {}), own), policyName, seedMode);
  }
}

// ------------------------------------------------------------------------------------------ ui

const commands = get('ui/commands.v1.json');
const commandIds = new Set();
const PROFILES = new Set(['knowledge', 'local-llm', 'observability', 'redis', 'demo', 'load']);
if (commands) {
  const REL = 'ui/commands.v1.json';
  for (const id of checkUnique(REL, commands.commands, 'id', 'command id')) commandIds.add(id);
  for (const c of commands.commands) {
    for (const svc of c.services ?? []) {
      if (!new RegExp(`(^|[\\s/])${svc}(\\s|$)`).test(c.command)) error(REL, `${c.id}: service ${svc} does not appear in the command`);
    }
    for (const [, list] of c.command.matchAll(/COMPOSE_PROFILES=(\S+)/g)) {
      for (const p of list.split(',')) if (!PROFILES.has(p)) error(REL, `${c.id}: unknown compose profile ${p}`);
    }
  }
}

const outcomes = get('ui/outcomes.v1.json');
if (outcomes) {
  const REL = 'ui/outcomes.v1.json';
  const ids = checkUnique(REL, outcomes.outcomes, 'id', 'outcome id');
  const allIds = rawSchema('ui-outcomes.v1')?.properties?.outcomes?.items?.properties?.id?.enum ?? [];
  for (const id of allIds) if (!ids.has(id)) error(REL, `outcome ${id} is not defined`);
  const reached = new Set();
  outcomes.rules.forEach((r, i) => {
    if (!ids.has(r.outcome)) error(REL, `rules[${i}]: unknown outcome ${r.outcome}`);
    reached.add(r.outcome);
    if (Object.keys(r.when).length === 0 && i !== outcomes.rules.length - 1) error(REL, `rules[${i}]: a catch-all rule must be last`);
  });
  if (Object.keys(outcomes.rules.at(-1).when).length !== 0) error(REL, 'the last rule must be a catch-all (when: {})');
  for (const id of ids) if (!reached.has(id)) error(REL, `outcome ${id} is not produced by any rule`);
  checkUnique(REL, outcomes.reasons, 'match', 'reason match');
  if (!registryEmpty) {
    for (const id of evaluatorIds) {
      if (!outcomes.reasons.some((r) => r.match === `${id}:*`)) warn(REL, `no reason copy for evaluator ${id} (falls back to "*:*")`);
    }
  }
}

const copy = get('ui/copy.v1.json');
if (copy) {
  for (const s of strings(copy.copy)) {
    for (const [, kind, id] of s.matchAll(/\{(command|claim):([a-z0-9_]+)\}/g)) {
      if (kind === 'command' && commands && !commandIds.has(id)) error('ui/copy.v1.json', `{command:${id}} is not in ui/commands.v1.json`);
      if (kind === 'claim' && claims && !claimIds.has(id)) error('ui/copy.v1.json', `{claim:${id}} is not in claims/value-claims.v1.json`);
    }
  }
}

const glossary = get('ui/glossary.v1.json');
if (glossary) {
  const REL = 'ui/glossary.v1.json';
  const ids = checkUnique(REL, glossary.terms, 'id', 'term id');
  for (const t of glossary.terms) for (const s of t.see_also ?? []) if (!ids.has(s)) error(REL, `${t.id}: see_also ${s} is not a term`);
}

// ------------------------------------------------------------------------------------------ calibration, benchmarks

for (const [label, ext] of external) {
  if (ext.schema === 'calibration.v1') {
    const snapshotMock = ext.data.decision_model.snapshot.startsWith('mock/');
    ext.data.entries.forEach((e, i) => {
      const where = `entries[${i}] ${e.target.evaluator_id}@${e.target.evaluator_version}`;
      for (let j = 1; j < e.table.length; j++) {
        if (e.table[j] < e.table[j - 1]) {
          error(label, `${where}: table is not non-decreasing at index ${j}`);
          break;
        }
      }
      if (e.n !== e.n_pos + e.n_neg) error(label, `${where}: n must equal n_pos + n_neg`);
      const ls = e.label_source;
      if (e.quality === 'measured') {
        if (ls.uniform_audit || ls.judge) error(label, `${where}: measured quality allows human and public-dataset labels only`);
        if (e.n < 200 || e.n_pos < 30 || e.n_neg < 30) error(label, `${where}: measured quality needs n >= 200, n_pos >= 30, n_neg >= 30`);
      }
      if ((e.quality === 'mock') !== snapshotMock) error(label, `${where}: quality mock is used exactly for mock/* snapshots`);
    });
  }
  if (ext.schema === 'benchmark-run.v1') checkUnique(label, ext.data.systems, 'id', 'system id');
}

// ------------------------------------------------------------------------------------------ superseded identifiers

// Path globs as scripts/check-forbidden.mjs reads them: "**/" is zero or more directories, "**" crosses
// directories, "*" and "?" do not.
function globToRegExp(glob) {
  let src = '';
  for (let i = 0; i < glob.length; i++) {
    const c = glob[i];
    if (c === '*' && glob[i + 1] === '*') {
      if (glob[i + 2] === '/') {
        src += '(?:.*/)?';
        i += 2;
      } else {
        src += '.*';
        i += 1;
      }
    } else if (c === '*') src += '[^/]*';
    else if (c === '?') src += '[^/]';
    else src += c.replace(/[.+^${}()|[\]\\]/g, '\\$&');
  }
  return new RegExp(`^${src}$`);
}

const superseded = get('superseded.v1.json');
if (superseded) {
  const REL = 'superseded.v1.json';
  const matchers = [];
  for (const [i, it] of superseded.identifiers.entries()) {
    const at = `identifiers[${i}] ${JSON.stringify(it.pattern)}`;
    const allow = (it.allow ?? []).map(globToRegExp);
    if (it.kind === 'regex') {
      let re;
      try {
        re = new RegExp(it.pattern, it.flags ?? '');
      } catch (e) {
        error(REL, `${at} is not a valid RegExp: ${e.message}`);
        continue;
      }
      // check-forbidden rejects a pattern that matches the empty string (it would flag every line).
      if (re.test('')) error(REL, `${at} matches the empty string`);
      else matchers.push({ it, allow, test: (line) => re.test(line) });
    } else {
      if (it.flags !== undefined) error(REL, `${at}: flags are only valid with kind regex`);
      matchers.push({ it, allow, test: (line) => line.includes(it.pattern) });
    }
  }
  // Contracts themselves must never use a superseded identifier, whatever its level. As in check-forbidden,
  // lines are matched one at a time, allow globs are repo-relative, and *.md prose is not scanned (the
  // READMEs name superseded identifiers to explain them).
  const scanned = [...docs].filter(([rel]) => rel !== REL).map(([rel, d]) => [rel, d.text]);
  for (const name of ['go.mod', 'embed.go', 'embed_test.go', 'package.json', 'validate.mjs', 'validate.test.mjs']) {
    if (existsSync(join(ROOT, name))) scanned.push([name, readFileSync(join(ROOT, name), 'utf8')]);
  }
  for (const [rel, text] of scanned) {
    const lines = text.split(/\r?\n/);
    for (const { it, allow, test } of matchers) {
      if (allow.some((re) => re.test(`contracts/${rel}`))) continue;
      const n = lines.findIndex(test);
      if (n >= 0) error(`${rel}:${n + 1}`, `uses superseded identifier ${JSON.stringify(it.pattern)} (${it.area}): ${it.note}`);
    }
  }
}

// ------------------------------------------------------------------------------------------ summary

for (const [rel, refs] of pendingRefs) {
  warn(rel, `evaluator refs not checked yet, the registry (contracts/evaluators/) is empty: ${[...refs].join(', ')}`);
}

const counts = `${docs.size} contract files (${validated} validated against a schema, ${parseOnly} parse-only), ${schemaNames.size} schemas`;
for (const w of warnings) console.log(`WARN  ${w}`);
for (const e of errors) console.log(`ERROR ${e}`);
const failed = errors.length > 0 || (STRICT && warnings.length > 0);
if (failed) {
  console.log(`contracts: FAILED with ${errors.length} error(s), ${warnings.length} warning(s); ${counts}`);
  process.exit(1);
}
info(`contracts: OK; ${counts}${warnings.length ? `; ${warnings.length} warning(s)` : ''}`);

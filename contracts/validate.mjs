#!/usr/bin/env node
// Validates every file in contracts/ against its JSON Schema (chosen by file-name convention) and runs
// the cross-file checks that JSON Schema cannot express. Exits non-zero on any error.
//
//   node validate.mjs            errors fail the run, warnings are printed
//   node validate.mjs --strict   warnings fail the run too
//   node validate.mjs --quiet    print problems only
//   node validate.mjs --root DIR validate another contracts tree (tests); its parent is the repo root
//
// Repo files that follow a contract (config/policies.yaml, config/policies.pending.yaml,
// config/calibration/*.json, docs/benchmarks/results/*.json) are validated too when they exist.

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
  { re: /^prompts\/[a-z0-9-]+\.v[0-9]+\.json$/, schema: 'prompt.v1' },
  { re: /^pricing\/prices\.v1\.json$/, schema: 'pricing.v1' },
  { re: /^pricing\/assumptions\.v1\.json$/, schema: 'pricing-assumptions.v1' },
  { re: /^claims\/value-claims\.v1\.json$/, schema: 'value-claims.v1' },
  { re: /^claims\/measured\.json$/, schema: 'measured.v1' },
  { re: /^claims\/compare\.v1\.json$/, schema: 'compare.v1' },
  { re: /^demo\/scenarios\.v1\.json$/, schema: 'scenarios.v1' },
  { re: /^demo\/faults\.v1\.json$/, schema: 'scenarios.v1' },
  { re: /^ui\/outcomes\.v1\.json$/, schema: 'ui-outcomes.v1' },
  { re: /^ui\/copy\.v1\.json$/, schema: 'ui-copy.v1' },
  { re: /^ui\/glossary\.v1\.json$/, schema: 'ui-glossary.v1' },
  { re: /^ui\/commands\.v1\.json$/, schema: 'ui-commands.v1' },
  { re: /^fixtures\/api\/[^/]+\.json$/, schema: 'api.v1', apiFixture: true },
  { re: /^fixtures\/vendor\/[A-Za-z0-9._-]+\.json$/, schema: null },
  { re: /^fixtures\/vendor\/[a-z0-9-]+\/[A-Za-z0-9._-]+\.json$/, schema: null, vendorFixture: true },
  { re: /^fixtures\/prompts\/[a-z0-9-]+\.[a-z0-9_-]+\.json$/, schema: null, promptFixture: true },
  { re: /^fixtures\/load\/[a-z0-9.-]+\.json$/, schema: 'load-run.v1' },
  { re: /^fixtures\/grpc\/[^/]+\.json$/, schema: null, grpcFixture: true },
  { re: /^vectors\/[^/]+\/[^/]+\.json$/, schema: 'vector.v1' },
  { re: /^superseded\.v1\.json$/, schema: 'superseded.v1' },
];

// fixtures/api/<Type>.<case>.json is validated against api.v1.schema.json#/$defs/<Type>. A generic
// instance such as Page<TraceSummary> is spelled Page-TraceSummary in the file name.
const API_FIXTURE = /^fixtures\/api\/([A-Z][A-Za-z0-9]*(?:-[A-Z][A-Za-z0-9]*)?)\.([a-z0-9][a-z0-9_-]*(?:\.[a-z0-9][a-z0-9_-]*)*)\.json$/;
function apiFixtureName(rel) {
  const m = rel.match(API_FIXTURE);
  return m && { def: m[1].replace(/^([A-Za-z0-9]+)-([A-Za-z0-9]+)$/, '$1<$2>'), variant: m[2] };
}

// Files in contracts/ that are not contract data.
// tools/ holds generators (contracts/tools/api-schema), not contract data.
const NOT_DATA = /^(README\.md|vectors\/README\.md|go\.mod|go\.sum|[a-z_]+\.go|validate(\.test)?\.mjs|package\.json|package-lock\.json)$|^tools\/|(^|\/)\.gitkeep$/;

const EXTERNAL = [
  { file: 'config/policies.yaml', schema: 'policy.v1' },
  // Demo routes the demo and e2e stacks cannot load (handbook reads the knowledge add-on):
  // appended to the routes of config/policies.yaml, merged over its defaults (checked below).
  { file: 'config/policies.pending.yaml', schema: 'policy.v1', pending: true },
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

const docs = new Map(); // rel -> { data, text, schema, fixture? }
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
  const fixture = rule.apiFixture ? apiFixtureName(rel) : undefined;
  if (fixture === null) {
    error(rel, 'an API fixture is named <Type>.<case>.json after the api.v1 definition it holds (Page<T> as Page-T), e.g. TraceDetail.flagged.json');
    continue;
  }
  const { data, text } = parse(full, rel);
  if (data === undefined) continue;
  docs.set(rel, { data, text, schema: rule.schema, fixture });
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

// validate checks data against a schema, or against one of its $defs when def is given.
function validate(rel, data, schemaName, def) {
  if (!schemaNames.has(schemaName)) {
    error(rel, `schema ${schemaName} is missing`);
    return false;
  }
  const label = def ? `${schemaName}#/$defs/${def}` : schemaName;
  if (def && !Object.hasOwn(docs.get(`schema/${schemaName}.schema.json`)?.data?.$defs ?? {}, def)) {
    error(rel, `${schemaName}.schema.json has no definition ${def}`);
    return false;
  }
  let fn;
  try {
    fn = ajv.getSchema(`${SCHEMA_BASE}${schemaName}.schema.json${def ? `#/$defs/${encodeURIComponent(def)}` : ''}`);
  } catch {
    return false; // compile error already reported
  }
  if (!fn) {
    error(rel, `cannot resolve ${label}`);
    return false;
  }
  if (fn(data)) return true;
  for (const e of fn.errors.slice(0, 20)) {
    const where = e.instancePath || '/';
    const extra = e.params && Object.keys(e.params).length ? ` ${JSON.stringify(e.params)}` : '';
    error(rel, `${where} ${e.message}${extra} [${label}]`);
  }
  if (fn.errors.length > 20) error(rel, `…and ${fn.errors.length - 20} more schema errors`);
  return false;
}

let validated = 0;
const valid = new Map(); // rel -> data, only documents that passed their schema
for (const [rel, doc] of docs) {
  if (!doc.schema || doc.schema === '$schema') continue;
  validated++;
  if (validate(rel, doc.data, doc.schema, doc.fixture?.def)) valid.set(rel, doc.data);
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
    if (validate(label, data, ext.schema)) external.set(label, { data, schema: ext.schema, pending: ext.pending === true });
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
  for (const v of questionTextViolations(spec)) error(rel, v.message);
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

// The two hard rules on question text (SPEC §2.8): every backticked reference resolves to a declared
// state field or to rules.<param> with rules declared and the param declared, and no question id
// ("<id>__v<version>") appears in text, because questions are answered independently.
function questionTextViolations(spec) {
  const fields = new Set(spec.state_fields);
  const params = new Set(Object.keys(spec.params ?? {}));
  const out = [];
  for (const s of strings(spec.question)) {
    for (const [, ref] of s.matchAll(/`([^`]+)`/g)) {
      const pm = ref.match(/^rules\.([a-z][a-z0-9_]*)$/);
      const ok = pm ? fields.has('rules') && params.has(pm[1]) : fields.has(ref);
      if (!ok) out.push({ code: 'unresolved_state_reference', message: `unresolved_state_reference: \`${ref}\`` });
    }
    if (/[a-z][a-z0-9_]{1,47}__v[0-9]+/.test(s)) {
      out.push({ code: 'no_id_reference', message: `no_id_reference: question text contains a question id: "${s}"` });
    }
  }
  return out;
}

// The SPEC §2.8 lints (warnings in Studio; built-ins and drafter examples must be lint-clean). Rules the
// SPEC gives only by id are defined as in the TypeScript reference (frontend-react/src/lib/decision/lint.ts)
// and pinned, with the rest, by vectors/lint:
// - non_descriptive_key: no word of three or more letters in the key besides generic ones (option_a, choice_2);
// - near_duplicate_options: lowercase [a-z0-9]+ token Jaccard of two options' `what` above 0.5, neither with not_for;
// - numeric_only_score_levels: a score level whose text has no letter;
// - score_used_for_policy: any score question (score answers calibrate poorly).
const LINT_ESCAPE_KEY = /^(no_|nothing_|not_|too_short|too_generic)/;
const LINT_ESCAPE_WHAT = /greeting|too short|nothing to|not about|no .*(subject|facts|content)/i;
const LINT_DOUBLE_NEGATIVE = /\bnot\b.*\b(no|not|never|without)\b/;
const LINT_COUNTING = /more than|at least|fewer than|how many|[0-9]/;
const LINT_GENERIC_WORDS = new Set(['option', 'choice', 'answer', 'item', 'value', 'class', 'category', 'label', 'type',
  'case', 'other', 'misc', 'level', 'result']);
const LINT_MAX_INSTRUCTIONS = 600;

function evaluatorLints(spec) {
  const q = spec.question;
  const out = [];
  const lint = (code, message) => out.push({ code, message: `${code}: ${message}` });
  const optionWhat = (o) => (isObj(o) && typeof o.what === 'string' ? o.what : strings(o).join(' '));
  const hasNotFor = (o) => isObj(o) && typeof o.not_for === 'string' && o.not_for !== '';
  const tokens = (s) => new Set(s.toLowerCase().match(/[a-z0-9]+/g) ?? []);
  const jaccard = (a, b) => {
    const common = [...a].filter((t) => b.has(t)).length;
    const union = a.size + b.size - common;
    return union === 0 ? 0 : common / union;
  };
  if (q.type === 'choice') {
    const entries = Object.entries(q.criteria);
    if (entries.length >= 3 && !entries.some(([k, o]) => LINT_ESCAPE_KEY.test(k) || LINT_ESCAPE_WHAT.test(optionWhat(o)))) {
      lint('missing_escape_option', 'no option for replies with nothing to judge (a greeting, a very short reply)');
    }
    for (const [k] of entries) {
      if (!k.split(/[_0-9]+/).some((w) => w.length >= 3 && !LINT_GENERIC_WORDS.has(w))) {
        lint('non_descriptive_key', `option key ${k} does not say what the option means`);
      }
    }
    for (let i = 0; i < entries.length; i++) {
      for (let j = i + 1; j < entries.length; j++) {
        const [ka, a] = entries[i];
        const [kb, b] = entries[j];
        if (!hasNotFor(a) && !hasNotFor(b) && jaccard(tokens(optionWhat(a)), tokens(optionWhat(b))) > 0.5) {
          lint('near_duplicate_options', `${ka} and ${kb} are described almost the same way; add not_for`);
        }
      }
    }
  }
  const instructions = strings(q.instructions);
  for (const s of instructions) {
    if (LINT_DOUBLE_NEGATIVE.test(s)) lint('double_negative', `instructions use a double negative: "${s}"`);
    if (LINT_COUNTING.test(s)) lint('counting_or_arithmetic', `instructions ask for counting or arithmetic (consider escalation.numeric_or_date): "${s}"`);
  }
  if ([...instructions.join('')].length > LINT_MAX_INSTRUCTIONS) {
    lint('long_instructions', `instructions are longer than ${LINT_MAX_INSTRUCTIONS} characters`);
  }
  if (q.type === 'score') {
    q.criteria.forEach((level, i) => {
      if (!/\p{L}/u.test(strings(level).join(' '))) lint('numeric_only_score_levels', `score level ${i} has no words`);
    });
    lint('score_used_for_policy', 'score answers calibrate poorly; prefer noul or choice for policy decisions');
  }
  return out;
}

// Built-in templates must stay lint-clean (SPEC §2.8).
for (const [rel, spec] of byPrefix('evaluators/')) {
  for (const l of evaluatorLints(spec)) error(rel, `${l.message} (built-in templates must be lint-clean)`);
}

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
for (const [label, ext] of external) if (ext.schema === 'policy.v1' && !ext.pending) checkPolicy(label, ext.data);

// config/policies.pending.yaml holds only version and routes; its routes merge over the defaults of
// config/policies.yaml and their names must not clash with its routes.
const PENDING = '../config/policies.pending.yaml';
const pendingPolicy = external.get(PENDING)?.data;
const pendingRoutes = new Set();
if (pendingPolicy) {
  const base = external.get('../config/policies.yaml')?.data;
  const extra = Object.keys(pendingPolicy).filter((k) => k !== 'version' && k !== 'routes');
  if (extra.length) error(PENDING, `only version and routes belong here (they merge over config/policies.yaml): ${extra.join(', ')}`);
  if (!base) error(PENDING, 'needs config/policies.yaml, whose defaults its routes merge over');
  else {
    const defaults = merge(ROUTE_DEFAULTS, base.defaults ?? {});
    checkUnique(PENDING, [...(base.routes ?? []), ...(pendingPolicy.routes ?? [])], 'name', 'route name (across config/policies.yaml and this file)');
    for (const r of pendingPolicy.routes ?? []) {
      pendingRoutes.add(r.name);
      const { name, description, match, ...settings } = r;
      checkRouteSettings(PENDING, `route ${name}`, merge(defaults, settings));
    }
  }
}

const presets = get('policies/presets.v1.json');
if (presets) {
  checkUnique('policies/presets.v1.json', presets.presets, 'id', 'preset id');
  checkUnique('policies/presets.v1.json', presets.presets, 'name', 'preset name');
  for (const p of presets.presets) {
    const settings = merge(ROUTE_DEFAULTS, p.settings);
    checkRouteSettings('policies/presets.v1.json', `preset ${p.id}`, settings);
    checkPresetReadiness('policies/presets.v1.json', p, settings);
  }
}

// A block or fallback preset must be able to enforce once calibrated (SPEC §7.2 block readiness, §2.9):
// streams are buffered (passthrough makes the route act as flag), something can block (a block-eligible
// active evaluator or a detectors.block_on entry), every block-eligible active evaluator has a corroborator
// the preset attaches (a detector, an active evaluator, or none required), and, while require_calibration
// holds, ui_note tells the user that the preset starts as Flag.
function checkPresetReadiness(rel, preset, s) {
  const mode = s.mode;
  if (mode !== 'block' && mode !== 'fallback') return;
  const where = `preset ${preset.id}`;
  if (s.streaming !== 'buffer') error(rel, `${where}: ${mode} mode needs streaming: buffer, or streamed answers can only be flagged`);
  if (s.require_calibration !== false && !/flag/i.test(preset.ui_note ?? '')) {
    error(rel, `${where}: with require_calibration it acts as Flag until its checks are calibrated; ui_note must say so`);
  }
  const active = (s.evaluators ?? []).filter((e) => e.enabled !== false && (e.stage ?? 'active') === 'active');
  const activeIds = new Set(active.map((e) => e.ref.split('@')[0]));
  let canBlock = (s.detectors?.block_on ?? []).length > 0;
  for (const e of active) {
    const spec = registry.get(e.ref);
    if (!spec?.outcomes.block_eligible) continue;
    canBlock = true;
    const req = spec.outcomes.requires_corroboration ?? [];
    if (req.length && !req.some((x) => x.startsWith('det:') || activeIds.has(x))) {
      error(rel, `${where}: ${e.ref} can block only when one of ${req.join(', ')} agrees, and the preset attaches none of them`);
    }
  }
  if (!canBlock) error(rel, `${where}: ${mode} mode, but no active evaluator is block-eligible and detectors.block_on is empty`);
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
    } else if (c.label === 'estimate' && c.unit.startsWith('usd') && c.per_unit_of === undefined) {
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
  // One unit of a per-1,000 figure (D114): value = the usd_per_1k claim's value, or the assumptions
  // figure, divided by 1,000.
  for (const c of claims.claims) {
    if (c.per_unit_of === undefined) continue;
    const src = claims.claims.find((o) => o.id === c.per_unit_of);
    const fig = figureValues.get(c.per_unit_of);
    if (c.unit !== 'usd') error(REL, `${c.id}: per_unit_of needs unit usd`);
    if (src && src.unit !== 'usd_per_1k') error(REL, `${c.id}: per_unit_of ${c.per_unit_of} is a ${src.unit} claim, not usd_per_1k`);
    const per1k = src ? src.value : fig?.value;
    const tol = src ? EPS : (fig?.tolerance ?? 0) / 1000;
    if (per1k === undefined || per1k === null) error(REL, `${c.id}: per_unit_of ${c.per_unit_of} is neither a usd_per_1k claim nor a figure of pricing/assumptions.v1.json`);
    else if (Math.abs(c.value - per1k / 1000) > tol + EPS) {
      error(REL, `${c.id}: value ${c.value} must be ${c.per_unit_of} / 1000 (${Number((per1k / 1000).toPrecision(12))})`);
    }
  }
}

// The comparison table (D113): every row has one cell per column; a cell other than unverified says
// where it was read (source_url, the vendor's public docs) and when (as_of, the fetch date).
const compare = get('claims/compare.v1.json');
if (compare) {
  const REL = 'claims/compare.v1.json';
  const cols = checkUnique(REL, compare.columns, 'id', 'column id');
  checkUnique(REL, compare.rows, 'id', 'row id');
  for (const r of compare.rows) {
    for (const c of cols) if (!(c in r.cells)) error(REL, `${r.id}: no cell for column ${c}`);
    for (const [c, cell] of Object.entries(r.cells)) {
      if (!cols.has(c)) error(REL, `${r.id}: cell ${c} is not a column`);
      if (cell.value === 'unverified') continue;
      if (!cell.source_url || !cell.as_of) error(REL, `${r.id}.${c}: a ${cell.value} cell needs the source_url it was read from and as_of, the date it was read`);
      if (!cell.text) error(REL, `${r.id}.${c}: a ${cell.value} cell says what the vendor documents (text)`);
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
    // P4 D35: dashboard secrets live in the encrypted credential store, never in a settings row.
    if (k.secret && k.runtime !== undefined && k.runtime !== null) {
      error(REL, `${k.name}: a secret key cannot be runtime ${k.runtime}; dashboard secrets go through the credential store`);
    }
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

// ------------------------------------------------------------------------------------------ prompts

// prompts/<id>.v<version>.json (prompt.v1). The file name matches id and version; {{name}} placeholders and
// the declared variables agree; each example supplies exactly the user template's variables within their
// max_chars, and its output passes output.schema. The first call, with the longest allowed inputs, must fit
// the static playground's per-call token cap (VITE_PLAYGROUND_MAX_TOKENS), estimated at 3.5 characters per
// token. Evaluator-draft prompts get the checks in checkDrafterPrompt.
const PROMPT_CHARS_PER_TOKEN = 3.5;
const PLACEHOLDER = /\{\{([^{}]*)\}\}/g;
const placeholders = (text) => [...new Set([...text.matchAll(PLACEHOLDER)].map((m) => m[1]))];

for (const [rel, p] of byPrefix('prompts/')) {
  const m = basename(rel).match(/^(.+)\.v([0-9]+)\.json$/);
  if (m[1] !== p.id || Number(m[2]) !== p.version) error(rel, `file name must be ${p.id}.v${p.version}.json`);
  const texts = { system: p.system.join('\n'), user: p.user.join('\n'), repair: (p.repair ?? []).join('\n') };
  const used = new Set();
  for (const [name, text] of Object.entries(texts)) {
    for (const v of placeholders(text)) {
      if (!Object.hasOwn(p.variables, v)) error(rel, `${name}: {{${v}}} is not a declared variable`);
      used.add(v);
    }
    if (text.replace(PLACEHOLDER, '').includes('{{')) error(rel, `${name}: "{{" that does not start a {{name}} placeholder`);
  }
  for (const v of Object.keys(p.variables)) if (!used.has(v)) error(rel, `variables.${v} is not used by system, user or repair`);
  if (p.call.max_repairs > 0 && !placeholders(texts.repair).length) {
    error(rel, 'call.max_repairs is above 0, so repair must be a template that passes the errors on');
  }
  const userVars = placeholders(texts.user);
  for (const v of userVars) {
    if (p.variables[v] && p.variables[v].max_chars === undefined) error(rel, `variables.${v} is sent with every call and needs max_chars`);
  }
  const render = (text, input) => text.replace(PLACEHOLDER,
    (_, v) => ((input[v] ?? '').trim() === '' ? p.variables[v]?.empty ?? '' : input[v]));
  const schemaOk = schemaNames.has(p.output.schema);
  if (!schemaOk) error(rel, `output.schema ${p.output.schema} is not a schema in schema/`);

  let chars = texts.system.length;
  p.examples.forEach((ex, i) => {
    const where = `examples[${i}]`;
    for (const v of userVars) if (!Object.hasOwn(ex.input, v)) error(rel, `${where}.input has no ${v}`);
    for (const [k, value] of Object.entries(ex.input)) {
      if (!userVars.includes(k)) error(rel, `${where}.input.${k} is not a variable of the user template`);
      const max = p.variables[k]?.max_chars;
      if (max !== undefined && [...value].length > max) error(rel, `${where}.input.${k} is longer than its max_chars ${max}`);
    }
    const reply = JSON.stringify(ex.output);
    chars += render(texts.user, ex.input).length + reply.length;
    const replyTokens = Math.ceil(reply.length / PROMPT_CHARS_PER_TOKEN);
    if (replyTokens > p.call.max_tokens) {
      error(rel, `${where}.output needs about ${replyTokens} tokens, more than call.max_tokens ${p.call.max_tokens}`);
    }
    if (schemaOk && validate(`${rel} (${where}.output)`, ex.output, p.output.schema) && p.output.schema === 'evaluator-draft.v1') {
      checkDraftExample(rel, where, ex);
    }
  });
  chars += render(texts.user, Object.fromEntries(userVars.map((v) => [v, 'x'.repeat(p.variables[v]?.max_chars ?? 0)]))).length;
  const cap = Number(keys?.keys.find((k) => k.name === 'VITE_PLAYGROUND_MAX_TOKENS')?.default ?? 0);
  const need = Math.ceil(chars / PROMPT_CHARS_PER_TOKEN) + p.call.max_tokens;
  if (cap > 0 && need > cap) {
    error(rel, `the first call needs about ${need} tokens with the longest inputs (input plus call.max_tokens), `
      + `above the static playground cap of ${cap} (VITE_PLAYGROUND_MAX_TOKENS)`);
  }
  if (p.output.schema === 'evaluator-draft.v1') checkDrafterPrompt(rel, texts.system);
}

// The drafter's system text names what a draft may use, so it must keep up with the schemas: every state
// field (rules as `rules.<name>`), every category and every detector (as det:<id>).
function checkDrafterPrompt(rel, system) {
  const props = rawSchema('evaluator.v1')?.properties ?? {};
  for (const f of props.state_fields?.items?.enum ?? []) {
    const ref = f === 'rules' ? '`rules.<name>`' : `\`${f}\``;
    if (!system.includes(ref)) error(rel, `system does not describe the state field ${ref}`);
  }
  for (const c of props.category?.enum ?? []) if (!new RegExp(`\\b${c}\\b`).test(system)) error(rel, `system does not list the category ${c}`);
  for (const d of DETECTORS) if (!system.includes(`det:${d}`)) error(rel, `system does not list the detector det:${d}`);
}

// A drafter example teaches by example, so it must be a draft the drafter's own rules accept: it passes the
// hard rules and is lint-clean, names only evaluators the example offers in existing_ids, and has 3 samples
// that should fire and 3 that should not, each giving exactly the state the question reads (user_request
// when it reads user_request; evidence of at least min_chars when evidence is required, none when unread).
function checkDraftExample(rel, where, ex) {
  const d = ex.output;
  const at = `${rel} (${where}.output)`;
  checkEvaluatorHardRules(at, d);
  for (const l of evaluatorLints(d)) error(at, `${l.message} (drafter examples must be lint-clean)`);
  const offered = new Set((ex.input.existing_ids ?? '').split(',').map((s) => s.trim()).filter(Boolean));
  for (const x of [...(d.outcomes.requires_corroboration ?? []), ...(d.outcomes.implies ?? [])]) {
    if (!x.startsWith('det:') && !offered.has(x)) error(at, `names evaluator ${x}, which the example's existing_ids does not offer`);
  }
  const firing = d.test_samples.filter((s) => s.should_fire).length;
  if (firing !== 3) error(at, `test_samples: ${firing} of 6 should fire, want 3`);
  const reads = new Set(d.state_fields);
  const minChars = d.evidence.requires === 'required' ? d.evidence.min_chars ?? 40 : 0;
  d.test_samples.forEach((s, j) => {
    const sample = `test_samples[${j}]`;
    if (reads.has('user_request') !== Object.hasOwn(s, 'user_request')) {
      error(at, `${sample}: give user_request exactly when the question reads it`);
    }
    if (!reads.has('evidence') && s.evidence !== undefined) error(at, `${sample}: the question does not read evidence`);
    const evidenceChars = (s.evidence ?? []).join('').length;
    if (d.evidence.requires === 'required' && evidenceChars < minChars) {
      error(at, `${sample}: evidence is required, and ${evidenceChars} characters are fewer than min_chars ${minChars}`);
    }
  });
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
  if (v.kind === 'detectors') checkDetectorVector(rel, v);
  if (v.kind === 'evidence') {
    for (const [i, t] of (v.input?.settings?.tag_patterns ?? []).entries()) {
      checkPortableRegex(rel, `input.settings.tag_patterns[${i}]`, t.pattern, t.flags ?? '');
    }
  }
}

// detectors vectors: an aligned finding that pins its text selects exactly that text of the response
// (UTF-16 offsets, which JavaScript string indices are).
function checkDetectorVector(rel, v) {
  const response = v.input?.response;
  if (typeof response !== 'string') {
    error(rel, 'input.response must be a string');
    return;
  }
  for (const [i, f] of (v.expected?.findings ?? []).entries()) {
    if (!isObj(f) || f.start === -1) continue;
    checkSpan(rel, `expected.findings[${i}]`, response, f.start, f.end, f.text);
  }
}

// checkSpan checks a half-open UTF-16 span [start, end) of text and, when want is a string, that it
// selects want.
function checkSpan(rel, where, text, start, end, want) {
  if (!Number.isInteger(start) || !Number.isInteger(end) || start < 0 || end < start || end > text.length) {
    error(rel, `${where}: [${start}, ${end}) is not a span of a ${text.length}-code-unit text`);
    return;
  }
  if (typeof want === 'string' && text.slice(start, end) !== want) {
    error(rel, `${where}: [${start}, ${end}) selects ${JSON.stringify(text.slice(start, end))}, not ${JSON.stringify(want)}`);
  }
}

// routeLints are the lints a spec gets on a route (D98), when a lint vector gives input.attached
// (the evaluator ids attached to the route): requires_corroboration warns when no listed
// corroborator can agree, that is none is an instant check (det:…) and none is attached; an
// implied evaluator that is not attached warns on its own.
function routeLints(spec, attached) {
  const on = new Set(attached);
  const outcomes = isObj(spec.outcomes) ? spec.outcomes : {};
  const requires = Array.isArray(outcomes.requires_corroboration) ? outcomes.requires_corroboration : [];
  const implies = Array.isArray(outcomes.implies) ? outcomes.implies : [];
  const instant = (x) => typeof x === 'string' && x.startsWith('det:');
  const found = [];
  if (!requires.some((x) => instant(x) || on.has(x)) && requires.some((x) => !instant(x))) found.push({ code: 'reference_not_attached' });
  for (const x of implies) if (!instant(x) && !on.has(x)) found.push({ code: 'reference_not_attached' });
  return found;
}

// lint vectors: input.spec is a schema-valid evaluator (it may break a hard rule), and
// input.attached (optional) the route's attached evaluator ids; expected is {errors, lints}, each a
// sorted list of distinct codes from SPEC §2.8, and must be what the text hard rules, the lints
// above and (with attached) the route lints produce for the spec.
function checkLintVector(rel, v) {
  const specValid = validate(`${rel} (input.spec)`, v.input?.spec, 'evaluator.v1');
  const attached = v.input?.attached;
  if (attached !== undefined && !(Array.isArray(attached) && attached.every((a) => typeof a === 'string'))) {
    error(rel, 'input.attached must be a list of evaluator ids');
  }
  if (specValid && isObj(v.expected)) {
    const codesOf = (found) => [...new Set(found.map((f) => f.code))].sort();
    const lints = [...evaluatorLints(v.input.spec), ...(Array.isArray(attached) ? routeLints(v.input.spec, attached) : [])];
    const got = { errors: codesOf(questionTextViolations(v.input.spec)), lints: codesOf(lints) };
    for (const key of ['errors', 'lints']) {
      if (Array.isArray(v.expected[key]) && got[key].join(',') !== v.expected[key].join(',')) {
        error(rel, `expected.${key} is ${JSON.stringify(v.expected[key])}, but the SPEC §2.8 rules give ${JSON.stringify(got[key])}`);
      }
    }
  }
  const codes = {
    errors: ['unresolved_state_reference', 'no_id_reference'],
    lints: ['missing_escape_option', 'non_descriptive_key', 'double_negative', 'counting_or_arithmetic',
      'near_duplicate_options', 'numeric_only_score_levels', 'long_instructions', 'score_used_for_policy',
      'reference_not_attached'],
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

// ------------------------------------------------------------------------------------------ api fixtures

// fixtures/api holds one golden body per REST response type and WebSocket event (SPEC §2.4, §2.5); msw
// handlers and Go handler tests start from them. Beyond their api.v1 definition: every required type has
// a fixture, a WsEvent fixture is named WsEvent.<type>[.<variant>].json, the parts of one trace carry its
// trace id, gate decisions are decision.v1 documents, and aligned UTF-16 spans (claim spans, verified
// evidence spans, detector findings) select the text they claim to.
const REQUIRED_API_FIXTURES = [
  'HealthStatus', 'ReadyStatus', 'SystemStatus', 'Page<TraceSummary>', 'TraceDetail', 'Page<AuditSummary>', 'AuditDetail',
  'Page<GateDecisionRow>', 'ApiErrorBody', 'WsEvent',
];
const REQUIRED_WS_EVENTS = [
  'hello', 'hb', 'trace_started', 'gate_decision', 'audit_queued', 'audit_started', 'audit_completed', 'audit_failed',
  'audit_dropped', 'gap',
];

const fixtureTypes = new Set();
const wsEvents = new Set();
for (const [rel, doc] of docs) {
  if (!doc.fixture) continue;
  const { def, variant } = doc.fixture;
  fixtureTypes.add(def);
  const f = valid.get(rel);
  if (!f) continue;
  if (def === 'TraceDetail') checkTraceDetail(rel, '', f);
  if (def === 'AuditDetail') checkAudit(rel, '', f);
  if (def === 'WsEvent') {
    wsEvents.add(f.type);
    if (variant.split('.')[0] !== f.type) error(rel, `a WsEvent fixture is named WsEvent.${f.type}[.<variant>].json after its event type`);
    checkWsEvent(rel, f);
  }
}
for (const def of REQUIRED_API_FIXTURES) {
  if (!fixtureTypes.has(def)) error('fixtures/api', `no fixture for ${def} (${def.replace(/<(.+)>$/, '-$1')}.<case>.json)`);
}
for (const type of REQUIRED_WS_EVENTS) {
  if (!wsEvents.has(type)) error('fixtures/api', `no fixture for the WebSocket event ${type} (WsEvent.${type}.json)`);
}

// ------------------------------------------------------------------------------------------ gRPC fixtures

// fixtures/grpc/<Message>.<case>.json holds one proto message in protojson form (field names as in
// proto/evaluator.proto), shared by the Go builder and the Python consumer (P4-PLAN D34). Secrets in
// them are obvious fakes, fingerprints have the D30 form, and allowed_hosts are canonical origins (D78).
const GRPC_FIXTURE = /^fixtures\/grpc\/([A-Z][A-Za-z0-9]*)\.([a-z0-9][a-z0-9_-]*)\.json$/;
const FAKE_SECRET = /^(sk-or-v1-fixture-|sk-or-v1-e2e-|sk-test-)[A-Za-z0-9-]*$/;
const CANONICAL_ORIGIN = /^https?:\/\/[a-z0-9.-]+(:[0-9]{1,5})?$/;
const protoFile = join(REPO, 'proto', 'evaluator.proto');
const protoMessages = existsSync(protoFile)
  ? new Set([...readFileSync(protoFile, 'utf8').matchAll(/^message ([A-Za-z0-9]+) \{/gm)].map((m) => m[1]))
  : undefined;
for (const [rel, doc] of docs) {
  if (!rel.startsWith('fixtures/grpc/')) continue;
  const m = rel.match(GRPC_FIXTURE);
  if (!m) {
    error(rel, 'a gRPC fixture is named <Message>.<case>.json after the proto message it holds');
    continue;
  }
  if (protoMessages && !protoMessages.has(m[1])) error(rel, `${m[1]} is not a message of proto/evaluator.proto`);
  const visit = (v, at) => {
    if (Array.isArray(v)) v.forEach((x, i) => visit(x, `${at}[${i}]`));
    else if (v && typeof v === 'object') {
      for (const [k, x] of Object.entries(v)) {
        if (k === 'secret' && typeof x === 'string' && x !== '' && !FAKE_SECRET.test(x)) {
          error(rel, `${at}.secret must be an obvious fake (sk-or-v1-fixture-…, sk-or-v1-e2e-…, sk-test-…)`);
        }
        if (k === 'fingerprint' && typeof x === 'string' && x !== '' && !/^fp_[0-9a-f]{12}$/.test(x)) {
          error(rel, `${at}.fingerprint must be fp_ plus 12 lowercase hex characters (D30)`);
        }
        if (['allowed_hosts', 'allowedHosts'].includes(k)) {
          for (const o of Array.isArray(x) ? x : [x]) {
            let canonical = false;
            try {
              const u = new URL(o);
              canonical = CANONICAL_ORIGIN.test(o) && u.origin === o;
            } catch {
              canonical = false;
            }
            if (!canonical) error(rel, `${at}.${k}: ${JSON.stringify(o)} is not a canonical origin scheme://host[:port] (D78)`);
          }
        }
        visit(x, `${at}.${k}`);
      }
    }
  };
  visit(doc.data, '$');
}

function checkTraceDetail(rel, at, t) {
  for (const [i, d] of (t.decisions ?? []).entries()) {
    if (d.trace_id !== t.trace_id) error(rel, `${at}decisions[${i}].trace_id is not the trace's ${t.trace_id}`);
    checkDecision(rel, `${at}decisions[${i}]`, d, t.body?.response_text);
  }
  if (t.audit_detail) {
    if (t.audit_detail.trace_id !== t.trace_id) error(rel, `${at}audit_detail.trace_id is not the trace's ${t.trace_id}`);
    if (t.audit.audit_id !== t.audit_detail.audit_id) error(rel, `${at}audit.audit_id is not audit_detail.audit_id`);
    checkAudit(rel, `${at}audit_detail.`, t.audit_detail);
  }
}

// A gate decision view is the stored decision/v1 document; detector findings are offsets into the gated
// response text (secret findings are masked, so only their span is checked).
function checkDecision(rel, where, d, responseText) {
  validate(`${rel} (${where})`, d, 'decision.v1');
  if (typeof responseText !== 'string') return;
  for (const det of d.detectors ?? []) {
    for (const [i, f] of (det.findings ?? []).entries()) {
      if (f.start === -1) continue;
      checkSpan(rel, `${where}.detectors[${det.id}].findings[${i}]`, responseText, f.start, f.end,
        det.id === 'secret_pattern' ? undefined : f.text);
    }
  }
}

function checkAudit(rel, at, a) {
  for (const [i, c] of (a.claims ?? []).entries()) {
    const where = `${at}claims[${i}]`;
    if (c.span && typeof a.llm_response === 'string') {
      checkSpan(rel, `${where}.span`, a.llm_response, c.span.start, c.span.end, c.span.method === 'exact' ? c.quote ?? undefined : undefined);
    }
    for (const [j, s] of (c.evidence_spans ?? []).entries()) {
      const doc = a.context_used?.[s.doc_index];
      if (!doc) {
        if (a.context_used?.length) error(rel, `${where}.evidence_spans[${j}]: doc_index ${s.doc_index} is not in context_used`);
        continue;
      }
      if (doc.id !== s.doc_id) error(rel, `${where}.evidence_spans[${j}]: doc_id ${s.doc_id} is not context_used[${s.doc_index}].id ${doc.id}`);
      if (typeof doc.text === 'string') {
        checkSpan(rel, `${where}.evidence_spans[${j}]`, doc.text, s.start, s.end, s.kind === 'quote_verified' && s.text !== null ? s.text : undefined);
      }
    }
  }
}

function checkWsEvent(rel, e) {
  const d = e.data ?? {};
  if (e.type === 'hello' && d.epoch !== e.epoch) error(rel, 'data.epoch is not the envelope epoch');
  for (const [key, value] of [['trace', d.trace], ['decision', d.decision], ['audit', d.audit]]) {
    if (value && value.trace_id !== e.trace_id) error(rel, `data.${key}.trace_id is not the envelope trace_id ${e.trace_id}`);
  }
  if (e.type === 'gate_decision') checkDecision(rel, 'data.decision', d.decision);
  if (e.type === 'audit_completed') checkAudit(rel, 'data.audit.', d.audit);
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
// Gateway phase tags (trafficgen --phase): a scenario that needs a later phase than the walking skeleton carries one.
const PHASE_TAGS = new Set(['p2', 'p3', 'p4', 'p5']);
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

// Judge fixtures: decomposer quotes are verbatim parts of the answer, verifier evidence is verbatim from the request
// (or from the scenario's knowledge documents).
function checkScenarioJudge(rel, s) {
  const texts = [...strings(s.request), ...(s.knowledge ?? []).map((k) => k.text)];
  (s.judge?.claims ?? []).forEach((c, i) => {
    if (c.quote !== undefined && s.response !== undefined && !s.response.includes(c.quote)) {
      error(rel, `${s.id}: judge.claims[${i}].quote ${JSON.stringify(c.quote)} is not a verbatim part of response`);
    }
    for (const e of c.evidence ?? []) {
      if (!texts.some((t) => t.includes(e))) error(rel, `${s.id}: judge.claims[${i}].evidence ${JSON.stringify(e)} does not occur in request or knowledge`);
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
  const answersFor = (where, answers) => {
    for (const [qid, answer] of Object.entries(answers)) {
      const q = asked.get(qid);
      if (!q) error(rel, `${s.id}: ${where}.${qid} is not asked on route ${s.route} (${policyName})`);
      else if (q.spec && answer.type !== q.spec.question.type) error(rel, `${s.id}: ${where}.${qid} is a ${answer.type} answer to a ${q.spec.question.type} question`);
    }
  };
  answersFor('mock_answers', s.mock_answers);
  if (s.fallback) answersFor('fallback.mock_answers', s.fallback.mock_answers);
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

  // What the client receives: the block message, the fallback answer, or the answer itself. A scenario
  // with a fallback answer is one whose gate falls back (SPEC §2.12 Fallback): a re-gate that does not
  // block delivers the fallback answer with action fallback; one that blocks applies on_fail (block: the
  // block body with action block; flag: the fallback answer with action flag; original: the answer with
  // action flag).
  if (s.fallback && mode !== 'fallback') error(rel, `${s.id}: fallback needs a route in fallback mode, and ${s.route} runs in ${mode} mode`);
  if (x.action === 'fallback' && !s.fallback) error(rel, `${s.id}: action fallback needs a fallback answer`);
  const block = settings.block ?? {};
  const blocked = (block.style ?? 'message') === 'message' ? block.message : s.response; // style error: no answer text
  let delivered = x.action === 'block' ? blocked : s.response;
  if (s.fallback && mode === 'fallback') {
    const onFail = settings.fallback?.on_fail ?? 'block';
    const regate = s.fallback.expect.would_action;
    const action = regate !== 'block' ? 'fallback' : onFail === 'block' ? 'block' : 'flag';
    if (x.would_action !== 'block') error(rel, `${s.id}: a fallback answer means the gate falls back, which needs expect.would_action block`);
    if (x.action !== action) {
      error(rel, `${s.id}: the re-gate of the fallback answer would ${regate} and on_fail is ${onFail}, so expect.action is ${action}, not ${x.action}`);
    }
    delivered = action === 'block' ? blocked : regate !== 'block' || onFail === 'flag' ? s.fallback.response : s.response;
  }
  if ((x.delivered_text ?? s.response) !== delivered) {
    error(rel, `${s.id}: the client receives ${JSON.stringify(delivered)} (action ${x.action} on route ${s.route}), so expect.delivered_text must say so${x.delivered_text === undefined ? '' : `, not ${JSON.stringify(x.delivered_text)}`}`);
  }
  if (x.delivered_text !== undefined && x.delivered_text === s.response) error(rel, `${s.id}: expect.delivered_text equals response; leave it out`);
  if (x.stream_decision && !s.stream) error(rel, `${s.id}: expect.stream_decision describes a stream, but the scenario does not stream`);

  // Knowledge documents only on a route that reads the knowledge add-on, and only there.
  const readsKnowledge = (settings.evidence?.sources ?? []).includes('knowledge');
  if (readsKnowledge !== Boolean(s.knowledge?.length)) {
    error(rel, `${s.id}: ${readsKnowledge ? `route ${s.route} reads the knowledge add-on, so the scenario lists the documents it returns (knowledge)` : `knowledge documents need a route whose evidence.sources include knowledge`}`);
  }
  if (readsKnowledge && !s.sets.includes('knowledge')) error(rel, `${s.id}: a scenario that needs the knowledge add-on carries the knowledge tag`);
}

// The two packs (D119): demo/scenarios.v1.json (demo, e2e and the rest) and demo/faults.v1.json (vendor
// faults only). Both follow the same rules; the faults set and mock_faults belong to the faults pack
// alone, every scenario of either pack names its judge (fixtures, or null for no LLM call) since the
// mock judge fails an unscripted audit (Appendix F D93), and at most MAX_FAULT_RUN scenarios in a row fault the
// decision model, so the breaker never opens on a pack run (it opens after 5 consecutive vendor faults).
const MAX_FAULT_RUN = 4;
const packIds = new Map(); // scenario id -> pack file
function checkPack(REL, pack, isFaults) {
  if (!isFaults && pack.scenarios.length === 0) error(REL, 'the main pack holds at least one scenario');
  checkUnique(REL, pack.scenarios, 'id', 'scenario id');
  for (const s of pack.scenarios) {
    if (packIds.has(s.id)) error(REL, `${s.id}: the id is also a scenario of ${packIds.get(s.id)}; mock-upstream embeds both packs, so ids are unique across them`);
    else packIds.set(s.id, REL);
  }
  let run = 0;
  for (const s of pack.scenarios) {
    if (!Object.hasOwn(s, 'judge')) error(REL, `${s.id}: a scenario names its judge: fixtures, or null when no LLM call is expected (the mock judge fails an unscripted audit)`);
    if (isFaults) {
      if (!s.sets.includes('faults')) error(REL, `${s.id}: every scenario of the faults pack carries the faults set`);
      run = s.mock_faults?.decision ? run + 1 : 0;
      if (run > MAX_FAULT_RUN) error(REL, `${s.id}: more than ${MAX_FAULT_RUN} decision-model faults in a row would open the breaker; put a scenario without one between them`);
    } else {
      if (s.sets.includes('faults')) error(REL, `${s.id}: the faults set belongs to demo/faults.v1.json`);
      if (s.mock_faults) error(REL, `${s.id}: mock_faults belongs to demo/faults.v1.json, never the main pack`);
    }
  }
  const loaded = external.get('../config/policies.yaml');
  let [policyName, policy] = loaded ? ['config/policies.yaml', loaded.data] : ['policies/example.policies.yaml', get('policies/example.policies.yaml')];
  if (loaded && pendingPolicy) {
    policyName = 'config/policies.yaml + config/policies.pending.yaml';
    policy = { ...policy, routes: [...(policy.routes ?? []), ...(pendingPolicy.routes ?? [])] };
  }
  const seedMode = keys?.keys.find((k) => k.name === 'TRUTHTABLE_GATE_DEFAULT_MODE')?.default ?? 'observe';
  const outcomeRules = get('ui/outcomes.v1.json')?.rules;
  for (const s of pack.scenarios) {
    if (s.answer_source === 'scripted' && s.response === undefined) error(REL, `${s.id}: a scripted scenario needs response`);
    if (s.answer_source === 'generated' && s.response === undefined) error(REL, `${s.id}: a generated scenario needs response, the stand-in answer the zero-key demo uses`);
    if (s.sets.includes('generated') !== (s.answer_source === 'generated')) error(REL, `${s.id}: the generated set holds exactly the scenarios with answer_source generated`);
    if (pendingRoutes.has(s.route) && !s.sets.some((t) => PHASE_TAGS.has(t))) {
      error(REL, `${s.id}: route ${s.route} is in config/policies.pending.yaml, which the gateway cannot load yet, so the scenario carries a phase tag (p3)`);
    }
    if (s.sets.includes('s0') && s.sets.some((t) => PHASE_TAGS.has(t))) error(REL, `${s.id}: an s0 scenario runs on the walking skeleton and carries no phase tag`);
    for (const qid of Object.keys(s.fallback?.mock_answers ?? {})) {
      const m = qid.match(/^(.+)__v([0-9]+)$/);
      checkRef(REL, `${m[1]}@${m[2]}`, `scenario ${s.id} fallback.mock_answers`);
    }
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
for (const [rel, isFaults] of [['demo/scenarios.v1.json', false], ['demo/faults.v1.json', true]]) {
  const pack = get(rel);
  if (pack) checkPack(rel, pack, isFaults);
}
if (docs.has('demo/scenarios.v1.json') && !docs.has('demo/faults.v1.json')) error('demo', 'demo/faults.v1.json is missing (the faults pack; it may hold no scenarios)');

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

// ------------------------------------------------------------------------------------------ vendor and prompt fixtures

// fixtures/vendor/<vendor>/<endpoint>.<case>.json (D122): a vendor response as the browser and the
// gateway see it, {source, status, headers, body}. P5 fixtures are synthetic (from mock-upstream's
// faultTable and handlers, and SPEC §6), and source says so: none is presented as a capture. Header
// names are lowercase; a key in a body is an obvious fake.
const VENDOR_FIXTURE = /^fixtures\/vendor\/[a-z0-9-]+\/([a-z0-9_]+)\.([a-z0-9_]+)\.json$/;
for (const [rel, doc] of docs) {
  if (!rel.startsWith('fixtures/vendor/') || rel.split('/').length !== 4) continue;
  const f = doc.data;
  if (!VENDOR_FIXTURE.test(rel)) error(rel, 'a vendor fixture is named fixtures/vendor/<vendor>/<endpoint>.<case>.json');
  if (!isObj(f)) {
    error(rel, 'a vendor fixture is an object {source, status, headers, body}');
    continue;
  }
  const extra = Object.keys(f).filter((k) => !['source', 'request', 'status', 'headers', 'body'].includes(k));
  if (extra.length) error(rel, `unknown fields ${extra.join(', ')}`);
  if (typeof f.source !== 'string' || !/^(synthetic|capture): /.test(f.source)) error(rel, 'source starts with "synthetic: " (made from mock-upstream and the SPEC) or "capture: " (a real response)');
  if (!Number.isInteger(f.status) || f.status < 100 || f.status > 599) error(rel, 'status is an HTTP status code');
  if (!isObj(f.headers) || Object.entries(f.headers).some(([k, v]) => k !== k.toLowerCase() || typeof v !== 'string')) {
    error(rel, 'headers is an object of lowercase names to string values');
  }
  if (!('body' in f)) error(rel, 'body is the response body: JSON, or a string for a non-JSON body');
  for (const s of strings(f)) {
    for (const [k] of s.matchAll(/sk-[A-Za-z0-9_-]{8,}/g)) {
      if (!FAKE_SECRET.test(k) && !/^sk-or-v1-mock-/.test(k)) error(rel, `${k.slice(0, 12)}… is not an obvious fake key (sk-or-v1-e2e-, sk-or-v1-mock-, sk-test-)`);
    }
  }
}

// fixtures/prompts/<prompt>-render.<case>.json: a prompt rendered by the Python reference, which the
// TypeScript port renders byte for byte (D108): {prompt, case, input, rendered}.
for (const [rel, doc] of docs) {
  if (!rel.startsWith('fixtures/prompts/')) continue;
  const f = doc.data;
  if (!isObj(f) || typeof f.prompt !== 'string' || typeof f.case !== 'string' || !isObj(f.input) || !isObj(f.rendered)) {
    error(rel, 'a prompt fixture is {prompt, case, input, rendered}');
    continue;
  }
  if (!docs.has(`prompts/${f.prompt}.json`)) error(rel, `prompt ${f.prompt} is not in prompts/`);
  if (`${basename(rel).split('.')[1]}` !== f.case) error(rel, `case ${f.case} does not match the file name`);
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

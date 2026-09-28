// Tests for validate.mjs. The first suite copies contracts/ into a temporary repo, breaks one thing
// and expects the validator to report it; the second checks that schemas without a committed instance
// accept a realistic document and reject broken ones. Run with `npm test` (node --test).

import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { after, describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import Ajv2020 from 'ajv/dist/2020.js';
import addFormats from 'ajv-formats';

const HERE = dirname(fileURLToPath(import.meta.url));
const SPEC = join(HERE, '..', 'docs', 'v2', 'SPEC.md');
const POLICY = join(HERE, '..', 'config', 'policies.yaml');
const temps = [];
after(() => temps.forEach((d) => rmSync(d, { recursive: true, force: true })));

// A throwaway repo root holding a copy of contracts/ (without node_modules) and of config/policies.yaml,
// the policy the scenario checks resolve routes against.
function sandbox() {
  const repo = mkdtempSync(join(tmpdir(), 'contracts-'));
  temps.push(repo);
  const root = join(repo, 'contracts');
  cpSync(HERE, root, { recursive: true, filter: (src) => !src.includes('node_modules') });
  if (existsSync(POLICY)) {
    mkdirSync(join(repo, 'config'));
    cpSync(POLICY, join(repo, 'config', 'policies.yaml'));
  }
  const read = (rel) => JSON.parse(readFileSync(join(root, rel), 'utf8'));
  const write = (rel, data, base = root) => {
    mkdirSync(dirname(join(base, rel)), { recursive: true });
    writeFileSync(join(base, rel), typeof data === 'string' ? data : JSON.stringify(data, null, 2));
  };
  const edit = (rel, fn) => {
    const data = read(rel);
    fn(data);
    write(rel, data);
  };
  return { repo, root, read, write, edit };
}

function run(root, ...flags) {
  const r = spawnSync(process.execPath, [join(HERE, 'validate.mjs'), '--root', root, ...flags], { encoding: 'utf8' });
  return { code: r.status, out: r.stdout + r.stderr };
}

// The 11 built-in templates exactly as SPEC §2.8 prints them, plus the fields every file carries.
function specTemplates() {
  const text = readFileSync(SPEC, 'utf8');
  const start = text.indexOf('**Built-in templates**');
  const end = text.indexOf('**What each template can do in block mode:**');
  const blocks = [...text.slice(start, end).matchAll(/```json\n([\s\S]*?)```/g)].map((m) => JSON.parse(m[1]));
  return blocks.map((b) => ({ spec_version: 'evaluator/v1', version: 1, builtin: true, ...b }));
}

function withTemplates(sb) {
  for (const t of specTemplates()) sb.write(`evaluators/${t.id}.v1.json`, t);
  sb.write('question-sets/gateway-default.v1.json', {
    id: 'gateway-default', version: 1, evaluators: ['groundedness@1', 'contradiction@1', 'refusal@1', 'pii_secret_leak@1'],
  });
  return sb;
}

describe('validate.mjs', () => {
  test('the committed contracts pass', () => {
    const r = run(HERE);
    assert.equal(r.code, 0, r.out);
    assert.match(r.out, /contracts: OK/);
  });

  test('the committed contracts have no warnings (--strict passes)', () => {
    const r = run(HERE, '--strict');
    assert.equal(r.code, 0, r.out);
    assert.doesNotMatch(r.out, /WARN/);
  });

  test('strict mode fails on warnings', () => {
    const sb = sandbox();
    sb.edit('config/keys.v1.json', (k) => { k.groups.push({ id: 'unused_group', title: 'Unused' }); });
    const lenient = run(sb.root);
    assert.equal(lenient.code, 0, lenient.out);
    assert.match(lenient.out, /WARN .*group unused_group has no keys/);
    const strict = run(sb.root, '--strict');
    assert.equal(strict.code, 1, strict.out);
    assert.match(strict.out, /FAILED with 0 error\(s\), 1 warning\(s\)/);
  });

  test('the committed built-in templates are SPEC §2.8 verbatim', { skip: !existsSync(SPEC) }, () => {
    const templates = specTemplates();
    const files = readdirSync(join(HERE, 'evaluators')).filter((f) => f.endsWith('.json')).sort();
    assert.deepEqual(files, templates.map((t) => `${t.id}.v1.json`).sort());
    for (const t of templates) {
      const committed = JSON.parse(readFileSync(join(HERE, 'evaluators', `${t.id}.v1.json`), 'utf8'));
      assert.deepEqual(committed, t, `evaluators/${t.id}.v1.json differs from SPEC §2.8`);
      if (t.question.type === 'choice') {
        assert.deepEqual(Object.keys(committed.question.criteria), Object.keys(t.question.criteria), `${t.id}: option order`);
      }
    }
  });

  test('the SPEC §2.8 templates pass the schema and hard rules, and resolve policy refs', { skip: !existsSync(SPEC) }, () => {
    const sb = withTemplates(sandbox());
    const r = run(sb.root);
    assert.equal(r.code, 0, r.out);
    assert.doesNotMatch(r.out, /not checked yet/);
    assert.equal(specTemplates().length, 11);
  });

  test('evaluator hard rules are enforced', { skip: !existsSync(SPEC) }, () => {
    const sb = withTemplates(sandbox());
    sb.edit('evaluators/refusal.v1.json', (e) => { e.signal.risk_weights.not_an_option = 1; });
    sb.edit('evaluators/groundedness.v1.json', (e) => { e.question.instructions += ' Compare with `system_instructions`.'; });
    sb.edit('evaluators/contradiction.v1.json', (e) => { e.escalation.escalate_from = 0.9; });
    sb.edit('evaluators/commitments.v1.json', (e) => { e.outcomes.requires_corroboration.push('det:not_a_detector'); });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /risk_weights key "not_an_option" is not an option/);
    assert.match(r.out, /unresolved_state_reference: `system_instructions`/);
    assert.match(r.out, /escalate_from 0\.9 must be < warn_at/);
    assert.match(r.out, /unknown detector det:not_a_detector/);
  });

  test('policy refs, params and hedging are checked once templates exist', { skip: !existsSync(SPEC) }, () => {
    const sb = withTemplates(sandbox());
    const yaml = readFileSync(join(sb.root, 'policies/example.policies.yaml'), 'utf8')
      .replace('hedge_after_ms: 400', 'hedge_after_ms: 1500')
      .replace('params: {allowed_commitments:', 'params: {no_such_param:');
    sb.write('policies/example.policies.yaml', yaml);
    sb.edit('question-sets/gateway-default.v1.json', (q) => { q.evaluators.push('missing@1'); });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /hedge_after_ms 1500 must be 0 or below deadline_ms 1500/);
    assert.match(r.out, /commitments@1 declares no param no_such_param/);
    assert.match(r.out, /question-sets\/gateway-default\.v1\.json: evaluator missing@1 is not in the registry/);
  });

  test('schema violations are reported with the schema name', () => {
    const sb = sandbox();
    sb.edit('config/keys.v1.json', (k) => { k.keys[0].kind = 'number'; });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /config\/keys\.v1\.json: \/keys\/0\/kind .*\[keys\.v1\]/);
  });

  test('exactly the SPEC §2.7 .env.example keys are not advanced', { skip: !existsSync(SPEC) }, () => {
    const text = readFileSync(SPEC, 'utf8');
    const start = text.indexOf('contains exactly:**');
    assert.ok(start > 0, 'SPEC §2.7 .env.example list not found');
    const list = text.slice(start, text.indexOf('`. ', start) + 1);
    const expected = [...list.matchAll(/`([A-Z][A-Z0-9_]+)`/g)].map((m) => m[1]).sort();
    assert.equal(expected.length, 15);
    const registry = JSON.parse(readFileSync(join(HERE, 'config', 'keys.v1.json'), 'utf8'));
    assert.deepEqual(registry.keys.filter((k) => !k.advanced).map((k) => k.name).sort(), expected);
  });

  test('config key registry rules', () => {
    const sb = sandbox();
    sb.edit('config/keys.v1.json', (k) => {
      k.keys.find((x) => x.name === 'TRUTHTABLE_PORT').default = 'eighty';
      k.keys.find((x) => x.name === 'TRUTHTABLE_READ_TIMEOUT').default = '30 seconds';
      for (const x of k.keys.slice(0, 20)) x.advanced = false;
      k.keys.push({ ...k.keys.find((x) => x.name === 'BIND_ADDR') });
    });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /TRUTHTABLE_PORT: default "eighty" is not an integer/);
    assert.match(r.out, /TRUTHTABLE_READ_TIMEOUT: default "30 seconds" is not a Go duration/);
    assert.match(r.out, /\.env\.example would list \d+ keys/);
    assert.match(r.out, /name or alias BIND_ADDR is declared twice/);
  });

  test('value-claim labels, secret defaults and scripted scenarios follow their rules', () => {
    const sb = sandbox();
    sb.edit('claims/value-claims.v1.json', (c) => {
      c.claims.find((x) => x.label === 'measured').value = 1;
      c.claims.find((x) => x.label === 'third_party').as_of = null;
    });
    sb.edit('config/keys.v1.json', (k) => { k.keys.find((x) => x.name === 'OPENROUTER_API_KEY').default = 'sk-or-v1-x'; });
    sb.write('demo/scenarios.v1.json', {
      schema: 'trustagent.scenarios/v1',
      scenarios: [{
        id: 'no-response', title: 'Scripted without text', sets: ['demo'], route: 'unknown-route', stream: false, answer_source: 'scripted',
        request: { messages: [{ role: 'user', content: 'Hi' }] }, mock_answers: {}, expect: { action: 'pass' },
      }],
    });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /measured claims have value null/);
    assert.match(r.out, /third_party claims need source_url and as_of/);
    assert.match(r.out, /OPENROUTER_API_KEY: a secret cannot ship a default/);
    assert.match(r.out, /no-response: a scripted scenario needs response/);
    assert.match(r.out, /WARN .*no-response: route unknown-route is not defined/);
  });

  test('scenario judge fixtures and expectations agree with each other', () => {
    const sb = sandbox();
    sb.edit('demo/scenarios.v1.json', (d) => {
      const [pass, flag, none] = d.scenarios;
      pass.judge.claims[0].quote = 'a sentence the answer never says';
      pass.judge.claims[1].evidence = ['Monthly plans can be refunded at any time.'];
      pass.expect.outcome = 'flagged';
      flag.expect.mode = 'observe';
      flag.judge = null;
      none.judge = { claims: [] };
    });
    const r = run(sb.root);
    assert.equal(r.code, 1, r.out);
    assert.match(r.out, /grounded-pass: judge\.claims\[0\]\.quote "a sentence the answer never says" is not a verbatim part of response/);
    assert.match(r.out, /grounded-pass: judge\.claims\[1\]\.evidence "Monthly plans can be refunded at any time\." does not occur in request/);
    assert.match(r.out, /grounded-pass: expect\.outcome flagged, but ui\/outcomes\.v1\.json gives passed/);
    assert.match(r.out, /refund-contradiction-flag: action flag is impossible in observe mode/);
    assert.match(r.out, /refund-contradiction-flag: observe routes answer with X-TrustAgent-Status deferred, not ok/);
    assert.match(r.out, /refund-contradiction-flag: expect\.mode observe, but route support-bot runs in flag mode/);
    assert.match(r.out, /refund-contradiction-flag: audit_verdict HALLUCINATED needs judge claims/);
    assert.match(r.out, /no-evidence-not-assessed: audit_verdict NOT_ASSESSED makes no LLM call, so judge must be null/);
  });

  test('scenario mock answers and routes follow config/policies.yaml', { skip: !existsSync(POLICY) }, () => {
    const sb = sandbox();
    sb.edit('demo/scenarios.v1.json', (d) => {
      const [, flag, none] = d.scenarios;
      flag.mock_answers.refusal__v1 = { type: 'choice', choice: 'answers_request' };
      flag.mock_answers.groundedness__v1.type = 'choice';
      none.headers = { 'X-TrustAgent-Route': 'billing' };
    });
    let r = run(sb.root);
    assert.equal(r.code, 1, r.out);
    assert.match(r.out, /refund-contradiction-flag: mock_answers\.refusal__v1 is not asked on route support-bot \(config\/policies\.yaml\)/);
    assert.match(r.out, /refund-contradiction-flag: mock_answers\.groundedness__v1 is a choice answer to a noul question/);
    assert.match(r.out, /no-evidence-not-assessed: the request lands on route default in config\/policies\.yaml, not support-bot/);

    // A route that also asks an evidence-free evaluator always sends it, so it cannot be not_assessed.
    const other = sandbox();
    const yaml = readFileSync(POLICY, 'utf8').replace('      - ref: contradiction@1\n', '      - ref: contradiction@1\n      - ref: refusal@1\n');
    other.write('config/policies.yaml', yaml, other.repo);
    r = run(other.root);
    assert.equal(r.code, 1, r.out);
    assert.match(r.out, /grounded-pass: refusal@1 needs no evidence, so it is always asked on route support-bot, but mock_answers has no refusal__v1/);
    assert.match(r.out, /no-evidence-not-assessed: status not_assessed is impossible on route support-bot: refusal@1 needs no evidence/);
  });

  test('pricing figures and value claims cannot drift from the arithmetic', () => {
    const sb = sandbox();
    sb.edit('pricing/assumptions.v1.json', (a) => { a.figures.find((f) => f.id === 'fast_check_per_1k').per_1k_usd = 0.3; });
    sb.edit('claims/value-claims.v1.json', (c) => {
      c.claims.find((x) => x.id === 'llm_full_audit_cost_per_1k').value = 30;
      c.claims.find((x) => x.id === 'jev_input_price_per_mtok').value = 0.05;
    });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /figure fast_check_per_1k: stated 0\.3 but the terms compute 0\.2100/);
    assert.match(r.out, /llm_full_audit_cost_per_1k: value 30 differs from figure llm_full_audit_per_1k/);
    assert.match(r.out, /jev_input_price_per_mtok: value 0\.05 differs from the price table/);
  });

  test('assumptions must reference existing price entries', () => {
    const sb = sandbox();
    sb.edit('pricing/assumptions.v1.json', (a) => { a.prices.judge.match = 'claude-nonexistent*'; });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /prices\.judge \(anthropic claude-nonexistent\*\) is not in pricing\/prices\.v1\.json/);
  });

  test('unmapped files and unknown vector kinds fail', () => {
    const sb = sandbox();
    sb.write('stray.json', {});
    const vec = { vector: '1', kind: 'decide', name: 'x', status: 'draft', input: {}, expected: {} };
    sb.write('vectors/nonsense/x.json', { ...vec, kind: 'nonsense' });
    sb.write('vectors/decide/other-name.json', vec);
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /stray\.json: no schema mapping/);
    assert.match(r.out, /vectors\/nonsense\/x\.json: unknown vector kind directory "nonsense"/);
    assert.match(r.out, /vectors\/decide\/other-name\.json: name "x" must equal the file name/);
  });

  test('contracts must not use superseded identifiers', () => {
    const sb = sandbox();
    // Taken from the list itself so this file never spells a superseded identifier.
    const literal = sb.read('superseded.v1.json').identifiers.find((i) => i.kind === 'literal').pattern;
    sb.edit('ui/copy.v1.json', (c) => { c.copy.trace.details = `See ${literal}`; });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /ui\/copy\.v1\.json:\d+: uses superseded identifier/);
    assert.ok(r.out.includes(`uses superseded identifier ${JSON.stringify(literal)}`), r.out);
  });

  test('regex identifiers honour their flags and allow globs', () => {
    const sb = sandbox();
    // A header identifier with flag i must also catch the lowercase spelling, which is built here from the
    // pattern so this file never spells a superseded header.
    const header = sb.read('superseded.v1.json').identifiers.find((i) => i.kind === 'regex' && i.flags === 'i');
    const spelled = header.pattern.replace(/\\b$/, '').replace(/\(\?![^)]*\)$/, '').toLowerCase();
    sb.edit('ui/copy.v1.json', (c) => { c.copy.trace.details = `Header ${spelled}: x`; });
    let r = run(sb.root);
    assert.equal(r.code, 1, r.out);
    assert.ok(r.out.includes(`uses superseded identifier ${JSON.stringify(header.pattern)}`), r.out);
    sb.edit('superseded.v1.json', (s) => {
      s.identifiers.find((i) => i.pattern === header.pattern).allow = ['contracts/ui/*.json'];
    });
    r = run(sb.root);
    assert.equal(r.code, 0, r.out);
  });

  test('superseded.v1.json keeps the shape scripts/check-forbidden.mjs accepts', () => {
    const sb = sandbox();
    sb.edit('superseded.v1.json', (s) => {
      s.unenforced = [];
      s.identifiers[0].typo = 'x';
      s.identifiers.push({ area: 'dx', pattern: 'abc', kind: 'regex', level: 'info', note: 'unknown level' });
    });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /superseded\.v1\.json: \/ must NOT have additional properties .*"unenforced"/);
    assert.match(r.out, /\/identifiers\/0 must NOT have additional properties .*"typo"/);
    assert.match(r.out, /\/identifiers\/\d+\/level must be equal to one of the allowed values/);
  });

  test('superseded regex and flag rules are enforced', () => {
    const sb = sandbox();
    sb.edit('superseded.v1.json', (s) => {
      s.identifiers.push({ area: 'dx', pattern: 'abc', kind: 'literal', flags: 'i', note: 'literal with flags' });
      s.identifiers.push({ area: 'dx', pattern: 'x*', kind: 'regex', note: 'matches the empty string' });
      s.identifiers.push({ area: 'dx', pattern: '(', kind: 'regex', note: 'invalid' });
    });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /"abc": flags are only valid with kind regex/);
    assert.match(r.out, /"x\*" matches the empty string/);
    assert.match(r.out, /"\(" is not a valid RegExp/);
  });

  test('rule patterns must be portable between RE2 and JavaScript', () => {
    const sb = sandbox();
    sb.edit('rules/secrets.v1.json', (r) => {
      r.patterns.push({ id: 'lookbehind', pattern: '(?<=key=)[a-z]+', flags: '' });
      r.patterns.push({ id: 'backref', pattern: '(a)\\1', flags: '' });
      r.patterns.push({ id: 'inline', pattern: '(?i)abc', flags: '' });
      r.patterns.push({ id: 'badflags', pattern: 'abc', flags: 'gu' });
      r.patterns.push({ id: 'broken', pattern: '[a-', flags: '' });
    });
    const yaml = readFileSync(join(sb.root, 'policies/example.policies.yaml'), 'utf8')
      .replace('    line_headers: true\n', '    line_headers: true\n    tag_patterns: [{pattern: "(?=x)y"}]\n');
    sb.write('policies/example.policies.yaml', yaml);
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /rules\/secrets\.v1\.json: patterns\[\d+\]\.pattern: "\(\?<=key=\)\[a-z\]\+" uses a lookbehind/);
    assert.match(r.out, /uses a backreference/);
    assert.match(r.out, /"\(\?i\)abc" uses inline flags/);
    assert.match(r.out, /flags "gu" must be a subset of "ims"/);
    assert.match(r.out, /"\[a-" does not compile/);
    assert.match(r.out, /policies\/example\.policies\.yaml: defaults: evidence\.tag_patterns\[0\].* uses a lookahead/);
  });

  test('lint vectors: the spec is schema-valid and expected lists known codes, sorted', () => {
    const sb = sandbox();
    sb.edit('vectors/lint/double-negative-instructions.json', (v) => {
      v.input.spec.category = 'not_a_category';
      v.expected.lints = ['long_instructions', 'double_negative', 'made_up_lint'];
    });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /double-negative-instructions\.json \(input\.spec\): \/category must be equal to one of the allowed values/);
    assert.match(r.out, /expected\.lints: unknown code "made_up_lint"/);
    assert.match(r.out, /expected\.lints must be sorted and distinct/);
  });

  test('every outcome is defined and reachable', () => {
    const sb = sandbox();
    sb.edit('ui/outcomes.v1.json', (o) => {
      o.outcomes = o.outcomes.filter((x) => x.id !== 'error');
      o.rules = o.rules.filter((r) => r.outcome !== 'would_flag');
    });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /outcome error is not defined/);
    assert.match(r.out, /outcome would_flag is not produced by any rule/);
  });

  test('copy placeholders must resolve to commands and claims', () => {
    const sb = sandbox();
    sb.edit('ui/copy.v1.json', (c) => { c.copy.trace.details = '{command:nope} {claim:nope}'; });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /\{command:nope\} is not in ui\/commands\.v1\.json/);
    assert.match(r.out, /\{claim:nope\} is not in claims\/value-claims\.v1\.json/);
  });

  test('repo calibration files are validated when present', () => {
    const sb = sandbox();
    const table = Array.from({ length: 101 }, (_, i) => i / 100);
    table[50] = 0.1;
    const hex = 'a'.repeat(64);
    sb.write('config/calibration/typesafe__jev-1.13-20260917.json', {
      schema: 'trustagent.calibration/v1', created_at: '2026-10-05T10:00:00Z', generated_by: 'cmd/calibrate@abc1234',
      decision_model: { backend: 'openrouter-decisions', snapshot: 'typesafe/jev-1.13-20260917', family: 'typesafe/jev-1.13' },
      entries: [{
        target: { kind: 'evaluator', evaluator_id: 'groundedness', evaluator_version: 1, calibration_key: hex, params_sha256: null },
        fit_context: { state_fields: ['assistant_response', 'evidence'], partition: 'single', question_set_sha256: hex },
        method: 'isotonic', table, platt: null, quality: 'measured', n: 100, n_pos: 40, n_neg: 60,
        label_source: { human: 0, public_dataset: 100, uniform_audit: 0 }, datasets: ['ragtruth:qa@rev'],
        metrics: { ece_before: 0.11, ece_after: 0.03, brier_before: 0.09, brier_after: 0.07, auroc: 0.93 },
        reliability: [{ lo: 0, hi: 0.1, n: 10, mean_pred: 0.04, frac_pos: 0.06 }],
        recommended: { space: 'calibrated', warn_at: { value: 0.78, precision: 0.9, recall: 0.71 }, critical_at: null, critical_null_reason: 'insufficient evidence to block' },
      }],
      warnings: [],
    }, sb.repo);
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /table is not non-decreasing at index 50/);
    assert.match(r.out, /measured quality needs n >= 200/);
  });
});

// ------------------------------------------------------------------------------------------ schema samples
// Documents that have no committed instance yet (they are produced at runtime or by other streams)
// are exercised here, so each schema is known to accept a realistic document and reject a broken one.

const BASE = 'https://github.com/timkush1/TrustAgent/contracts/schema/';
function schemas() {
  const ajv = new Ajv2020({ strict: true, strictTypes: false, strictTuples: false, strictRequired: false, allowUnionTypes: true, allErrors: true });
  addFormats(ajv);
  for (const f of readdirSync(join(HERE, 'schema'))) ajv.addSchema(JSON.parse(readFileSync(join(HERE, 'schema', f), 'utf8')));
  return (name, doc) => {
    const fn = ajv.getSchema(`${BASE}${name}.schema.json`);
    return fn(doc) ? null : ajv.errorsText(fn.errors);
  };
}

const TRACE = '0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a90';
const HEX = 'b'.repeat(64);
const groundedness = () => specTemplates().find((t) => t.id === 'groundedness');

function sampleDecision() {
  const result = {
    ref: 'groundedness@1', stage: 'active', internal: false, status: 'assessed', reason: null, type: 'noul',
    answer: { type: 'noul', noul: 0.97 }, probabilities: null, risk_raw: 0.97, risk: 0.97, threshold_space: 'raw',
    calibrated: false, calibration_state: 'none', calibration_warning: null, level: 'critical', capped: null, top: null,
    vendor_confidence: null, confidence_source: null, escalate: null, corroborated: false, block_eligible: true,
    thresholds: { warn_at: 0.8, critical_at: 0.95, escalate_from: 0.4 },
  };
  return {
    spec: 'decision/v1', trace_id: TRACE, attempt: 0, created_at: '2026-09-27T12:00:00.123456789Z', route: 'support-bot',
    policy_version: 3, mode: 'block', effective_mode: 'block', fail: 'open', status: 'ok', skip_reason: null, action: 'flag',
    would_action: 'flag', enforced: true, verdict: 'unsupported', max_risk: 0.97, calibration: 'none', degraded: null,
    disagreement: false, deep_audit: 'high', reasons: ['groundedness:critical', 'needs_second_signal'],
    evaluators: { 'groundedness@1': result },
    detectors: [{ id: 'numbers_not_in_evidence', ran: true, fired: true, findings: [{ text: '60', start: 34, end: 36 }] }],
    excluded: [], late: false, gate_ms: 212,
    decision_model: {
      backend: 'openrouter-decisions', model_requested: 'typesafe/jev-1.13', snapshot: 'typesafe/jev-1.13-20260917', calls: 1,
      attempts: 1, hedged: false, latency_ms: 212, connect_ms: 3, input_tokens: 4550, cost_usd: 0.00019, cost_source: 'reported',
    },
    compile: {
      cpt: 4, requests: 1, question_set_sha256: HEX, request_sha256: [HEX], packed_evidence_ids: ['refund-policy'],
      truncated_ids: [], est_tokens: [4550],
    },
    evidence_summary: {
      configured: ['request_system', 'request_context', 'tool_results', 'extension'],
      used: [{ source: 'request_system', items: 1, labels: ['refund-policy.md'], tokens: 30 }],
      empty: ['request_context', 'tool_results', 'extension'], knowledge: 'not_requested', truncated: false, tokens: 30,
    },
  };
}

function sampleDecideInput() {
  return {
    policy: { configured_mode: 'block', fail: 'open', require_calibration: true, on_no_evidence: 'ignore', detectors: { flag_on: ['urls_not_in_evidence'], block_on: [] } },
    evaluators: [{ ref: 'groundedness@1', spec: groundedness(), stage: 'active', internal: false, thresholds: { space: 'raw' } }],
    answers: { groundedness__v1: { type: 'noul', noul: 0.97 } },
    question_map: { groundedness__v1: 'groundedness@1' },
    excluded: [],
    calibration: {},
    meta: {
      backend: 'openrouter-decisions', stream: 'none', evidence_present: true, evidence_truncated: false, knowledge_only: false,
      skip_reason: null, request_status: { 0: 'ok' }, qid_request: { groundedness__v1: 0 }, question_errors: {},
    },
    detectors: { numbers_not_in_evidence: { ran: true, fired: false } },
  };
}

describe('schemas accept realistic documents', { skip: !existsSync(SPEC) }, () => {
  const check = schemas();

  test('evaluator-draft.v1', () => {
    const { id, version, builtin, ...rest } = groundedness();
    const sample = { assistant_response: 'Refunds within 60 days.', evidence: ['Refunds within 30 days.'], should_fire: true };
    const draft = { ...rest, rationale: 'A yes/no question keeps the check sharp.', split_suggestions: [], test_samples: Array(6).fill(sample) };
    assert.equal(check('evaluator-draft.v1', draft), null);
    assert.notEqual(check('evaluator-draft.v1', { ...draft, id }), null, 'id is not part of a draft');
    assert.notEqual(check('evaluator-draft.v1', { ...draft, test_samples: [sample] }), null, 'exactly 6 samples');
    assert.notEqual(check('evaluator-draft.v1', { ...draft, signal: { risk_weights: { yes: 1 } } }), null, 'noul has no risk weights');
  });

  test('decision.v1', () => {
    const d = sampleDecision();
    assert.equal(check('decision.v1', d), null);
    assert.notEqual(check('decision.v1', { ...d, reasons: ['made_up_reason'] }), null);
    assert.notEqual(check('decision.v1', { ...d, action: 'pending' }), null);
    assert.notEqual(check('decision.v1', { ...d, created_at: '2026-09-27T12:00:00+02:00' }), null, 'UTC only');
  });

  test('decide-input.v1', () => {
    const input = sampleDecideInput();
    assert.equal(check('decide-input.v1', input), null);
    assert.notEqual(check('decide-input.v1', { ...input, question_map: { 'groundedness@1': 'groundedness@1' } }), null);
    const noSpace = sampleDecideInput();
    delete noSpace.evaluators[0].thresholds.space;
    assert.notEqual(check('decide-input.v1', noSpace), null);
  });

  test('policy.v1 rejects superseded and invalid fields', () => {
    assert.equal(check('policy.v1', { version: 1, routes: [{ name: 'a', match: {}, mode: 'flag' }] }), null);
    assert.notEqual(check('policy.v1', { version: 1, routes: [{ name: 'default', match: {} }] }), null, '"default" is reserved');
    assert.notEqual(check('policy.v1', { version: 1, defaults: { evaluators: [{ ref: 'groundedness@latest' }] } }), null);
    assert.notEqual(check('policy.v1', { version: 1, defaults: { hedge_after_ms: 50 } }), null);
    assert.notEqual(check('policy.v1', { version: 1, defaults: { deep_audit: { verifier: 'cascade' } } }), null);
    assert.notEqual(check('policy.v1', { version: 1, routes: [{ name: 'a', match: {}, gate_mode: 'flag' }] }), null, 'unknown fields');
  });

  test('recording.v1', () => {
    const base = {
      schema: 'trustagent.recording/v1', kind: 'recorded',
      label: 'Recorded 2026-10-12 · typesafe/jev-1.13-20260917 + claude-haiku-4-5 · scripted answers · replayed, not live',
      recorded_at: '2026-10-12T09:00:00Z', git_sha: 'abc1234', trustagent_version: '2.0.0',
      decision_model: { backend: 'openrouter-decisions', snapshot: 'typesafe/jev-1.13-20260917' },
      judge: { provider: 'anthropic', model: 'claude-haiku-4-5', fallback_active: false }, calibration_state: 'measured',
      answer_sources: { scripted: 1, generated: 0 }, totals: { traces: 1, decision_usd: 0.0002, llm_usd: 0.03 },
      roi: { window: '24h', gated_responses: 1, simulated: false },
      traces: [{ trace_id: TRACE, created_at: '2026-10-12T09:00:00Z', answer_source: 'scripted' }],
      events: [{ t_ms: 0, type: 'gate_decision', trace_id: TRACE, data: {} }],
    };
    assert.equal(check('recording.v1', base), null);
    assert.notEqual(check('recording.v1', { ...base, decision_model: { backend: 'mock', snapshot: 'mock/jev-heuristic-v1' } }), null);
    assert.notEqual(check('recording.v1', { ...base, judge: { ...base.judge, fallback_active: true } }), null);
    assert.notEqual(check('recording.v1', { ...base, traces: [{ ...base.traces[0], answer_source: null }] }), null);
    const mock = { ...base, kind: 'mock', label: 'Simulated (mock checks + mock judge): not model output', decision_model: { backend: 'mock', snapshot: 'mock/jev-heuristic-v1' } };
    assert.equal(check('recording.v1', mock), null);
    assert.notEqual(check('recording.v1', { ...mock, label: 'Recorded live' }), null);
  });

  test('benchmark-run.v1', () => {
    const metrics = { precision: 0.9, recall: 0.8, f1: 0.85, accuracy: 0.86, balanced_accuracy: 0.84, auroc: 0.92, ece: 0.04, brier: 0.1, ci95: { f1: [0.8, 0.9] } };
    const run = {
      run_id: 'bench-lite-20261012', created_at: '2026-10-12T09:00:00Z', git_sha: 'abc1234', tier: 'lite', measured: true, mock: false,
      dataset: { name: 'ragtruth', subset: 'qa', split: 'test', n: 200, revision: 'abc', license_note: 'MIT' },
      systems: [{
        id: 'gate', decision_model: { backend: 'openrouter-decisions', snapshot: 'typesafe/jev-1.13-20260917' }, judge: null, metrics,
        latency_ms: { p50: 231, p95: 512 }, cost: { usd_total: 0.04, usd_per_1k: 0.2, cost_source: 'reported' }, reliability: [],
      }],
      errors: { failed: 0, excluded_over_budget: 0 }, environment: { region: 'eu-west', concurrency: 4 },
    };
    assert.equal(check('benchmark-run.v1', run), null);
    assert.notEqual(check('benchmark-run.v1', { ...run, mock: true }), null);
    assert.notEqual(check('benchmark-run.v1', { ...run, systems: [{ ...run.systems[0], cost: { ...run.systems[0].cost, cost_source: 'simulated' } }] }), null);
  });

  test('scenarios.v1', () => {
    const scenario = {
      id: 'refund-contradiction', title: 'Refund window: 60 days vs 30 days', sets: ['s0', 'demo'], route: 'support-bot', stream: false,
      answer_source: 'scripted',
      request: {
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: 'Can I still get a refund on my annual plan? I bought it 45 days ago.' }],
        trustagent: { context: [{ id: 'refund-policy', text: 'Annual plans can be refunded within 30 days of purchase.' }] },
      },
      headers: { 'X-TrustAgent-Route': 'support-bot' },
      response: 'You can get a full refund within 60 days of purchase.',
      mock_answers: { contradiction__v1: { type: 'noul', noul: 0.97 }, groundedness__v1: { type: 'noul', noul: 0.96 } },
      judge: { claims: [{ claim: 'Refunds are available within 60 days of purchase.', verdict: 'CONTRADICTED', confidence: 0.95 }] },
      expect: {
        mode: 'flag', action: 'flag', would_action: 'block', status: 'ok', verdict: 'contradicted', max_risk: 0.97,
        reasons: ['contradiction:critical'], checked_against: 'extension:1', deep_audit: 'high', audit: 'queued',
        audit_verdict: 'HALLUCINATED', outcome: 'flagged',
      },
    };
    const doc = { schema: 'trustagent.scenarios/v1', scenarios: [scenario] };
    const withExpect = (x) => ({ ...doc, scenarios: [{ ...scenario, expect: { ...scenario.expect, ...x } }] });
    assert.equal(check('scenarios.v1', doc), null);
    const { response, ...noResponse } = scenario;
    assert.equal(check('scenarios.v1', { ...doc, scenarios: [{ ...noResponse, answer_source: 'generated' }] }), null);
    assert.notEqual(check('scenarios.v1', { ...doc, scenarios: [{ ...scenario, mock_answers: { 'groundedness@1': {} } }] }), null);
    assert.equal(check('scenarios.v1', withExpect({ checked_against: 'request_system:2,tool_results:1' })), null);
    assert.equal(check('scenarios.v1', withExpect({ checked_against: 'none', max_risk: null })), null);
    assert.notEqual(check('scenarios.v1', withExpect({ checked_against: 'system:1' })), null);
    assert.notEqual(check('scenarios.v1', withExpect({ outcome: 'held' })), null);
    assert.notEqual(check('scenarios.v1', withExpect({ deep_audit: 'low' })), null);
  });
});

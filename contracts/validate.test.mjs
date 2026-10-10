// Tests for validate.mjs. The first suite copies contracts/ into a temporary repo, breaks one thing
// and expects the validator to report it; the second checks that schemas without a committed instance
// accept a realistic document and reject broken ones. Run with `npm test` (node --test).

import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
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
const PENDING = join(HERE, '..', 'config', 'policies.pending.yaml');
const temps = [];
after(() => temps.forEach((d) => rmSync(d, { recursive: true, force: true })));

// A throwaway repo root holding a copy of contracts/ (without node_modules) and of config/policies.yaml and
// config/policies.pending.yaml, the policy the scenario checks resolve routes against.
function sandbox() {
  const repo = mkdtempSync(join(tmpdir(), 'contracts-'));
  temps.push(repo);
  const root = join(repo, 'contracts');
  cpSync(HERE, root, { recursive: true, filter: (src) => !src.includes('node_modules') });
  if (existsSync(POLICY)) {
    mkdirSync(join(repo, 'config'));
    cpSync(POLICY, join(repo, 'config', 'policies.yaml'));
    if (existsSync(PENDING)) cpSync(PENDING, join(repo, 'config', 'policies.pending.yaml'));
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
      k.keys.find((x) => x.name === 'TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY').runtime = 'settable';
    });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /TRUTHTABLE_PORT: default "eighty" is not an integer/);
    assert.match(r.out, /TRUTHTABLE_READ_TIMEOUT: default "30 seconds" is not a Go duration/);
    assert.match(r.out, /\.env\.example would list \d+ keys/);
    assert.match(r.out, /name or alias BIND_ADDR is declared twice/);
    assert.match(r.out, /TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY: a secret key cannot be runtime settable/);
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

  test('the faults pack follows the main pack rules, and only it holds faults (D119)', () => {
    const sb = sandbox();
    const main = sb.read('demo/scenarios.v1.json');
    const base = main.scenarios.find((s) => s.id === 'grounded-pass');
    const fault = (id, decision, extra = {}) => ({ ...structuredClone(base), id, sets: ['faults', 'p5'], mock_faults: { decision }, ...extra });
    const run5 = ['f1', 'f2', 'f3', 'f4', 'f5'].map((id) => fault(id, '429'));
    const noJudge = fault('f-no-judge', '529@1');
    delete noJudge.judge;
    const untagged = fault('f-untagged', '500');
    untagged.sets = ['p5'];
    sb.write('demo/faults.v1.json', { schema: 'trustagent.scenarios/v1', scenarios: [...run5, noJudge, untagged, fault('grounded-pass', '503')] });
    sb.edit('demo/scenarios.v1.json', (d) => {
      d.scenarios[1].sets.push('faults');
      d.scenarios[2].mock_faults = { upstream: '502' };
      delete d.scenarios.find((s) => s.id === 'help-center-link-block-json').judge;
    });
    let r = run(sb.root);
    assert.equal(r.code, 1, r.out);
    assert.match(r.out, /f5: more than 4 decision-model faults in a row would open the breaker/);
    assert.doesNotMatch(r.out, /f4: more than 4/);
    assert.match(r.out, /f-no-judge: a scenario names its judge/);
    assert.match(r.out, /f-untagged: every scenario of the faults pack carries the faults set/);
    assert.match(r.out, /grounded-pass: the id is also a scenario of demo\/scenarios\.v1\.json/);
    assert.match(r.out, /refund-contradiction-flag: the faults set belongs to demo\/faults\.v1\.json/);
    assert.match(r.out, /no-evidence-not-assessed: mock_faults belongs to demo\/faults\.v1\.json/);
    assert.match(r.out, /demo\/scenarios\.v1\.json: help-center-link-block-json: a scenario names its judge/);
    // The main pack's rules apply to the faults pack: a broken expectation there is reported too.
    const other = sandbox();
    const bad = fault('f-bad-mode', '429');
    bad.expect.mode = 'observe';
    other.write('demo/faults.v1.json', { schema: 'trustagent.scenarios/v1', scenarios: [bad] });
    r = run(other.root);
    assert.equal(r.code, 1, r.out);
    assert.match(r.out, /demo\/faults\.v1\.json: f-bad-mode: action pass is impossible in observe mode/);
    // A bad fault name pattern is a schema error.
    const third = sandbox();
    third.write('demo/faults.v1.json', { schema: 'trustagent.scenarios/v1', scenarios: [fault('f-pattern', 'Hang@x')] });
    assert.match(run(third.root).out, /demo\/faults\.v1\.json: \/scenarios\/0\/mock_faults\/decision must match pattern/);
    // The pack may be empty, but it must exist.
    const fourth = sandbox();
    rmSync(join(fourth.root, 'demo', 'faults.v1.json'));
    assert.match(run(fourth.root).out, /demo\/faults\.v1\.json is missing/);
  });

  test('per-unit claims divide their per-1,000 figure; compare cells are sourced (D113, D114)', () => {
    const sb = sandbox();
    sb.edit('claims/value-claims.v1.json', (c) => {
      c.claims.find((x) => x.id === 'fast_check_cost_per_check').value = 0.0003;
      c.claims.find((x) => x.id === 'studio_draft_cost_estimate').per_unit_of = 'no_such_figure';
      // D168: the cost of one call is its tokens at the price claim, to two significant digits.
      c.claims.find((x) => x.id === 'hero_card_cost_usd').value = 0.0002;
      c.claims.find((x) => x.id === 'llm_full_audit_cost_per_1k').tokens_x_price = { input_tokens: 10, price_claim: 'hero_card_latency_ms' };
    });
    sb.edit('claims/compare.v1.json', (c) => {
      c.rows[0].cells.inline = { value: 'yes', vendor: 'Langfuse', text: 'Runs as a hosted service.', source_url: null, as_of: null, reviewed: false };
      c.rows[1].cells.self_hosted = { value: 'partial', vendor: 'Guardrails AI', text: '', source_url: 'https://example.com/docs', as_of: '2026-10-09', reviewed: false };
      delete c.rows[2].cells.explanations;
      c.rows[3].cells.extra = { value: 'unverified', vendor: null, text: '', source_url: null, as_of: null, reviewed: false };
    });
    const r = run(sb.root);
    assert.equal(r.code, 1, r.out);
    assert.match(r.out, /fast_check_cost_per_check: value 0\.0003 must be fast_check_cost_per_1k \/ 1000/);
    assert.match(r.out, /studio_draft_cost_estimate: per_unit_of no_such_figure is neither a usd_per_1k claim nor a figure/);
    assert.match(r.out, /hero_card_cost_usd: value 0\.0002 must be 565 tokens × jev_input_price_per_mtok \(0\.042 per million\) = 0\.000024/);
    assert.match(r.out, /llm_full_audit_cost_per_1k: tokens_x_price needs unit usd/);
    assert.match(r.out, /llm_full_audit_cost_per_1k: tokens_x_price\.price_claim hero_card_latency_ms is not a usd_per_mtok claim with a value/);
    assert.match(r.out, /observability_judge\.inline: a yes cell needs the source_url it was read from and as_of/);
    assert.match(r.out, /guardrails\.self_hosted: a partial cell says what the vendor documents \(text\)/);
    assert.match(r.out, /cloud_groundedness: no cell for column explanations/);
    assert.match(r.out, /small_model_evaluators: cell extra is not a column/);
    // A row's examples name only the products its cells cite, and each cell names its vendor (D113).
    const vend = sandbox();
    vend.edit('claims/compare.v1.json', (c) => {
      c.rows[0].examples = `${c.rows[0].examples}, Datadog`;
      c.rows[1].cells.inline.vendor = 'Someone Else';
      c.rows[2].cells.cost_class.vendor = null;
      c.rows[3].cells.explanations.text = 'Not documented (as of 2026-10-10): scores only.';
      // The vendor is shown beside the text, so the text never names it (D169).
      c.rows[3].cells.cost_class.text = `${c.rows[3].cells.cost_class.vendor} says it is cheap.`;
    });
    const v = run(vend.root);
    assert.equal(v.code, 1, v.out);
    assert.match(v.out, /observability_judge: example Datadog is cited by no cell/);
    assert.match(v.out, /guardrails\.inline: vendor Someone Else is not in the row's examples/);
    assert.match(v.out, /cloud_groundedness\.cost_class: a varies cell names the vendor whose docs it cites \(vendor\)/);
    assert.match(v.out, /small_model_evaluators\.explanations: the text says what the docs say; "Not documented" is the value not_documented/);
    assert.match(v.out, /small_model_evaluators\.cost_class: the text names the vendor Galileo, which is shown beside it/);
    // reviewed is a boolean (schema).
    const other = sandbox();
    other.edit('claims/compare.v1.json', (c) => {
      c.rows[0].cells.cost_class.reviewed = 'no';
      c.rows[3].cells.inline.value = 'maybe';
    });
    const o = run(other.root).out;
    assert.match(o, /claims\/compare\.v1\.json: \/rows\/0\/cells\/cost_class\/reviewed must be boolean/);
    assert.match(o, /\/rows\/3\/cells\/inline\/value must be equal to one of the allowed values/);
  });

  test('vendor, prompt and load fixtures keep their shapes (D108, D118, D122)', () => {
    const sb = sandbox();
    const ok = sb.read('fixtures/vendor/openrouter/decisions.401.json');
    sb.write('fixtures/vendor/openrouter/decisions.captured.json', { ...ok, source: 'from my laptop' });
    // A key that is not an obvious fake, built at run time so no key-shaped literal sits in the
    // source for the secret scanner (security.yml gitleaks) to report.
    sb.write('fixtures/vendor/openrouter/key.leak.json', { ...ok, body: { key: ['sk-or-v1', '0123456789abcdef0123'].join('-') } });
    sb.write('fixtures/vendor/openrouter/chat.headers.json', { ...ok, headers: { 'Content-Type': 'application/json' } });
    sb.write('fixtures/vendor/openrouter/Bad-Name.json', ok);
    sb.edit('fixtures/prompts/drafter-render.basic.json', (f) => { f.prompt = 'no-such-prompt.v1'; });
    sb.edit('fixtures/load/load-run.example.json', (f) => { f.scenarios[0].n = 10; f.dirty = true; });
    const published = { ...sb.read('fixtures/load/load-run.example.json'), vendors: 'openrouter' };
    sb.write('docs/benchmarks/load/run-1.json', published, sb.repo);
    const r = run(sb.root);
    assert.equal(r.code, 1, r.out);
    assert.match(r.out, /decisions\.captured\.json: source starts with "synthetic: "/);
    assert.match(r.out, /key\.leak\.json: sk-or-v1-012… is not an obvious fake key/);
    assert.match(r.out, /chat\.headers\.json: headers is an object of lowercase names/);
    assert.match(r.out, /Bad-Name\.json: (a vendor fixture is named|no schema mapping)/);
    assert.match(r.out, /drafter-render\.basic\.json: prompt no-such-prompt\.v1 is not in prompts\//);
    assert.match(r.out, /load-run\.example\.json: \/scenarios\/0\/n must be >= 500/);
    assert.match(r.out, /load-run\.example\.json: \/dirty must be equal to constant/);
    assert.match(r.out, /\.\.\/docs\/benchmarks\/load\/run-1\.json: \/vendors must be equal to constant/);
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
    assert.match(r.out, /refund-contradiction-flag: mock_answers\.refusal__v1 is not asked on route support-bot \(config\/policies\.yaml( \+ config\/policies\.pending\.yaml)?\)/);
    assert.match(r.out, /refund-contradiction-flag: mock_answers\.groundedness__v1 is a choice answer to a noul question/);
    assert.match(r.out, /no-evidence-not-assessed: the request lands on route default in config\/policies\.yaml( \+ config\/policies\.pending\.yaml)?, not support-bot/);

    // A route that also asks an evidence-free evaluator always sends it, so it cannot be not_assessed.
    const other = sandbox();
    const yaml = readFileSync(POLICY, 'utf8').replace('      - ref: contradiction@1\n', '      - ref: contradiction@1\n      - ref: refusal@1\n');
    other.write('config/policies.yaml', yaml, other.repo);
    r = run(other.root);
    assert.equal(r.code, 1, r.out);
    assert.match(r.out, /grounded-pass: refusal@1 needs no evidence, so it is always asked on route support-bot, but mock_answers has no refusal__v1/);
    assert.match(r.out, /no-evidence-not-assessed: status not_assessed is impossible on route support-bot: refusal@1 needs no evidence/);
  });

  test('pending routes merge over config/policies.yaml, and their scenarios carry a phase tag', { skip: !existsSync(PENDING) }, () => {
    const sb = sandbox();
    const pending = readFileSync(PENDING, 'utf8')
      .replace('version: 1\n', 'version: 1\noverrides: {client_mode: none}\n')
      .concat('  - {name: support-bot, match: {headers: {X-TrustAgent-Route: support-bot-2}}}\n');
    sb.write('config/policies.pending.yaml', pending, sb.repo);
    sb.edit('demo/scenarios.v1.json', (d) => {
      const s = d.scenarios.find((x) => x.id === 'knowledge-only-capped');
      s.sets = s.sets.filter((t) => t !== 'p3');
    });
    const r = run(sb.root);
    assert.equal(r.code, 1, r.out);
    assert.match(r.out, /policies\.pending\.yaml: only version and routes belong here .*: overrides/);
    assert.match(r.out, /policies\.pending\.yaml: duplicate route name .*"support-bot"/);
    assert.match(r.out, /knowledge-only-capped: route handbook is in config\/policies\.pending\.yaml, which the gateway cannot load yet, so the scenario carries a phase tag \(p3\)/);
  });

  test('fallback answers, delivered text, streams, knowledge and tags follow the route', { skip: !existsSync(PENDING) }, () => {
    const sb = sandbox();
    sb.edit('demo/scenarios.v1.json', (d) => {
      const byId = (id) => d.scenarios.find((x) => x.id === id);
      byId('payments-fallback').expect.delivered_text = 'Something else.';
      byId('payments-fallback').fallback.mock_answers.refusal__v1 = { type: 'choice', choice: 'answers_request' };
      byId('help-center-link-block').expect.delivered_text = byId('help-center-link-block').response;
      byId('refund-contradiction-flag').fallback = { response: 'x', mock_answers: {}, expect: { would_action: 'pass' } };
      delete byId('knowledge-only-capped').knowledge;
      byId('tool-results-grounded').expect.stream_decision = { comment: true, trailers: [] };
      byId('generated-monthly-refund').sets = ['demo'];
      byId('grounded-pass').sets.push('p3');
    });
    const r = run(sb.root);
    assert.equal(r.code, 1, r.out);
    assert.match(r.out, /payments-fallback: the client receives "Approved refunds go back to your original payment method within 5 to 10 business days\." \(action fallback on route payments-faq\), so expect\.delivered_text must say so, not "Something else\."/);
    assert.match(r.out, /payments-fallback: fallback\.mock_answers\.refusal__v1 is not asked on route payments-faq/);
    assert.match(r.out, /help-center-link-block: the client receives "I can't verify that answer against our help articles, so I've held it back\."/);
    assert.match(r.out, /help-center-link-block: expect\.delivered_text equals response; leave it out/);
    assert.match(r.out, /refund-contradiction-flag: fallback needs a route in fallback mode, and support-bot runs in flag mode/);
    assert.match(r.out, /knowledge-only-capped: route handbook reads the knowledge add-on, so the scenario lists the documents it returns/);
    assert.match(r.out, /tool-results-grounded: expect\.stream_decision describes a stream, but the scenario does not stream/);
    assert.match(r.out, /generated-monthly-refund: the generated set holds exactly the scenarios with answer_source generated/);
    assert.match(r.out, /grounded-pass: an s0 scenario runs on the walking skeleton and carries no phase tag/);
  });

  test('a fallback answer whose re-gate blocks applies on_fail to the action and the delivered text', { skip: !existsSync(PENDING) }, () => {
    const sb = sandbox();
    sb.edit('demo/scenarios.v1.json', (d) => {
      const s = d.scenarios.find((x) => x.id === 'payments-fallback');
      s.fallback.expect.would_action = 'block';
    });
    let r = run(sb.root);
    assert.equal(r.code, 1, r.out);
    // payments-faq has on_fail block: the client gets the block body with action block.
    assert.match(r.out, /payments-fallback: the re-gate of the fallback answer would block and on_fail is block, so expect\.action is block, not fallback/);
    assert.match(r.out, /payments-fallback: the client receives "I can't confirm that from our policy documents\. A person will follow up\."/);

    const ok = sandbox();
    ok.edit('demo/scenarios.v1.json', (d) => {
      const s = d.scenarios.find((x) => x.id === 'payments-fallback');
      s.fallback.expect.would_action = 'block';
      s.expect.action = 'block';
      s.expect.outcome = 'blocked';
      s.expect.delivered_text = "I can't confirm that from our policy documents. A person will follow up.";
    });
    r = run(ok.root);
    assert.doesNotMatch(r.out, /payments-fallback/, r.out);
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

  test('lint vectors must agree with the SPEC §2.8 rules, and built-ins must be lint-clean', () => {
    const sb = sandbox();
    sb.edit('vectors/lint/counting-or-arithmetic-instructions.json', (v) => { v.expected.lints = []; });
    sb.edit('vectors/lint/no-id-reference.json', (v) => { v.expected.errors = []; });
    sb.edit('evaluators/refusal.v1.json', (e) => { e.question.instructions = 'Does `assistant_response` decline at least one request?'; });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /counting-or-arithmetic-instructions\.json: expected\.lints is \[\], but the SPEC §2\.8 rules give \["counting_or_arithmetic"\]/);
    assert.match(r.out, /no-id-reference\.json: expected\.errors is \[\], but the SPEC §2\.8 rules give \["no_id_reference"\]/);
    assert.match(r.out, /evaluators\/refusal\.v1\.json: counting_or_arithmetic: .*\(built-in templates must be lint-clean\)/);
  });

  test('route lint vectors (input.attached) must agree with the D98 corroboration rule', () => {
    const sb = sandbox();
    // Attach the one corroborator: the vector still says it warns, which the rule no longer gives.
    sb.edit('vectors/lint/corroboration-with-none-attached-warns.json', (v) => { v.input.attached = ['groundedness']; });
    sb.edit('vectors/lint/implied-not-attached-warns.json', (v) => { v.input.attached = 'contradiction'; });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /corroboration-with-none-attached-warns\.json: expected\.lints is \["reference_not_attached"\], but the SPEC §2\.8 rules give \[\]/);
    assert.match(r.out, /implied-not-attached-warns\.json: input\.attached must be a list of evaluator ids/);
  });

  test('drafter examples are drafts the drafter rules accept', () => {
    const sb = sandbox();
    sb.edit('prompts/evaluator-drafter.v1.json', (p) => {
      const [competitors, delivery, secrets] = p.examples.map((e) => e.output);
      competitors.question.instructions += ' Answer how many companies it names.';
      competitors.outcomes.requires_corroboration = ['brand_safety'];
      delivery.question.criteria.true += ' in `order_records`';
      delivery.test_samples[0].evidence = ['Shipped.'];
      secrets.id = 'asks_for_secrets';
    });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    const at = (i) => `prompts/evaluator-drafter\\.v1\\.json \\(examples\\[${i}\\]\\.output\\)`;
    assert.match(r.out, new RegExp(`${at(0)}: counting_or_arithmetic: .*drafter examples must be lint-clean`));
    assert.match(r.out, new RegExp(`${at(0)}: references evaluator "brand_safety", which is not in the registry`));
    assert.match(r.out, new RegExp(`${at(0)}: names evaluator brand_safety, which the example's existing_ids does not offer`));
    assert.match(r.out, new RegExp(`${at(1)}: unresolved_state_reference: \`order_records\``));
    assert.match(r.out, new RegExp(`${at(1)}: test_samples\\[0\\]: evidence is required, and 8 characters are fewer than min_chars 40`));
    assert.match(r.out, new RegExp(`${at(2)}: / must NOT have additional properties .*"additionalProperty":"id"`));
  });

  test('drafter examples: sample split and sample fields', () => {
    const sb = sandbox();
    sb.edit('prompts/evaluator-drafter.v1.json', (p) => {
      const secrets = p.examples[2].output;
      secrets.test_samples[0].should_fire = false;
      secrets.test_samples[1].user_request = 'I forgot my password.';
      secrets.test_samples[2].evidence = ['Password resets are done from the sign-in page only.'];
    });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    const at = 'prompts/evaluator-drafter\\.v1\\.json \\(examples\\[2\\]\\.output\\)';
    assert.match(r.out, new RegExp(`${at}: test_samples: 2 of 6 should fire, want 3`));
    assert.match(r.out, new RegExp(`${at}: test_samples\\[1\\]: give user_request exactly when the question reads it`));
    assert.match(r.out, new RegExp(`${at}: test_samples\\[2\\]: the question does not read evidence`));
  });

  test('prompt placeholders, variables, file name and token budget are checked', () => {
    const sb = sandbox();
    sb.edit('prompts/evaluator-drafter.v1.json', (p) => {
      p.user.push('<tone>{{tone}}</tone>');
      p.variables.unused = { description: 'Never rendered.' };
      for (let i = 0; i < 6; i++) p.system.push('x'.repeat(1500));
      p.system[0] = p.system[0].replace('TrustAgent', 'TrustAgent {{ oops');
      p.system = p.system.map((l) => l.replace(', det:system_prompt_leak', '').replace('`system_instructions`', 'system instructions'));
    });
    const copy = sb.read('prompts/evaluator-drafter.v1.json');
    sb.write('prompts/other-drafter.v1.json', copy);
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /evaluator-drafter\.v1\.json: user: \{\{tone\}\} is not a declared variable/);
    assert.match(r.out, /evaluator-drafter\.v1\.json: examples\[0\]\.input has no tone/);
    assert.match(r.out, /evaluator-drafter\.v1\.json: variables\.unused is not used by system, user or repair/);
    assert.match(r.out, /evaluator-drafter\.v1\.json: system: "\{\{" that does not start a \{\{name\}\} placeholder/);
    assert.match(r.out, /evaluator-drafter\.v1\.json: the first call needs about [0-9]+ tokens .* above the static playground cap of 8000/);
    assert.match(r.out, /evaluator-drafter\.v1\.json: system does not list the detector det:system_prompt_leak/);
    assert.match(r.out, /evaluator-drafter\.v1\.json: system does not describe the state field `system_instructions`/);
    assert.match(r.out, /other-drafter\.v1\.json: file name must be evaluator-drafter\.v1\.json/);
  });

  test('block and fallback presets must be ready to enforce once calibrated', () => {
    const sb = sandbox();
    // A block-eligible template whose only corroborator is another evaluator.
    sb.write('evaluators/brand_claims.v1.json', {
      ...sb.read('evaluators/refusal.v1.json'),
      id: 'brand_claims', name: 'Brand claims',
      outcomes: { warn_at: 0.7, critical_at: 0.9, block_eligible: true, requires_corroboration: ['contradiction'], implies: [] },
    });
    sb.edit('policies/presets.v1.json', (p) => {
      const byId = Object.fromEntries(p.presets.map((x) => [x.id, x]));
      byId['block-contradictions'].settings.streaming = 'passthrough';
      delete byId['block-contradictions'].ui_note;
      byId['replace-unverified'].settings.evaluators = [{ ref: 'brand_claims@1' }, { ref: 'contradiction@1', stage: 'shadow' }];
      byId['replace-unverified'].settings.detectors = { block_on: [] };
      byId['flag-risky'].settings = { mode: 'block', streaming: 'buffer', require_calibration: false, evaluators: [{ ref: 'refusal@1' }] };
      byId['observe-all'].name = byId['support-bot'].name;
    });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /preset block-contradictions: block mode needs streaming: buffer/);
    assert.match(r.out, /preset block-contradictions: with require_calibration it acts as Flag .* ui_note must say so/);
    assert.match(r.out, /preset replace-unverified: brand_claims@1 can block only when one of contradiction agrees, and the preset attaches none of them/);
    assert.match(r.out, /preset flag-risky: block mode, but no active evaluator is block-eligible and detectors\.block_on is empty/);
    assert.match(r.out, /duplicate preset name "Support bot"/);
    assert.doesNotMatch(r.out, /preset flag-risky: .*ui_note/); // require_calibration: false needs no note
  });

  test('detector vector findings select their text; evidence tag patterns are portable', () => {
    const sb = sandbox();
    sb.edit('vectors/detectors/numbers-utf16-offsets-after-emoji.json', (v) => {
      v.expected.findings[0].start -= 2; // two code units early
    });
    sb.edit('vectors/detectors/quotes-misquoted-policy.json', (v) => { v.expected.findings[0].end = 999; });
    sb.edit('vectors/evidence/route-tag-patterns-and-crlf.json', (v) => { v.input.settings.tag_patterns[0].flags = 'g'; });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /numbers-utf16-offsets-after-emoji\.json: expected\.findings\[0\]: \[31, 34\) selects "n 7", not "7"/);
    assert.match(r.out, /quotes-misquoted-policy\.json: expected\.findings\[0\]: \[17, 999\) is not a span/);
    assert.match(r.out, /route-tag-patterns-and-crlf\.json: input\.settings\.tag_patterns\[0\]: flags "g" must be a subset of "ims"/);
  });

  test('API fixtures are named after their api.v1 type and cover the core responses and events', () => {
    const sb = sandbox();
    const api = (name) => join(sb.root, 'fixtures', 'api', name);
    renameSync(api('HealthStatus.ok.json'), api('health.json'));
    renameSync(api('WsEvent.gap.json'), api('WsEvent.hb.slow-consumer.json'));
    rmSync(api('WsEvent.audit_dropped.json'));
    sb.write('fixtures/api/NoSuchType.example.json', {});
    sb.edit('fixtures/api/Page-TraceSummary.recent.json', (p) => { p.items[0].gate_status = 'thinking'; });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /fixtures\/api\/health\.json: an API fixture is named <Type>\.<case>\.json/);
    assert.match(r.out, /fixtures\/api: no fixture for HealthStatus \(HealthStatus\.<case>\.json\)/);
    assert.match(r.out, /WsEvent\.hb\.slow-consumer\.json: a WsEvent fixture is named WsEvent\.gap\[\.<variant>\]\.json/);
    assert.match(r.out, /fixtures\/api: no fixture for the WebSocket event audit_dropped \(WsEvent\.audit_dropped\.json\)/);
    assert.match(r.out, /NoSuchType\.example\.json: api\.v1\.schema\.json has no definition NoSuchType/);
    assert.match(r.out, /Page-TraceSummary\.recent\.json: \/items\/0\/gate_status .*\[api\.v1#\/\$defs\/Page<TraceSummary>\]/);
  });

  test('API fixtures keep trace ids, decision.v1 and UTF-16 spans consistent', () => {
    const sb = sandbox();
    sb.edit('fixtures/api/TraceDetail.refund-contradiction-flag.json', (t) => {
      t.audit_detail.claims[0].span.start -= 3;
      t.audit_detail.claims[0].evidence_spans[0].end += 1;
      t.decisions[0].detectors[0].findings[0].end += 1;
      t.decisions[0].reasons.push('made_up_reason');
      t.audit_detail.trace_id = t.decisions[0].trace_id.replace(/.$/, '0');
    });
    sb.edit('fixtures/api/AuditDetail.direct-astral-utf16-spans.json', (a) => {
      // Code-point offsets instead of UTF-16 ones: the two-unit emoji is counted once.
      a.claims[0].span = { ...a.claims[0].span, start: a.claims[0].span.start - 1, end: a.claims[0].span.end - 1 };
      a.claims[1].evidence_spans[0].doc_id = 'another-doc';
    });
    sb.edit('fixtures/api/WsEvent.audit_completed.json', (e) => { e.data.audit.trace_id = e.data.audit.audit_id; });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    const t = /TraceDetail\.refund-contradiction-flag\.json/.source;
    assert.match(r.out, new RegExp(`${t}: audit_detail\\.claims\\[0\\]\\.span: \\[30, 40\\) selects "in 60 days", not "60 days"`));
    assert.match(r.out, new RegExp(`${t}: audit_detail\\.claims\\[0\\]\\.evidence_spans\\[0\\]: \\[0, 57\\) selects`));
    assert.match(r.out, new RegExp(`${t}: decisions\\[0\\]\\.detectors\\[numbers_not_in_evidence\\]\\.findings\\[0\\]: \\[33, 36\\) selects "60 "`));
    assert.match(r.out, new RegExp(`${t} \\(decisions\\[0\\]\\): /reasons/3 must match pattern .*\\[decision\\.v1\\]`));
    assert.match(r.out, new RegExp(`${t}: audit_detail\\.trace_id is not the trace's`));
    assert.match(r.out, /AuditDetail\.direct-astral-utf16-spans\.json: claims\[0\]\.span: \[7, 24\) selects " We ship to Canad", not "We ship to Canada"/);
    assert.match(r.out, /claims\[1\]\.evidence_spans\[0\]: doc_id another-doc is not context_used\[0\]\.id shipping\.md/);
    assert.match(r.out, /WsEvent\.audit_completed\.json: data\.audit\.trace_id is not the envelope trace_id/);
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

  test('bench publish rules hold for published runs, measured.json and the fixtures (D203, D204, D212)', () => {
    const sb = sandbox();
    // A schema-valid published run on two snapshots is refused by the publish rules.
    sb.write('docs/benchmarks/results/refused-snapshots.json', sb.read('fixtures/bench/run.refused.mixed_snapshot.json'), sb.repo);
    // A refusal fixture that breaks nothing, and a publishable one that breaks a rule.
    sb.write('fixtures/bench/run.refused.dirty.json', sb.read('fixtures/bench/run.publishable.json'));
    sb.edit('fixtures/bench/measured.publishable.json', (m) => { delete m.sources.attempts; });
    // A measured_pair that is not a measured claim, and one placed on a measured claim.
    sb.edit('claims/value-claims.v1.json', (c) => {
      c.claims.find((x) => x.id === 'same_checks_cost_multiple').measured_pair = 'fast_check_cost_per_1k';
      c.claims.find((x) => x.id === 'gate_latency_p50_measured').measured_pair = 'gate_latency_p95_measured';
    });
    // A measured.json that says measured without its snapshot, attempts or cost sources.
    sb.edit('claims/measured.json', (m) => {
      m.status = 'measured'; m.tier = 'lite'; m.generated_by = 'evals.bench publish'; m.generated_at = '2026-10-12T10:00:00Z';
      m.runs = ['r1'];
      m.values.fast_check_usd_per_1k.value = 0.02;
      m.values.fast_check_usd_per_1k.run_id = 'r1';
    });
    const r = run(sb.root);
    assert.equal(r.code, 1);
    assert.match(r.out, /refused-snapshots\.json: bench publish refuses this run: mixed_snapshot/);
    assert.match(r.out, /run\.refused\.dirty\.json: the fixture must break rule dirty, but breaks none/);
    assert.match(r.out, /measured\.publishable\.json: a publishable fixture breaks a rule: attempts/);
    assert.match(r.out, /same_checks_cost_multiple: measured_pair fast_check_cost_per_1k is a estimate claim, not measured/);
    assert.match(r.out, /gate_latency_p50_measured: a measured claim is the target of measured_pair, never its source/);
    assert.match(r.out, /claims\/measured\.json: a measured file names the decision-model snapshot/);
    assert.match(r.out, /claims\/measured\.json: sources\.attempts discloses every bench run/);
    assert.match(r.out, /claims\/measured\.json: values\.fast_check_usd_per_1k needs sources\.fast_check_usd_per_1k\.cost_source/);
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
    // D203: bare hosts; a recorded tape names vendor hosts only. D207-D209: featured and calibration.
    const hosts = {
      ...base,
      decision_model: { ...base.decision_model, host: 'openrouter.ai' },
      judge: { ...base.judge, host: 'openrouter.ai' },
      answer_sources: { scripted: 1, generated: 0, generated_host: null },
      featured: { trace_id: TRACE, rule: 'contradicted_with_span' },
      calibration: { snapshot: 'typesafe/jev-1.13-20260917', quality: 'measured' },
    };
    assert.equal(check('recording.v1', hosts), null);
    assert.notEqual(check('recording.v1', { ...hosts, decision_model: { ...hosts.decision_model, host: 'mock-upstream' } }), null, 'recorded needs a vendor host');
    assert.notEqual(check('recording.v1', { ...hosts, judge: { ...hosts.judge, host: 'https://openrouter.ai/api' } }), null, 'bare hosts only');
    assert.notEqual(check('recording.v1', { ...hosts, answer_sources: { ...hosts.answer_sources, generated_host: 'mock-upstream' } }), null);
    assert.notEqual(check('recording.v1', { ...hosts, featured: { ...hosts.featured, rule: 'scripted' } }), null, 'scripted is the simulated tape only');
    assert.equal(check('recording.v1', { ...mock, decision_model: { ...mock.decision_model, host: 'mock-upstream' }, featured: { trace_id: TRACE, rule: 'scripted' } }), null);
    assert.equal(check('recording.v1', { ...hosts, calibration: { snapshot: null, quality: 'none' } }), null);
    assert.notEqual(check('recording.v1', { ...hosts, calibration: { snapshot: null, quality: 'proxy' } }), null);
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
    // D203-D205: hosts, inputs, quality, dirty and latency conditions.
    const p6 = JSON.parse(readFileSync(join(HERE, 'fixtures', 'bench', 'run.publishable.json'), 'utf8'));
    assert.equal(check('benchmark-run.v1', p6), null);
    assert.notEqual(check('benchmark-run.v1', { ...p6, vendor: { ...p6.vendor, decision_host: 'mock-upstream' } }), null);
    assert.notEqual(check('benchmark-run.v1', { ...p6, dirty: true }), null);
    assert.notEqual(check('benchmark-run.v1', { ...p6, quality: { ...p6.quality, failure_rate: 0.06 } }), null);
    assert.notEqual(check('benchmark-run.v1', { ...p6, latency: { connection: 'hot', gate_concurrency: 1 } }), null);
    assert.notEqual(check('benchmark-run.v1', { ...p6, inputs: { ...p6.inputs, host_env: { ...p6.inputs.host_env, loadavg: [1] } } }), null);
  });

  test('measured.v1', () => {
    const m = JSON.parse(readFileSync(join(HERE, 'fixtures', 'bench', 'measured.publishable.json'), 'utf8'));
    assert.equal(check('measured.v1', m), null);
    assert.equal(check('measured.v1', { ...m, snapshot: ['typesafe/jev-1.13-20260917', 'typesafe/jev-1.14-20261101'] }), null);
    assert.notEqual(check('measured.v1', { ...m, sources: { ...m.sources, fast_check_usd_per_1k: { cost_source: 'unknown' } } }), null);
    assert.notEqual(check('measured.v1', { ...m, sources: { ...m.sources, groundedness_ece: { ...m.sources.groundedness_ece, method: 'in_sample' } } }), null);
    assert.notEqual(check('measured.v1', { ...m, intervals: { ragtruth_qa_accuracy: { lo: 0.7, hi: 0.9, method: 'normal' } } }), null);
    assert.notEqual(check('measured.v1', { ...m, mock: true }), null);
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
    // Delivery: the delivered text, the HTTP status and how a stream carries the decision.
    assert.equal(check('scenarios.v1', withExpect({ delivered_text: 'Held back.', http_status: 422 })), null);
    assert.equal(check('scenarios.v1', withExpect({ stream_decision: { comment: true, trailers: ['X-TrustAgent-Mode', 'X-TrustAgent-Action'] } })), null);
    assert.notEqual(check('scenarios.v1', withExpect({ stream_decision: { comment: true, trailers: ['Trailer-Mode'] } })), null);
    assert.notEqual(check('scenarios.v1', withExpect({ stream_decision: { comment: true } })), null);
    assert.notEqual(check('scenarios.v1', withExpect({ http_status: 100 })), null);
    // A fallback answer with its re-gate, and knowledge documents.
    const fallback = { response: 'Refunds take 5 to 10 business days.', mock_answers: { groundedness__v1: { type: 'noul', noul: 0.04 } }, expect: { would_action: 'pass', reasons: [] } };
    assert.equal(check('scenarios.v1', { ...doc, scenarios: [{ ...scenario, fallback }] }), null);
    assert.notEqual(check('scenarios.v1', { ...doc, scenarios: [{ ...scenario, fallback: { ...fallback, expect: {} } }] }), null);
    assert.notEqual(check('scenarios.v1', { ...doc, scenarios: [{ ...scenario, fallback: { response: 'x', mock_answers: {} } }] }), null);
    assert.equal(check('scenarios.v1', { ...doc, scenarios: [{ ...scenario, knowledge: [{ id: 'kb-leave', text: 'New employees get 20 days.' }] }] }), null);
    assert.notEqual(check('scenarios.v1', { ...doc, scenarios: [{ ...scenario, knowledge: [{ text: 'no id' }] }] }), null);
    assert.notEqual(check('scenarios.v1', { ...doc, scenarios: [{ ...scenario, knowledge: [] }] }), null);
  });
});

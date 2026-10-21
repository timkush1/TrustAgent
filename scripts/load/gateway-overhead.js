// k6: the time the TrustAgent gateway adds to an answer, per scenario (docs/GATEWAY.md §13,
// docs/v2/design-notes/design-gateway.md §14).
//
// The overhead of each answer is its X-TrustAgent-Overhead-Ms header: the whole request minus the
// upstream's own time. Observe routes decide after the answer is sent and have no such header;
// there the gateway's own trustagent_gateway_overhead_seconds{route} histogram (read from /metrics
// before and after the run) gives the figure, and, when /metrics cannot be read, the total request
// time stands in (an upper bound: mock-upstream answers at once, and it also holds the network
// hops). Every vendor is mock-upstream (docker-compose.load.yml), so nothing is spent.
//
//   scenario  route   what                                          target (overhead)
//   a         load-a  gate disabled: nothing to ask                 p95 < 3 ms
//   b         load-b  observe: Tier 0 + asynchronous mock Jev       p95 < 5 ms
//   c         load-c  flag, mock Jev answering in 0 ms              p95 < 10 ms
//   d         load-d  flag, mock Jev answering in 150 ms            p95 < 175 ms   (slow-Jev pass)
//   e         load-e  block with buffered streams                   reported only
//   f         load-f  15% of Jev calls hang, hedge at 400 ms        p99 <= 1500 ms (the deadline), no 5xx
//   g         load-g  block, JSON answers (not streamed)            p95 < 10 ms
//
// `make load-test-gate` runs each scenario as its own k6 run (a, b, c, e, f and g against
// mock-upstream, then d after pointing the gateway at mock-jev-slow), so the counters read before
// and after a run belong to that scenario alone. By hand, with the load stack up (`make load-up`;
// it publishes no port), inside the proxy container's network namespace:
//
//   API_KEY="$(python3 scripts/envfile.py get TRAFFICGEN_API_KEY)" \
//   docker run --rm --network "container:$(docker compose -p truthtable-load ps -q proxy)" \
//     -v "$PWD/scripts/load:/scripts/load:ro" -e BASE_URL=http://127.0.0.1:8080 -e API_KEY -e SCENARIOS=c \
//     grafana/k6 run /scripts/load/gateway-overhead.js
//
// Environment: BASE_URL (http://localhost:8080), API_KEY (the proxy key, X-TrustAgent-Key),
// SCENARIOS (a,b,c,e,f,g), RATE (requests per second per scenario, 20), DURATION (per scenario,
// 30s), HANG_RATE (share of scenario f's requests whose fast check hangs, 0.15), METRICS_URL (the
// proxy's /metrics, http://127.0.0.1:8002/metrics), JEV_MOCK_URL (the mock the fast check calls,
// http://mock-upstream:8090), SUMMARY_PATH (a JSON file for the k6 summary, none by default).
//
// The end of the run prints one line `TRUSTAGENT_LOAD_RESULT {json}`: every scenario's figures,
// the fast-check client's work during the run (its vendor attempts and hedges from /metrics, the
// decision requests the mock recorded) and Tier 0's time under load. scripts/load/report.py reads
// these lines.

import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { Counter, Gauge, Rate, Trend } from 'k6/metrics';

import { buckets, decisionRequests, histogramStats, parseMetrics, sumOf, target } from './overhead-lib.js';

const BASE_URL = (__ENV.BASE_URL || 'http://localhost:8080').replace(/\/+$/, '');
const API_KEY = __ENV.API_KEY || '';
const RATE = Number(__ENV.RATE || 20);
const DURATION = __ENV.DURATION || '30s';
const HANG_RATE = Number(__ENV.HANG_RATE || 0.15);
const METRICS_URL = __ENV.METRICS_URL || 'http://127.0.0.1:8002/metrics';
const JEV_MOCK_URL = (__ENV.JEV_MOCK_URL || 'http://mock-upstream:8090').replace(/\/+$/, '');
const GAP_S = 5; // quiet time between scenarios of one run, so one's tail does not overlap the next

// The line scripts/load/report.py reads.
export const RESULT_PREFIX = 'TRUSTAGENT_LOAD_RESULT ';

export const SCENARIOS = {
  a: { route: 'load-a', stream: false, mode: 'disabled', what: 'gate disabled (nothing to ask)', threshold: 'p(95)<3' },
  b: { route: 'load-b', stream: false, mode: 'observe', what: 'observe: Tier 0 + async mock Jev', threshold: 'p(95)<5' },
  c: { route: 'load-c', stream: false, mode: 'flag', what: 'flag, mock Jev 0 ms', threshold: 'p(95)<10' },
  d: { route: 'load-d', stream: false, mode: 'flag', what: 'flag, mock Jev 150 ms', threshold: 'p(95)<175' },
  e: { route: 'load-e', stream: true, mode: 'block', what: 'block, buffered streams (report only)', threshold: null },
  f: { route: 'load-f', stream: false, mode: 'flag', what: 'Jev hangs 15%, hedge 400 ms', threshold: 'p(99)<=1500', noServerErrors: true },
  g: { route: 'load-g', stream: false, mode: 'block', what: 'block, JSON answers (not streamed)', threshold: 'p(95)<10' },
};

const selected = (__ENV.SCENARIOS || 'a,b,c,e,f,g')
  .split(',')
  .map((s) => s.trim())
  .filter((s) => s !== '');
for (const name of selected) {
  if (!SCENARIOS[name]) throw new Error(`unknown scenario "${name}" (known: ${Object.keys(SCENARIOS).join(', ')})`);
}

// k6 duration strings as the executor takes them: 45s, 2m, 1m30s.
function seconds(duration) {
  let total = 0;
  const re = /(\d+(?:\.\d+)?)(ms|s|m|h)/g;
  let match;
  while ((match = re.exec(duration)) !== null) {
    const unit = { ms: 0.001, s: 1, m: 60, h: 3600 }[match[2]];
    total += Number(match[1]) * unit;
  }
  if (total <= 0) throw new Error(`DURATION "${duration}" is not a k6 duration`);
  return total;
}

const overhead = new Trend('gateway_overhead_ms', true);
const serverErrors = new Rate('gateway_5xx');
const non200 = new Counter('gateway_non_200');
const requests = new Counter('gateway_requests');
const withoutHeader = new Counter('gateway_overhead_without_header');

// What teardown reads from /metrics and the mock, as gauges handleSummary can see. Per scenario:
// the overhead histogram of its route (n and percentiles, ms); per run: the fast-check work and
// Tier 0's time. A value of -1 means it could not be read.
const STATS = ['n', 'p50', 'p95', 'p99', 'max'];
const routeGauges = {};
for (const name of selected) {
  routeGauges[name] = {};
  for (const stat of STATS) routeGauges[name][stat] = new Gauge(`load_metrics_${name}_${stat}`);
}
const runGauges = {};
for (const g of ['metrics_read', 'jev_attempts', 'jev_hedged', 'mock_requests', 'detectors_n', 'detectors_p50', 'detectors_p95']) {
  runGauges[g] = new Gauge(`load_${g}`);
}

const scenarios = {};
const thresholds = {};
selected.forEach((name, i) => {
  scenarios[name] = {
    executor: 'constant-arrival-rate',
    rate: RATE,
    timeUnit: '1s',
    duration: DURATION,
    preAllocatedVUs: Math.max(10, RATE),
    maxVUs: Math.max(50, RATE * 4),
    startTime: `${Math.round(i * (seconds(DURATION) + GAP_S))}s`,
    exec: 'gate', // k6 tags every sample with scenario:<name>, which the thresholds select
  };
  const sc = SCENARIOS[name];
  thresholds[`gateway_overhead_ms{scenario:${name}}`] = sc.threshold ? [sc.threshold] : ['p(95)>=0'];
  // Thresholds that always hold make k6 keep each scenario's own counters for the summary.
  thresholds[`gateway_5xx{scenario:${name}}`] = [sc.noServerErrors ? 'rate==0' : 'rate>=0'];
  thresholds[`gateway_non_200{scenario:${name}}`] = ['count>=0'];
  thresholds[`gateway_requests{scenario:${name}}`] = ['count>=0'];
  thresholds[`gateway_overhead_without_header{scenario:${name}}`] = ['count>=0'];
});

export const options = {
  scenarios,
  thresholds,
  summaryTrendStats: ['avg', 'min', 'med', 'p(95)', 'p(99)', 'max', 'count'],
  discardResponseBodies: false,
};

// The evidence the answer is checked against, and a question it answers (mock-upstream's
// -default-answer repeats the document).
const MESSAGES = [
  {
    role: 'system',
    content:
      'You are the Acme support assistant. Answer only from the documents below.\n\n' +
      '<document id="refund-policy.md">Annual plans can be refunded within 30 days of purchase.</document>',
  },
  { role: 'user', content: 'How long do I have to ask for a refund on an annual plan?' },
];

export function gate() {
  const name = exec.scenario.name;
  const sc = SCENARIOS[name];
  const headers = {
    'Content-Type': 'application/json',
    // The upstream credential, passed through to mock-upstream (which accepts any).
    Authorization: 'Bearer load-test',
    'X-TrustAgent-Route': sc.route,
  };
  if (API_KEY) headers['X-TrustAgent-Key'] = API_KEY;
  if (name === 'f' && Math.random() < HANG_RATE) {
    // Test mode forwards this forward-only value to the fast check (never stored on the trace),
    // with X-Mock-Key set to the trace id, which a request's attempt and hedge share: mock-upstream
    // reads fault:hang@1 as "hang the first decision request of this call", so the hedge answers.
    headers['X-TrustAgent-Scenario'] = 'fault:hang@1';
  }
  const body = JSON.stringify({ model: 'load-test', stream: sc.stream, messages: MESSAGES });
  const res = http.post(`${BASE_URL}/v1/chat/completions`, body, { headers, timeout: '30s' });

  requests.add(1);
  serverErrors.add(res.status >= 500);
  if (res.status !== 200) non200.add(1);
  check(res, {
    'status is 200': (r) => r.status === 200,
    'the route applied': (r) => String(r.headers['X-Trustagent-Route'] || '').startsWith(`${sc.route}@`),
  });
  const header = res.headers['X-Trustagent-Overhead-Ms'];
  if (header !== undefined && header !== '' && !Number.isNaN(Number(header))) {
    overhead.add(Number(header));
  } else if (res.status === 200) {
    withoutHeader.add(1);
    overhead.add(res.timings.duration);
  }
}

// reading is what setup and teardown read: the proxy's /metrics and the fast-check mock's totals.
function reading() {
  const out = { samples: null, mock: null };
  const m = http.get(METRICS_URL, { timeout: '10s', tags: { name: 'metrics' } });
  if (m.status === 200) out.samples = parseMetrics(m.body);
  const r = http.get(`${JEV_MOCK_URL}/_mock/requests`, { timeout: '10s', tags: { name: 'mock_requests' } });
  if (r.status === 200) {
    try {
      out.mock = JSON.parse(r.body).totals || {};
    } catch (e) {
      out.mock = null;
    }
  }
  return out;
}

export function setup() {
  return reading();
}

// teardown turns the two readings into the run's gauges (see routeGauges and runGauges).
export function teardown(before) {
  const after = reading();
  const ok = before && before.samples && after.samples;
  runGauges.metrics_read.add(ok ? 1 : 0);
  for (const name of selected) {
    const g = routeGauges[name];
    let st = { n: -1, p50: -1, p95: -1, p99: -1, max: -1 };
    if (ok) {
      const want = { route: SCENARIOS[name].route };
      st = histogramStats(
        buckets(before.samples, 'trustagent_gateway_overhead_seconds', want),
        buckets(after.samples, 'trustagent_gateway_overhead_seconds', want),
      );
    }
    for (const stat of STATS) g[stat].add(st[stat] === null ? -1 : st[stat]);
  }
  if (ok) {
    const delta = (name, want) => sumOf(after.samples, name, want) - sumOf(before.samples, name, want);
    runGauges.jev_attempts.add(delta('trustagent_gateway_jev_requests_total'));
    // Every hedge leaves exactly one loser (the hedge or the request it raced).
    runGauges.jev_hedged.add(delta('trustagent_gateway_jev_requests_total', { outcome: 'hedge_lost' }));
    const det = histogramStats(
      buckets(before.samples, 'trustagent_gateway_gate_seconds', { stage: 'detectors' }),
      buckets(after.samples, 'trustagent_gateway_gate_seconds', { stage: 'detectors' }),
    );
    runGauges.detectors_n.add(det.n);
    runGauges.detectors_p50.add(det.p50 === null ? -1 : det.p50);
    runGauges.detectors_p95.add(det.p95 === null ? -1 : det.p95);
  } else {
    for (const g of ['jev_attempts', 'jev_hedged', 'detectors_n', 'detectors_p50', 'detectors_p95']) runGauges[g].add(-1);
  }
  runGauges.mock_requests.add(
    before && before.mock && after.mock ? decisionRequests(after.mock) - decisionRequests(before.mock) : -1,
  );
}

// gauge reads a teardown gauge from the summary (null when unread).
function gauge(data, name) {
  const m = data.metrics[name];
  if (!m || m.values.value === undefined || m.values.value < 0) return null;
  return m.values.value;
}

function count(data, metric, name) {
  const m = data.metrics[`${metric}{scenario:${name}}`];
  return m && m.values.count !== undefined ? m.values.count : 0;
}

// scenarioResult is one scenario's figures: from the overhead header when every answer carried
// it, else from the gateway's histogram for its route (observe), else the total time.
export function scenarioResult(data, name) {
  const sc = SCENARIOS[name];
  const metric = data.metrics[`gateway_overhead_ms{scenario:${name}}`];
  const v = metric ? metric.values : {};
  const missing = count(data, 'gateway_overhead_without_header', name);
  const fromMetrics = {};
  for (const stat of STATS) fromMetrics[stat] = gauge(data, `load_metrics_${name}_${stat}`);
  let source = 'header';
  let fig = { n: v.count || 0, p50: v.med, p95: v['p(95)'], p99: v['p(99)'], max: v.max };
  if (missing > 0) {
    if (sc.mode === 'observe' && fromMetrics.n) {
      source = 'metrics';
      fig = { ...fromMetrics, max: fromMetrics.max === null ? v.max : fromMetrics.max };
    } else {
      source = 'total';
    }
  }
  const t = target(sc.threshold);
  const r5 = data.metrics[`gateway_5xx{scenario:${name}}`];
  const rate5xx = r5 && r5.values.rate !== undefined ? r5.values.rate : 0;
  let ok = true;
  if (t) {
    const stat = fig[t.stat.replace('_ms', '')];
    ok = stat !== undefined && stat !== null && (t.inclusive ? stat <= t.max_ms : stat < t.max_ms);
  }
  if (sc.noServerErrors && rate5xx > 0) ok = false;
  const round = (x) => (x === undefined || x === null ? null : Math.round(x * 1000) / 1000);
  return {
    id: name,
    route: sc.route,
    mode: sc.mode,
    streaming: sc.stream,
    n: fig.n,
    p50_ms: round(fig.p50),
    p95_ms: round(fig.p95),
    p99_ms: round(fig.p99),
    max_ms: round(fig.max),
    source,
    target: t ? { stat: t.stat, max_ms: t.max_ms } : null,
    ok,
    report_only: sc.threshold === null,
    without_header: missing,
    requests: count(data, 'gateway_requests', name),
    non_200: count(data, 'gateway_non_200', name),
    rate_5xx: rate5xx,
  };
}

function fmt(value) {
  return value === undefined || value === null ? 'n/a' : `${value.toFixed(1)} ms`;
}

// The human table, then the result line scripts/load/report.py reads.
export function handleSummary(data) {
  const results = selected.map((name) => scenarioResult(data, name));
  const lines = ['', 'TrustAgent gateway overhead (simulated vendors: mock-upstream answers at once)', ''];
  for (const r of results) {
    const sc = SCENARIOS[r.id];
    const verdict = sc.threshold === null ? 'report only' : r.ok ? `ok (${sc.threshold})` : `FAILED (${sc.threshold})`;
    lines.push(
      `  (${r.id}) ${sc.what.padEnd(40)} med ${fmt(r.p50_ms)}  p95 ${fmt(r.p95_ms)}  p99 ${fmt(r.p99_ms)}  [${r.source}, n ${r.n}]  ${verdict}`,
    );
  }
  const read = gauge(data, 'load_metrics_read') === 1;
  const jev = read
    ? { attempts: gauge(data, 'load_jev_attempts'), hedged: gauge(data, 'load_jev_hedged'), mock_requests: gauge(data, 'load_mock_requests') }
    : null;
  const detectors = read
    ? { n: gauge(data, 'load_detectors_n'), p50_ms: gauge(data, 'load_detectors_p50'), p95_ms: gauge(data, 'load_detectors_p95') }
    : null;
  if (!read) lines.push('', `  ${METRICS_URL} could not be read: no fast-check counters, and observe figures are total times.`);
  lines.push('');
  const result = { rate: RATE, duration: DURATION, hang_rate: HANG_RATE, metrics_read: read, scenarios: results, jev, detectors };
  lines.push(RESULT_PREFIX + JSON.stringify(result), '');
  const out = { stdout: lines.join('\n') };
  if (__ENV.SUMMARY_PATH) out[__ENV.SUMMARY_PATH] = JSON.stringify(data, null, 2);
  return out;
}

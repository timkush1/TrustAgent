// k6: the time the TrustAgent gateway adds to an answer, per scenario (docs/GATEWAY.md §13,
// docs/v2/design-notes/design-gateway.md §14).
//
// The overhead of each answer is its X-TrustAgent-Overhead-Ms header: the whole request minus the
// upstream's own time. Observe routes decide after the answer is sent and have no such header;
// there the total request time counts instead, an upper bound because mock-upstream answers at
// once (it also holds the network hops). Every vendor is mock-upstream (docker-compose.load.yml),
// so nothing is spent.
//
//   scenario  route   what                                          target (overhead)
//   a         load-a  gate disabled: nothing to ask                 p95 < 3 ms
//   b         load-b  observe: Tier 0 + asynchronous mock Jev       p95 < 5 ms
//   c         load-c  flag, mock Jev answering in 0 ms              p95 < 10 ms
//   d         load-d  flag, mock Jev answering in 150 ms            p95 < 175 ms   (slow-Jev pass)
//   e         load-e  block with buffered streams                   reported only
//   f         load-f  15% of Jev calls hang, hedge at 400 ms        p99 <= 1500 ms (the deadline), no 5xx
//
// `make load-test-gate` runs a, b, c, e and f against mock-upstream, then d after pointing the
// gateway at mock-jev-slow. By hand, with the load stack up (`make load-up`; it publishes no port),
// inside the proxy container's network namespace:
//
//   API_KEY="$(python3 scripts/envfile.py get TRAFFICGEN_API_KEY)" \
//   docker run --rm -i --network "container:$(docker compose -p truthtable-load ps -q proxy)" \
//     -e BASE_URL=http://127.0.0.1:8080 -e API_KEY -e SCENARIOS=a,b,c \
//     grafana/k6 run - < scripts/load/gateway-overhead.js
//
// Environment: BASE_URL (http://localhost:8080), API_KEY (the proxy key, X-TrustAgent-Key),
// SCENARIOS (a,b,c,e,f), RATE (requests per second per scenario, 20), DURATION (per scenario,
// 30s), HANG_RATE (share of scenario f's requests whose fast check hangs, 0.15), SUMMARY_PATH (a
// JSON file for the summary, none by default).

import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { Counter, Rate, Trend } from 'k6/metrics';

const BASE_URL = (__ENV.BASE_URL || 'http://localhost:8080').replace(/\/+$/, '');
const API_KEY = __ENV.API_KEY || '';
const RATE = Number(__ENV.RATE || 20);
const DURATION = __ENV.DURATION || '30s';
const HANG_RATE = Number(__ENV.HANG_RATE || 0.15);
const GAP_S = 5; // quiet time between scenarios, so one's tail does not overlap the next

export const SCENARIOS = {
  a: { route: 'load-a', stream: false, what: 'gate disabled (nothing to ask)', threshold: 'p(95)<3' },
  b: { route: 'load-b', stream: false, what: 'observe: Tier 0 + async mock Jev', threshold: 'p(95)<5' },
  c: { route: 'load-c', stream: false, what: 'flag, mock Jev 0 ms', threshold: 'p(95)<10' },
  d: { route: 'load-d', stream: false, what: 'flag, mock Jev 150 ms', threshold: 'p(95)<175' },
  e: { route: 'load-e', stream: true, what: 'block, buffered streams (report only)', threshold: null },
  f: { route: 'load-f', stream: false, what: 'Jev hangs 15%, hedge 400 ms', threshold: 'p(99)<=1500', noServerErrors: true },
};

const selected = (__ENV.SCENARIOS || 'a,b,c,e,f')
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
const withoutHeader = new Counter('gateway_overhead_without_header');

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
  if (sc.noServerErrors) thresholds[`gateway_5xx{scenario:${name}}`] = ['rate==0'];
});

export const options = {
  scenarios,
  thresholds,
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
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
    // Test mode forwards the scenario id to the fast check; mock-upstream reads fault:hang@1 as
    // "hang the first decision request of this call" (keyed by its session id, the trace id), so
    // the gateway's hedge answers instead.
    headers['X-TrustAgent-Scenario'] = 'fault:hang@1';
  }
  const body = JSON.stringify({ model: 'load-test', stream: sc.stream, messages: MESSAGES });
  const res = http.post(`${BASE_URL}/v1/chat/completions`, body, { headers, timeout: '30s' });

  serverErrors.add(res.status >= 500);
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

function fmt(value) {
  return value === undefined ? 'n/a' : `${value.toFixed(1)} ms`;
}

// One line per scenario: the overhead percentiles, the target and whether it held.
export function handleSummary(data) {
  const lines = ['', 'TrustAgent gateway overhead (X-TrustAgent-Overhead-Ms; mock-upstream, simulated vendors)', ''];
  for (const name of selected) {
    const sc = SCENARIOS[name];
    const metric = data.metrics[`gateway_overhead_ms{scenario:${name}}`];
    const v = metric ? metric.values : {};
    let verdict = 'report only';
    if (sc.threshold && metric && metric.thresholds) {
      verdict = Object.values(metric.thresholds).every((t) => t.ok) ? `ok (${sc.threshold})` : `FAILED (${sc.threshold})`;
    }
    lines.push(
      `  (${name}) ${sc.what.padEnd(40)} med ${fmt(v.med)}  p95 ${fmt(v['p(95)'])}  p99 ${fmt(v['p(99)'])}  ${verdict}`,
    );
  }
  const missing = data.metrics.gateway_overhead_without_header;
  if (missing && missing.values.count > 0) {
    lines.push('', `  ${missing.values.count} answer(s) had no X-TrustAgent-Overhead-Ms (observe routes): their total time counted.`);
  }
  lines.push('');
  const out = { stdout: lines.join('\n') + '\n' };
  if (__ENV.SUMMARY_PATH) out[__ENV.SUMMARY_PATH] = JSON.stringify(data, null, 2);
  return out;
}

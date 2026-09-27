// k6 load test: proxy overhead on the hot path.
//
// Uses the test_response shortcut so no LLM is involved — what's measured is
// purely TrustAgent's added work on the request path (parse, tee, enqueue),
// which is the number that matters: audits are async by design.
//
// test_response needs TRUTHTABLE_TEST_MODE, which the demo stack turns on.
// Run (demo stack up: `make demo`; the key is TRAFFICGEN_API_KEY in .env):
//   k6 run -e API_KEY=<key> scripts/load/proxy-overhead.js
// or without installing k6 (`make load-test` passes TRAFFICGEN_API_KEY):
//   docker run --rm -i --network host -e API_KEY=<key> grafana/k6 run - < scripts/load/proxy-overhead.js

import http from 'k6/http';
import { check } from 'k6';

export const options = {
  stages: [
    { duration: '10s', target: 20 }, // ramp up
    { duration: '30s', target: 20 }, // sustained load
    { duration: '5s', target: 0 },   // ramp down
  ],
  thresholds: {
    http_req_duration: ['p(95)<100'], // proxy overhead budget: 100ms p95
    http_req_failed: ['rate<0.01'],
  },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

export default function () {
  const headers = { 'Content-Type': 'application/json' };
  if (__ENV.API_KEY) {
    // The proxy's own key; Authorization is the upstream's (SPEC §2.4, §2.12).
    headers['X-TrustAgent-Key'] = __ENV.API_KEY;
  }

  const payload = JSON.stringify({
    model: 'load-test',
    messages: [{ role: 'user', content: 'What is the capital of France?' }],
    test_response: 'Paris is the capital of France.',
  });

  const res = http.post(`${BASE_URL}/v1/chat/completions`, payload, { headers });

  check(res, {
    'status is 200': (r) => r.status === 200,
  });
}

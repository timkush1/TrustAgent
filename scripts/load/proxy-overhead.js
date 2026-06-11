// k6 load test: proxy overhead on the hot path.
//
// Uses the test_response shortcut so no LLM is involved — what's measured is
// purely TrustAgent's added work on the request path (parse, tee, enqueue),
// which is the number that matters: audits are async by design.
//
// Run (stack up, e.g. `make up-all`):
//   k6 run scripts/load/proxy-overhead.js
// or without installing k6:
//   docker run --rm -i --network host grafana/k6 run - < scripts/load/proxy-overhead.js
//
// With auth enabled, pass the key:
//   k6 run -e API_KEY=<key> scripts/load/proxy-overhead.js

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
    headers['Authorization'] = `Bearer ${__ENV.API_KEY}`;
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

// Scenario 3: steady load while e2e/run.sh deletes an API pod and drains a
// node. Gates: 0 failed requests, p95 < SLO.
import http from 'k6/http';
import { check } from 'k6';
import { TARGET, baseOptions, gates, summary } from './lib.js';

export const options = Object.assign({}, baseOptions, {
  scenarios: {
    steady: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.STEADY_RPS || 150),
      timeUnit: '1s',
      duration: __ENV.DURATION || '170s',
      preAllocatedVUs: 50,
      maxVUs: 300,
    },
  },
  thresholds: gates,
});

export default function () {
  const res = http.get(`${TARGET}/api/v1/work`, { timeout: '5s', tags: { name: 'work' } });
  check(res, { 'status is 200': (r) => r.status === 200 });
}

export function handleSummary(data) {
  return summary(data);
}

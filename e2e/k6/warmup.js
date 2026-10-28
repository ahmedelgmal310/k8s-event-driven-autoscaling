// Pulls the k6 image onto the infra node and proves the load-test namespace
// can reach the API through its NetworkPolicy before any timed scenario runs.
import http from 'k6/http';
import { check } from 'k6';
import { TARGET, summary } from './lib.js';

export const options = {
  scenarios: { warmup: { executor: 'shared-iterations', vus: 1, iterations: 5 } },
  thresholds: { checks: ['rate==1'] },
};

export default function () {
  const res = http.get(`${TARGET}/api/v1/work`, { timeout: '5s' });
  check(res, { 'status is 200': (r) => r.status === 200 });
}

export function handleSummary(data) {
  return summary(data);
}

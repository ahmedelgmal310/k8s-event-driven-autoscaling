// Scenario 2: a burst of BURST_N jobs, as fast as the API accepts them.
// Every submission must be accepted (HTTP 202).
import http from 'k6/http';
import { check } from 'k6';
import { TARGET, baseOptions, gates, summary } from './lib.js';

const N = Number(__ENV.BURST_N || 4000);
const PARAMS = {
  headers: { 'Content-Type': 'application/json' },
  timeout: '5s',
  tags: { name: 'enqueue' },
};

export const options = Object.assign({}, baseOptions, {
  scenarios: {
    burst: { executor: 'shared-iterations', vus: 50, iterations: N, maxDuration: '3m' },
  },
  thresholds: gates,
});

export default function () {
  const res = http.post(`${TARGET}/api/v1/jobs`, JSON.stringify({ vu: __VU, iter: __ITER }), PARAMS);
  check(res, { 'status is 202': (r) => r.status === 202 });
}

export function handleSummary(data) {
  return summary(data);
}

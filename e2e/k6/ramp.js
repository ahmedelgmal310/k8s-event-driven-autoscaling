// Scenario 1: traffic ramp. 20 -> PEAK req/s, hold, back down to 20 req/s and
// hold while the API scales in. Gates: 0 failed requests, p95 < SLO.
import http from 'k6/http';
import { check } from 'k6';
import { TARGET, baseOptions, gates, summary } from './lib.js';

const PEAK = Number(__ENV.PEAK_RPS || 300);

export const options = Object.assign({}, baseOptions, {
  scenarios: {
    ramp: {
      executor: 'ramping-arrival-rate',
      startRate: 20,
      timeUnit: '1s',
      preAllocatedVUs: 50,
      maxVUs: 400,
      stages: [
        { target: 20, duration: '30s' }, // baseline at the replica floor
        { target: PEAK, duration: '60s' }, // ramp: scale-out happens here
        { target: PEAK, duration: '120s' }, // hold at peak
        { target: 20, duration: '30s' }, // ramp down
        { target: 20, duration: '150s' }, // scale-in happens here, under load
      ],
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

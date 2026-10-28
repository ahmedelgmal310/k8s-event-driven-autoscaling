// Shared settings and summary output for the e2e load tests.
export const TARGET = __ENV.TARGET || 'http://demo-api.demo.svc.cluster.local';
export const SLO_P95_MS = Number(__ENV.SLO_P95_MS || 250);

// Every request opens a new connection, so kube-proxy balances each request
// across the current endpoints. With keep-alive, the first pods would keep
// their long-lived connections and new replicas would get little traffic.
export const baseOptions = {
  noConnectionReuse: true,
  discardResponseBodies: true,
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
};

// Zero failed requests and the latency SLO are hard gates: k6 exits non-zero
// (and the Job fails) when a threshold is crossed.
export const gates = {
  http_req_failed: ['rate==0'],
  'http_req_duration{expected_response:true}': [`p(95)<${SLO_P95_MS}`],
  checks: ['rate==1'],
};

function val(m, name, key) {
  return m[name] && m[name].values[key] !== undefined ? m[name].values[key] : null;
}

// summary prints a compact JSON summary between markers so e2e/run.sh can
// pull it out of the Job logs.
export function summary(data) {
  const m = data.metrics;
  const out = {
    requests: val(m, 'http_reqs', 'count'),
    rps_avg: val(m, 'http_reqs', 'rate'),
    failed_rate: val(m, 'http_req_failed', 'rate'),
    failed: val(m, 'http_req_failed', 'passes'),
    checks_rate: val(m, 'checks', 'rate'),
    dropped_iterations: val(m, 'dropped_iterations', 'count') || 0,
    p50_ms: val(m, 'http_req_duration', 'med'),
    p95_ms: val(m, 'http_req_duration', 'p(95)'),
    p99_ms: val(m, 'http_req_duration', 'p(99)'),
    max_ms: val(m, 'http_req_duration', 'max'),
    duration_s: data.state ? data.state.testRunDurationMs / 1000 : null,
    thresholds_ok: Object.values(m).every(
      (x) => !x.thresholds || Object.values(x.thresholds).every((t) => t.ok),
    ),
  };
  return { stdout: '\nK6_SUMMARY_BEGIN\n' + JSON.stringify(out) + '\nK6_SUMMARY_END\n' };
}

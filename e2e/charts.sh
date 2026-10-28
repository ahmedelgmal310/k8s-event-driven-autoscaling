#!/usr/bin/env bash
# Renders the e2e time series in $RESULTS_DIR as SVG charts (cmd/chartgen).
set -euo pipefail

RESULTS_DIR="${RESULTS_DIR:-e2e-results}"
chartgen() { go run ./cmd/chartgen "$@"; }

chartgen -in "${RESULTS_DIR}/s1-timeline.csv" -markers "${RESULTS_DIR}/s1-markers.csv" \
  -out "${RESULTS_DIR}/scenario1-api-scaling.svg" \
  -title "Scenario 1: API replicas follow the traffic ramp (CPU + req/s triggers)" \
  -left "api_desired=Desired replicas,api_ready=Ready replicas~" \
  -right "rps=Requests/s (Prometheus)" -right-label "requests per second"

chartgen -in "${RESULTS_DIR}/s2-timeline.csv" -markers "${RESULTS_DIR}/s2-markers.csv" \
  -out "${RESULTS_DIR}/scenario2-worker-scale-to-zero.svg" \
  -title "Scenario 2: workers scale 0 -> N -> 0 on Redis Streams consumer-group lag" \
  -left "worker_desired=Desired workers,worker_ready=Ready workers~" \
  -right "lag=Queue lag (undelivered jobs)" -right-label "jobs"

chartgen -in "${RESULTS_DIR}/s3-timeline.csv" -markers "${RESULTS_DIR}/s3-markers.csv" \
  -out "${RESULTS_DIR}/scenario3-disruption.svg" \
  -title "Scenario 3: pod deleted and node drained under steady load" \
  -left "api_desired=Desired replicas,api_ready=Ready replicas~" \
  -right "rps=Requests/s (Prometheus)" -right-label "requests per second"

ls -l "${RESULTS_DIR}"/*.svg

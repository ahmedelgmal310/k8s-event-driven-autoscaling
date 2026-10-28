#!/usr/bin/env bash
# End-to-end proof on a multi-node kind cluster (platform from e2e/setup.sh):
#   1. traffic ramp  -> API scales out on CPU + req/s, p95 < SLO, 0 errors, then scales in
#   2. job burst     -> workers scale 0 -> N on queue lag, queue drained in time, back to 0
#   3. disruption    -> API pod deleted and a node drained under load, 0 errors
# Every gate that fails exits non-zero. Time series, k6 summaries and the
# derived numbers are written to $RESULTS_DIR.
# Requires: kubectl, helm, jq, awk; the image event-autoscaling:e2e loaded into kind.
set -euo pipefail

NS="${NS:-demo}"
RELEASE="${RELEASE:-demo}"
CHART="${CHART:-charts/event-autoscaling}"
RESULTS_DIR="${RESULTS_DIR:-e2e-results}"
SLO_P95_MS="${SLO_P95_MS:-250}"
BURST_N="${BURST_N:-4000}"
DRAIN_TARGET_S="${DRAIN_TARGET_S:-120}"
ZERO_TARGET_S="${ZERO_TARGET_S:-120}"
API_MIN=2
API_MIN_PEAK=4

API="${RELEASE}-api"
WORKER="${RELEASE}-worker"
REDIS="${RELEASE}-redis"
HPA="keda-hpa-${API}"
STREAM="jobs"
GROUP="workers"

mkdir -p "$RESULTS_DIR"
SAMPLER_PIDS=()

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
now() { date +%s.%3N; }
minus() { awk -v a="$1" -v b="$2" 'BEGIN { printf "%.1f", a - b }'; }
# rel T ORIGIN: T - ORIGIN, or empty when T is empty (event not observed).
rel() { if [[ -n $1 ]]; then minus "$1" "$2"; fi; }

dump() {
  log "---- diagnostics ----"
  kubectl -n "$NS" get scaledobject,hpa,deploy,pods,pdb -o wide >&2 || true
  kubectl -n "$NS" describe hpa >&2 || true
  kubectl get nodes -L pool,topology.kubernetes.io/zone >&2 || true
  kubectl -n keda logs deploy/keda-operator --tail=60 >&2 || true
  kubectl -n loadtest get pods -o wide >&2 || true
  kubectl get events -A --sort-by=.lastTimestamp 2>/dev/null | tail -40 >&2 || true
}

stop_samplers() {
  for p in "${SAMPLER_PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
  for p in "${SAMPLER_PIDS[@]}"; do wait "$p" 2>/dev/null || true; done
  SAMPLER_PIDS=()
}

die() {
  log "FAIL: $*"
  stop_samplers
  dump
  exit 1
}
trap stop_samplers EXIT

prom_query() {
  local q
  q=$(jq -rn --arg q "$1" '$q|@uri')
  kubectl get --raw "/api/v1/namespaces/monitoring/services/prometheus-server:80/proxy/api/v1/query?query=${q}" \
    | jq -r '.data.result[0].value[1] // ""'
}

# Queue stats straight from Redis (kubectl exec, so no extra network path):
# "lag pending processed". lag is -1 if Redis cannot compute it.
# shellcheck disable=SC2016 # Lua source, not a shell expansion
LUA='local lag, pending = -1, -1
local ok, groups = pcall(redis.call, "XINFO", "GROUPS", KEYS[1])
if ok then
  for _, g in ipairs(groups) do
    local t = {}
    for i = 1, #g, 2 do t[g[i]] = g[i + 1] end
    if t["name"] == ARGV[1] then lag = t["lag"] or -1; pending = t["pending"] end
  end
end
local processed = redis.call("GET", KEYS[2]) or 0
return {tostring(lag), tostring(pending), tostring(processed)}'

queue_stats() {
  kubectl -n "$NS" exec "deploy/${REDIS}" -- redis-cli --raw EVAL "$LUA" 2 "$STREAM" "${STREAM}:processed" "$GROUP" \
    | paste -sd' ' -
}

# One CSV row (without t): api_desired,api_ready,worker_desired,worker_ready,
# rps,cpu_util,rps_per_pod,lag,pending,processed
sample_once() {
  local deploys hpa reps metrics rps q
  deploys=$(kubectl -n "$NS" get deploy "$API" "$WORKER" -o json)
  reps=$(jq -r --arg a "$API" --arg w "$WORKER" '
    def r(n): [.items[] | select(.metadata.name == n)][0] | [(.spec.replicas // 0), (.status.readyReplicas // 0)];
    (r($a) + r($w)) | map(tostring) | join(",")' <<<"$deploys")
  hpa=$(kubectl -n "$NS" get hpa "$HPA" -o json 2>/dev/null || echo '{}')
  metrics=$(jq -r '
    def qty: if . == null then "" elif test("m$") then (.[:-1] | tonumber / 1000 | tostring) else . end;
    [ ([.status.currentMetrics[]? | select(.type == "Resource") | .resource.current.averageUtilization][0] // "" | tostring),
      ([.status.currentMetrics[]? | select(.type == "External") | .external.current.averageValue][0] | qty)
    ] | join(",")' <<<"$hpa")
  rps=$(prom_query "sum(rate(http_requests_total{namespace=\"${NS}\",route=~\"/api/v1/.*\"}[30s]))" || true)
  q=$(queue_stats 2>/dev/null | tr ' ' ',' || true)
  [[ $q == *,*,* ]] || q=",,"
  printf '%s,%s,%s,%s\n' "$reps" "$rps" "$metrics" "$q"
}

# start_samplers PREFIX T0: replicas/HPA/queue every ~2s, kubectl top every 10s.
start_samplers() {
  local prefix=$1 t0=$2
  (
    echo "t,api_desired,api_ready,worker_desired,worker_ready,rps,cpu_util,rps_per_pod,lag,pending,processed"
    while true; do
      ts=$(now)
      row=$(sample_once 2>/dev/null) || row=",,,,,,,,,"
      echo "$(minus "$ts" "$t0"),${row}"
      sleep 1
    done
  ) >"${prefix}-timeline.csv" &
  SAMPLER_PIDS+=($!)
  (
    echo "t,pod,cpu_m,mem_mi"
    while true; do
      ts=$(now)
      kubectl -n "$NS" top pods --no-headers 2>/dev/null \
        | awk -v t="$(minus "$ts" "$t0")" '{ gsub("m", "", $2); gsub("Mi", "", $3); print t "," $1 "," $2 "," $3 }' || true
      sleep 10
    done
  ) >"${prefix}-top.csv" &
  SAMPLER_PIDS+=($!)
}

# csv_first FILE FROM COND: first t >= FROM where the awk COND holds; columns via v("name").
csv_first() {
  awk -F, -v from="$2" '
    function v(n) { return $(c[n]) }
    NR == 1 { for (i = 1; i <= NF; i++) c[$i] = i; next }
    ($1 + 0) >= from && ('"$3"') { print $1; exit }' "$1"
}

# csv_agg FILE COL max|min [FROM [TO]]: aggregate of the non-empty cells of COL.
csv_agg() {
  awk -F, -v col="$2" -v op="$3" -v from="${4:--1e9}" -v to="${5:-1e9}" '
    NR == 1 { for (i = 1; i <= NF; i++) c[$i] = i; next }
    ($1 + 0) >= from && ($1 + 0) <= to && $(c[col]) != "" {
      x = $(c[col]) + 0
      if (!seen || (op == "max" && x > m) || (op == "min" && x < m)) { m = x; seen = 1 }
    }
    END { if (seen) printf "%g", m }' "$1"
}

# top_stat FILE PODPREFIX FROM TO: "avg_cpu_m max_cpu_m max_mem_mi" per pod sample.
top_stat() {
  awk -F, -v p="$2" -v from="$3" -v to="$4" '
    NR > 1 && index($2, p) == 1 && ($1 + 0) >= from && ($1 + 0) <= to {
      n++; s += $3; if ($3 + 0 > mc) mc = $3 + 0; if ($4 + 0 > mm) mm = $4 + 0
    }
    END { if (n) printf "%.0f %.0f %.0f", s / n, mc, mm; else printf "  " }' "$1"
}

k6_job() {
  sed -e "s/__NAME__/$1/g" -e "s/__SCRIPT__/$2/" -e "s/__SLO__/${SLO_P95_MS}/" -e "s/__BURST_N__/${BURST_N}/" e2e/k6/job.yaml
}

# start_k6 NAME SCRIPT: prints the epoch at which the k6 pod started running.
start_k6() {
  local phase=""
  kubectl -n loadtest delete job "k6-$1" --ignore-not-found --wait=true >/dev/null
  k6_job "$1" "$2" | kubectl apply -f - >/dev/null
  for _ in $(seq 1 180); do
    phase=$(kubectl -n loadtest get pods -l "app.kubernetes.io/instance=$1" -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
    if [[ $phase == Running || $phase == Succeeded || $phase == Failed ]]; then
      now
      return 0
    fi
    sleep 1
  done
  return 1
}

# wait_k6 NAME TIMEOUT: waits for the Job, saves logs + summary, fails on threshold breach.
wait_k6() {
  local name=$1 deadline=$((SECONDS + $2)) ok="" failed=""
  while ((SECONDS < deadline)); do
    ok=$(kubectl -n loadtest get job "k6-${name}" -o jsonpath='{.status.succeeded}')
    failed=$(kubectl -n loadtest get job "k6-${name}" -o jsonpath='{.status.failed}')
    [[ -n $ok || -n $failed ]] && break
    sleep 2
  done
  kubectl -n loadtest logs "job/k6-${name}" >"${RESULTS_DIR}/k6-${name}.log" 2>&1 || true
  sed -n '/^K6_SUMMARY_BEGIN$/,/^K6_SUMMARY_END$/p' "${RESULTS_DIR}/k6-${name}.log" | sed '1d;$d' >"${RESULTS_DIR}/k6-${name}.json"
  log "k6 ${name}: $(cat "${RESULTS_DIR}/k6-${name}.json")"
  [[ $ok == 1 ]] || die "k6 ${name} failed (thresholds: 0 errors, p95 < ${SLO_P95_MS}ms); see ${RESULTS_DIR}/k6-${name}.log"
}

k6v() { jq -r ".$2 // empty" "${RESULTS_DIR}/k6-$1.json"; }

wait_for() { # wait_for TIMEOUT DESCRIPTION COMMAND...
  local timeout=$1 what=$2 deadline=$((SECONDS + $1))
  shift 2
  while ((SECONDS < deadline)); do
    if "$@"; then return 0; fi
    sleep 2
  done
  die "timed out after ${timeout}s waiting for: ${what}"
}

replicas() { kubectl -n "$NS" get deploy "$1" -o jsonpath='{.spec.replicas}'; }
worker_idle() {
  [[ $(replicas "$WORKER") == 0 ]] &&
    [[ -z $(kubectl -n "$NS" get pods -l app.kubernetes.io/component=worker -o name) ]]
}
api_at_min() { [[ $(replicas "$API") == "$API_MIN" ]]; }
hpa_has_both_metrics() {
  [[ $(kubectl -n "$NS" get hpa "$HPA" -o json 2>/dev/null \
    | jq '[.status.currentMetrics[]? | select(.type == "Resource" or .type == "External")] | length') == 2 ]]
}

# ---------------------------------------------------------------------------
log "deploying ${RELEASE} into ${NS}"
for ns in "$NS" loadtest; do
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
done
helm upgrade --install "$RELEASE" "$CHART" -n "$NS" -f e2e/values-e2e.yaml --wait --timeout 5m >/dev/null
kubectl -n "$NS" wait --for=condition=Ready "scaledobject/${API}" "scaledobject/${WORKER}" --timeout=180s >/dev/null \
  || die "ScaledObjects not Ready"
kubectl -n loadtest create configmap k6-scripts --from-file=e2e/k6/ --dry-run=client -o yaml | kubectl apply -f - >/dev/null

wait_for 240 "HPA ${HPA} reporting both CPU and request-rate metrics" hpa_has_both_metrics
wait_for 240 "worker scaled to zero while the queue is empty" worker_idle
log "baseline: api=$(replicas "$API") worker=$(replicas "$WORKER"); HPA metrics live"

start_k6 warmup warmup.js >/dev/null || die "k6 warmup did not start"
wait_k6 warmup 180

# --- Scenario 1: traffic ramp --------------------------------------------
log "scenario 1: traffic ramp 20 -> 300 req/s"
S1="${RESULTS_DIR}/s1"
T0=$(now)
start_samplers "$S1" "$T0"
k6_start=$(start_k6 ramp ramp.js) || die "k6 ramp did not start"
ks=$(minus "$k6_start" "$T0")
(sleep 170 && kubectl -n "$NS" get pods -l app.kubernetes.io/component=api -o json >"${S1}-pods-at-peak.json") &
wait_k6 ramp 720
k6_end=$(minus "$(now)" "$T0")
log "load finished; waiting for the API to return to ${API_MIN} replicas"
wait_for 300 "API scaled back in to ${API_MIN}" api_at_min
sleep 4
stop_samplers
wait

ramp_start=$(minus "$ks" -30)
rampdown_start=$(minus "$ks" -210)
s1_peak=$(csv_agg "${S1}-timeline.csv" api_desired max)
t_out=$(csv_first "${S1}-timeline.csv" 0 "v(\"api_desired\") > ${API_MIN}")
t_peak_ready=$(csv_first "${S1}-timeline.csv" 0 "v(\"api_ready\") >= ${s1_peak}")
t_in=$(csv_first "${S1}-timeline.csv" "$rampdown_start" "v(\"api_desired\") != \"\" && v(\"api_desired\") < ${s1_peak}")
t_min=$(csv_first "${S1}-timeline.csv" "$rampdown_start" "v(\"api_desired\") == ${API_MIN}")
replicas_at_k6_end=$(awk -F, -v e="$k6_end" 'NR > 1 && ($1 + 0) <= e && $2 != "" { r = $2 } END { print r }' "${S1}-timeline.csv")
s1_peak_cpu=$(csv_agg "${S1}-timeline.csv" cpu_util max)
s1_peak_rpp=$(csv_agg "${S1}-timeline.csv" rps_per_pod max)
s1_peak_rps=$(csv_agg "${S1}-timeline.csv" rps max)
read -r api_cpu_avg api_cpu_max api_mem_max <<<"$(top_stat "${S1}-top.csv" "${API}-" "$(minus "$ks" -120)" "$(minus "$ks" -210)")"
zones_at_peak=$(jq -r '[.items[] | select(.status.phase == "Running") | .spec.nodeName] | group_by(.) | map("\(.[0])=\(length)") | join(" ")' \
  "${S1}-pods-at-peak.json" 2>/dev/null || echo "")
printf 't,label\n%s,ramp start\n%s,peak hold\n%s,ramp down\n%s,k6 end\n' \
  "$ramp_start" "$(minus "$ks" -90)" "$rampdown_start" "$k6_end" >"${S1}-markers.csv"

[[ -n $t_out ]] || die "API never scaled out"
[[ ${s1_peak%.*} -ge $API_MIN_PEAK ]] || die "API peaked at ${s1_peak} replicas, expected >= ${API_MIN_PEAK}"
[[ -n $t_in ]] || die "API did not scale in after the load dropped"
[[ ${replicas_at_k6_end:-99} -lt ${s1_peak%.*} ]] || die "API had not started scaling in while k6 was still running"
time_to_scale_out=$(rel "$t_out" "$ramp_start")
time_to_peak_ready=$(rel "$t_peak_ready" "$ramp_start")
time_to_scale_in=$(rel "$t_in" "$rampdown_start")
time_to_min=$(rel "$t_min" "$rampdown_start")
log "scenario 1: peak ${s1_peak} replicas; first scale-out ${time_to_scale_out}s after ramp start; back to ${API_MIN} ${time_to_min}s after ramp down"

# --- Scenario 2: burst of jobs, scale from and back to zero -------------
log "scenario 2: burst of ${BURST_N} jobs into an idle queue"
wait_for 180 "worker at zero before the burst" worker_idle
read -r _ _ base_processed <<<"$(queue_stats)"
S2="${RESULTS_DIR}/s2"
T0=$(now)
start_samplers "$S2" "$T0"
k6_start=$(start_k6 burst burst.js) || die "k6 burst did not start"
ks=$(minus "$k6_start" "$T0")
wait_k6 burst 300
burst_end=$(minus "$(now)" "$T0")

target=$((base_processed + BURST_N))
drained_at=""
deadline=$((SECONDS + DRAIN_TARGET_S + 120))
while ((SECONDS < deadline)); do
  read -r lag pending processed <<<"$(queue_stats)"
  if [[ $lag == 0 && $pending == 0 && $processed -ge $target ]]; then
    drained_at=$(now)
    break
  fi
  sleep 1
done
[[ -n $drained_at ]] || die "queue not drained: lag=${lag} pending=${pending} processed=${processed}/${target}"
drain_seconds=$(minus "$drained_at" "$k6_start")
log "queue drained ${drain_seconds}s after the burst started (lag=0 pending=0 processed=$((processed - base_processed)))"

wait_for 300 "workers back to zero after the queue drained" worker_idle
zero_at=$(now)
zero_after_drain=$(minus "$zero_at" "$drained_at")
sleep 4
stop_samplers

t_w1=$(csv_first "${S2}-timeline.csv" 0 'v("worker_desired") != "" && v("worker_desired") >= 1')
t_w1_ready=$(csv_first "${S2}-timeline.csv" 0 'v("worker_ready") != "" && v("worker_ready") >= 1')
s2_peak_workers=$(csv_agg "${S2}-timeline.csv" worker_desired max)
t_wpeak=$(csv_first "${S2}-timeline.csv" 0 "v(\"worker_ready\") != \"\" && v(\"worker_ready\") >= ${s2_peak_workers}")
s2_peak_lag=$(csv_agg "${S2}-timeline.csv" lag max)
read -r w_cpu_avg w_cpu_max w_mem_max <<<"$(top_stat "${S2}-top.csv" "${WORKER}-" 0 1e9)"
duplicates=$((processed - target))
printf 't,label\n%s,burst start\n%s,burst sent\n%s,drained\n%s,zero\n' \
  "$ks" "$burst_end" "$(minus "$drained_at" "$T0")" "$(minus "$zero_at" "$T0")" >"${S2}-markers.csv"

[[ -n $t_w1 ]] || die "workers never scaled up from zero"
time_to_first_worker=$(rel "$t_w1" "$ks")
time_to_first_worker_ready=$(rel "$t_w1_ready" "$ks")
time_to_peak_workers=$(rel "$t_wpeak" "$ks")
awk -v d="$drain_seconds" -v t="$DRAIN_TARGET_S" 'BEGIN { exit !(d <= t) }' \
  || die "drain took ${drain_seconds}s, target ${DRAIN_TARGET_S}s"
awk -v d="$zero_after_drain" -v t="$ZERO_TARGET_S" 'BEGIN { exit !(d <= t) }' \
  || die "back to zero took ${zero_after_drain}s after drain, target ${ZERO_TARGET_S}s"
log "scenario 2: first worker ${time_to_first_worker}s, ${s2_peak_workers} workers at peak, drained ${drain_seconds}s, zero ${zero_after_drain}s after drain"

# --- Scenario 3: pod kill and node drain under load ----------------------
log "scenario 3: steady load while an API pod is deleted and a node is drained"
S3="${RESULTS_DIR}/s3"
T0=$(now)
start_samplers "$S3" "$T0"
k6_start=$(start_k6 steady steady.js) || die "k6 steady did not start"
ks=$(minus "$k6_start" "$T0")
sleep 30

victim=$(kubectl -n "$NS" get pods -l app.kubernetes.io/component=api --field-selector=status.phase=Running -o name | head -n1)
kubectl -n "$NS" delete "$victim" --wait=false >/dev/null
t_kill=$(minus "$(now)" "$T0")
log "deleted ${victim} (SIGTERM, graceful)"
sleep 30

node=$(kubectl -n "$NS" get pods -l app.kubernetes.io/component=api -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' \
  | sort | uniq -c | sort -rn | awk 'NR == 1 { print $2 }')
evicted=$(kubectl get pods -A --field-selector "spec.nodeName=${node}" -o json \
  | jq '[.items[] | select((.metadata.ownerReferences // [])[0].kind != "DaemonSet")] | length')
api_on_node=$(kubectl -n "$NS" get pods -l app.kubernetes.io/component=api --field-selector "spec.nodeName=${node}" -o name | wc -l)
t_drain=$(minus "$(now)" "$T0")
log "draining ${node} (${api_on_node} API pods, ${evicted} non-DaemonSet pods), PDB maxUnavailable=1"
drain_start=$SECONDS
kubectl drain "$node" --ignore-daemonsets --delete-emptydir-data --timeout=180s >"${S3}-drain.log" 2>&1 \
  || { cat "${S3}-drain.log" >&2; die "drain of ${node} failed"; }
drain_duration=$((SECONDS - drain_start))
t_drained=$(minus "$(now)" "$T0")
log "drained ${node} in ${drain_duration}s"
sleep 20
kubectl uncordon "$node" >/dev/null
t_uncordon=$(minus "$(now)" "$T0")
wait_k6 steady 400
s3_end=$(minus "$(now)" "$T0")
sleep 2
stop_samplers

s3_min_ready=$(csv_agg "${S3}-timeline.csv" api_ready min "$ks" "$s3_end")
s3_max_desired=$(csv_agg "${S3}-timeline.csv" api_desired max "$ks" "$s3_end")
printf 't,label\n%s,pod deleted\n%s,drain start\n%s,drain done\n%s,uncordon\n' \
  "$t_kill" "$t_drain" "$t_drained" "$t_uncordon" >"${S3}-markers.csv"

# --- Alert rules are loaded and evaluating ---------------------------------
rules_json=$(kubectl get --raw "/api/v1/namespaces/monitoring/services/prometheus-server:80/proxy/api/v1/rules")
rules_ok=$(jq '[.data.groups[].rules[] | select(.health == "ok")] | length' <<<"$rules_json")
rules_total=$(jq '[.data.groups[].rules[]] | length' <<<"$rules_json")
[[ $rules_total -ge 7 && $rules_ok == "$rules_total" ]] || die "alert rules not healthy: ${rules_ok}/${rules_total}"
ksm_max=$(prom_query "kube_horizontalpodautoscaler_spec_max_replicas{horizontalpodautoscaler=\"${HPA}\"}")
[[ $ksm_max == 10 ]] || die "kube-state-metrics HPA series missing (got '${ksm_max}')"
firing=$(kubectl get --raw "/api/v1/namespaces/monitoring/services/prometheus-server:80/proxy/api/v1/alerts" \
  | jq -r '[.data.alerts[] | "\(.labels.alertname)(\(.state))"] | join(" ")')

# --- Results ----------------------------------------------------------------
jq -n \
  --arg k8s "$(kubectl version -o json | jq -r .serverVersion.gitVersion)" \
  --arg keda "$(kubectl -n keda get deploy keda-operator -o jsonpath='{.spec.template.spec.containers[0].image}')" \
  --argjson nodes "$(kubectl get nodes -o name | wc -l)" \
  --arg slo "$SLO_P95_MS" \
  --slurpfile ramp "${RESULTS_DIR}/k6-ramp.json" \
  --slurpfile burst "${RESULTS_DIR}/k6-burst.json" \
  --slurpfile steady "${RESULTS_DIR}/k6-steady.json" \
  --arg s1_peak "$s1_peak" --arg tso "$time_to_scale_out" --arg tpr "$time_to_peak_ready" \
  --arg tsi "$time_to_scale_in" --arg tmin "$time_to_min" --arg rke "$replicas_at_k6_end" \
  --arg cpu "$s1_peak_cpu" --arg rpp "$s1_peak_rpp" --arg rps "$s1_peak_rps" \
  --arg acpu "$api_cpu_avg" --arg acpumax "$api_cpu_max" --arg amem "$api_mem_max" --arg spread "$zones_at_peak" \
  --arg n "$BURST_N" --arg tw1 "$time_to_first_worker" --arg tw1r "$time_to_first_worker_ready" \
  --arg twp "$time_to_peak_workers" --arg pw "$s2_peak_workers" --arg plag "$s2_peak_lag" \
  --arg drain "$drain_seconds" --arg zero "$zero_after_drain" --arg dup "$duplicates" \
  --arg wcpu "$w_cpu_avg" --arg wcpumax "$w_cpu_max" --arg wmem "$w_mem_max" \
  --arg victim "$victim" --arg node "$node" --arg evicted "$evicted" --arg apin "$api_on_node" \
  --arg dd "$drain_duration" --arg minr "$s3_min_ready" --arg maxd "$s3_max_desired" \
  --arg rules "${rules_ok}/${rules_total}" --arg firing "$firing" \
  'def num: tonumber? // null;
  {
    environment: {kubernetes: $k8s, keda_image: $keda, nodes: $nodes, slo_p95_ms: ($slo | num)},
    ramp: {k6: $ramp[0], peak_api_replicas: ($s1_peak | num), time_to_first_scale_out_s: ($tso | num),
           time_to_peak_replicas_ready_s: ($tpr | num), time_to_first_scale_in_s: ($tsi | num),
           time_to_min_replicas_s: ($tmin | num), replicas_when_load_stopped: ($rke | num),
           peak_hpa_cpu_utilization_pct: $cpu, peak_hpa_rps_per_pod: $rpp, peak_rps_prometheus: $rps,
           api_pod_cpu_m_avg_at_peak: $acpu, api_pod_cpu_m_max: $acpumax, api_pod_mem_mi_max: $amem,
           api_pods_per_node_at_peak: $spread},
    burst: {k6: $burst[0], jobs: ($n | num), time_to_first_worker_scheduled_s: ($tw1 | num),
            time_to_first_worker_ready_s: ($tw1r | num), time_to_peak_workers_ready_s: ($twp | num),
            peak_workers: ($pw | num), peak_lag: $plag, time_to_drain_s: ($drain | num),
            time_back_to_zero_after_drain_s: ($zero | num), duplicate_deliveries: ($dup | num),
            worker_pod_cpu_m_avg: $wcpu, worker_pod_cpu_m_max: $wcpumax, worker_pod_mem_mi_max: $wmem},
    disruption: {k6: $steady[0], deleted_pod: $victim, drained_node: $node, api_pods_on_node: ($apin | num),
                 pods_evicted: ($evicted | num), drain_duration_s: ($dd | num),
                 min_api_ready: $minr, max_api_desired: $maxd},
    alerts: {rules_healthy: $rules, active_at_end: $firing}
  }' >"${RESULTS_DIR}/results.json"
cat "${RESULTS_DIR}/results.json"

ms() { awk -v v="$1" 'BEGIN { printf "%.1f ms", v }'; }
{
  echo "## Event-driven autoscaling e2e results"
  echo
  echo "kind, $(jq -r .environment.nodes "${RESULTS_DIR}/results.json") nodes, $(jq -r .environment.kubernetes "${RESULTS_DIR}/results.json"), $(jq -r .environment.keda_image "${RESULTS_DIR}/results.json"). SLO: p95 < ${SLO_P95_MS} ms, 0 failed requests."
  echo
  echo "| Scenario | Measured |"
  echo "|---|---|"
  echo "| Ramp: requests / failed | $(k6v ramp requests) / $(k6v ramp failed) |"
  echo "| Ramp: p95 / p99 / max latency | $(ms "$(k6v ramp p95_ms)") / $(ms "$(k6v ramp p99_ms)") / $(ms "$(k6v ramp max_ms)") |"
  echo "| Ramp: API replicas | ${API_MIN} -> ${s1_peak} -> ${API_MIN} |"
  echo "| Ramp: first scale-out after ramp start | ${time_to_scale_out} s |"
  echo "| Ramp: all ${s1_peak} replicas ready after ramp start | ${time_to_peak_ready} s |"
  echo "| Ramp: first scale-in after load drop / back to ${API_MIN} | ${time_to_scale_in} s / ${time_to_min} s |"
  echo "| Ramp: peak HPA inputs | CPU ${s1_peak_cpu}% of request, ${s1_peak_rpp} req/s per pod |"
  echo "| Burst: jobs accepted / failed | $(k6v burst requests) / $(k6v burst failed) |"
  echo "| Burst: first worker scheduled / ready (from 0) | ${time_to_first_worker} s / ${time_to_first_worker_ready} s |"
  echo "| Burst: peak workers / peak lag | ${s2_peak_workers} / ${s2_peak_lag} |"
  echo "| Burst: queue drained (target ${DRAIN_TARGET_S} s) | ${drain_seconds} s |"
  echo "| Burst: back to 0 workers after drain (target ${ZERO_TARGET_S} s) | ${zero_after_drain} s |"
  echo "| Disruption: requests / failed | $(k6v steady requests) / $(k6v steady failed) |"
  echo "| Disruption: p95 latency | $(ms "$(k6v steady p95_ms)") |"
  echo "| Disruption: drain of ${node} (${api_on_node} API pods) | ${drain_duration} s, min ready API pods ${s3_min_ready} |"
  echo "| Alert rules healthy | ${rules_ok}/${rules_total} |"
} >"${RESULTS_DIR}/summary.md"
[[ -z ${GITHUB_STEP_SUMMARY:-} ]] || cat "${RESULTS_DIR}/summary.md" >>"$GITHUB_STEP_SUMMARY"

log "PASS: scale-out/in with 0 errors, scale 0 -> ${s2_peak_workers} -> 0 on queue lag, 0 errors through pod kill and node drain"

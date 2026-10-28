#!/usr/bin/env bash
# Installs the e2e platform into the current kube context and waits until the
# autoscaling pipeline is usable end to end:
#   metrics-server  (CPU trigger, kubectl top)
#   KEDA            (ScaledObjects, external metrics API)
#   Prometheus      (request-rate trigger, alert rules) + kube-state-metrics
# Versions are pinned here and listed in docs/versions.md.
set -euo pipefail

KEDA_CHART_VERSION="${KEDA_CHART_VERSION:-2.21.0}"
METRICS_SERVER_CHART_VERSION="${METRICS_SERVER_CHART_VERSION:-3.14.0}"
PROMETHEUS_CHART_VERSION="${PROMETHEUS_CHART_VERSION:-29.35.0}"

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

# Zones for the three app nodes (kind cannot set topology labels at creation).
zones=(zone-a zone-b zone-c)
i=0
for node in $(kubectl get nodes -l pool=app -o jsonpath='{.items[*].metadata.name}'); do
  kubectl label node "$node" "topology.kubernetes.io/zone=${zones[$i]}" --overwrite >/dev/null
  i=$((i + 1))
done
[[ $i -eq 3 ]] || { log "expected 3 app nodes, found $i"; exit 1; }
kubectl label node -l pool=infra topology.kubernetes.io/zone=zone-infra --overwrite >/dev/null
kubectl get nodes -L pool,topology.kubernetes.io/zone

helm repo add kedacore https://kedacore.github.io/charts --force-update >/dev/null
helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server --force-update >/dev/null
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update >/dev/null
helm repo update >/dev/null

log "installing metrics-server chart ${METRICS_SERVER_CHART_VERSION}"
helm upgrade --install metrics-server metrics-server/metrics-server \
  --version "$METRICS_SERVER_CHART_VERSION" --namespace kube-system \
  -f e2e/platform/metrics-server-values.yaml --wait --timeout 5m

log "installing KEDA chart ${KEDA_CHART_VERSION}"
helm upgrade --install keda kedacore/keda \
  --version "$KEDA_CHART_VERSION" --namespace keda --create-namespace \
  -f e2e/platform/keda-values.yaml --wait --timeout 5m

# The alert rules under test are the chart's own rule file.
rules_values=$(mktemp)
{
  echo "serverFiles:"
  echo "  alerting_rules.yml:"
  sed 's/^/    /' charts/event-autoscaling/files/alerts.yaml
} >"$rules_values"

log "installing Prometheus chart ${PROMETHEUS_CHART_VERSION}"
helm upgrade --install prometheus prometheus-community/prometheus \
  --version "$PROMETHEUS_CHART_VERSION" --namespace monitoring --create-namespace \
  -f e2e/platform/prometheus-values.yaml -f "$rules_values" --wait --timeout 5m
rm -f "$rules_values"

for crd in scaledobjects.keda.sh triggerauthentications.keda.sh; do
  kubectl wait --for=condition=Established "crd/${crd}" --timeout=120s
done
for api in v1beta1.metrics.k8s.io v1beta1.external.metrics.k8s.io; do
  kubectl wait --for=condition=Available "apiservice/${api}" --timeout=180s
done

# metrics-server needs a scrape cycle before `kubectl top` and the CPU trigger work.
for _ in $(seq 1 60); do
  if kubectl top nodes >/dev/null 2>&1; then
    log "metrics-server serving node metrics"
    break
  fi
  sleep 3
done
kubectl top nodes

for _ in $(seq 1 60); do
  if kubectl get --raw "/api/v1/namespaces/monitoring/services/prometheus-server:80/proxy/-/ready" >/dev/null 2>&1; then
    log "Prometheus ready"
    exit 0
  fi
  sleep 3
done
log "Prometheus did not become ready"
exit 1

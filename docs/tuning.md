# Tuning requests and autoscaling from measurements

The resource requests in `charts/event-autoscaling/values.yaml` come from the e2e measurements,
not from guesses. This page records how, so the next change can be measured the same way.

## How the numbers are collected

`e2e/run.sh` samples these during every scenario:

- every ~1.3 s (`s<N>-timeline.csv`): desired and ready replicas for the API and the worker; the
  HPA's current CPU utilization and req/s per pod (`status.currentMetrics`); total req/s from
  Prometheus; and queue lag, pending and processed counts read directly from Redis
- every 10 s (`s<N>-top.csv`): `kubectl top pods` CPU (millicores) and memory (MiB) per pod

`results.json` summarizes them: average and maximum per-pod CPU during the peak-hold window of the
ramp, maximum memory, and per-worker CPU and memory during the burst.

## Run 1: initial guesses ([run 40606549815](https://github.com/ahmedelgmal310/k8s-event-driven-autoscaling/actions/runs/40606549815))

Initial requests: API 100m CPU / 32Mi, worker 50m / 32Mi. With `workIterations: 3000`, at the peak
hold (300 req/s on 7 pods ≈ 43 req/s per pod):

| | Measured | Requested | Utilization |
|---|---|---|---|
| API CPU per pod | 29m average, 33m max | 100m | **peak HPA CPU 50%**, so the 70% CPU trigger never bound |
| API memory per pod | 11Mi max | 32Mi | 34% |
| Worker CPU per pod (4 concurrent jobs) | 12m | 50m | 24% |
| Worker memory per pod | 9Mi | 32Mi | 28% |

All the scaling came from the req/s trigger. At those requests the CPU trigger was dead
configuration, and each pod reserved 2–4× the capacity it used.

The same run showed a false positive in `AutoscalerMetricsUnavailable`. While KEDA held the worker
at zero, its HPA reported `ScalingActive=false` (ScalingDisabled). The rule now only counts HPAs
with running replicas, and a promtool test pins that case.

## Sizing rule applied

- **CPU request** = CPU at the req/s target ÷ a utilization that keeps CPU a live second signal.
  29m at ~43 req/s means ≈ 27m at the 40 req/s target, which is ~55% of a **50m** request. CPU
  (target 70%) now binds when requests become more expensive than today, or during the ramp,
  while per-pod load briefly runs above target.
- **Memory**: request ≈ 1.5× the measured peak (11Mi → **16Mi**), limit ≈ 4–6× (**64Mi**) for GC
  headroom.
- **Worker**: 12m → **25m** request (2× headroom for bursts of CPU-heavier jobs), 9Mi → **16Mi**,
  limit **64Mi**.
- **No CPU limits**: throttling would add tail latency for no scheduling benefit. The request
  drives scheduling and the utilization math.

## Run 2: after tuning ([run 40608705499](https://github.com/ahmedelgmal310/k8s-event-driven-autoscaling/actions/runs/40608705499))

| | Measured | Requested | Effect |
|---|---|---|---|
| API CPU per pod | 24m average, 29m max | 50m | **peak HPA CPU 83%**: both triggers now drive the ramp |
| API memory per pod | 11Mi max | 16Mi | 69% |
| Worker CPU / memory | 4m / 9Mi | 25m / 16Mi | |

The outcome stayed the same (7 replicas at peak, 0 failed requests, p95 0.70 ms) with half the
reserved CPU, and the CPU trigger now does real work. The per-pod CPU varies between runs on shared
GitHub runners (24m and 29m on the same load), which is why the request keeps headroom instead of
matching the average.

## Rules of thumb for the other knobs

| Knob | How to pick it | e2e | prod |
|---|---|---|---|
| `requestsPerSecondPerPod` | highest rate one pod sustains with p95 well under the SLO, ÷ 1.5 | 40 | 60 |
| `cpuUtilization` | 60–75%. Lower reacts earlier and costs more | 70 | 65 |
| `rateWindow` | ≥ 4 × the scrape interval. Shorter reacts faster but is noisier | 30s | 1m |
| `minReplicas` (API) | one per zone, plus whatever covers the scale-out delay of a sharp ramp | 2 | 3 |
| scale-up policy | aggressive: +100% per period, no stabilization | 15s | 30s |
| scale-down stabilization | long: scale-in is where you lose capacity you will need again | 30s | 600s |
| `lagPerReplica` | throughput per pod × the acceptable drain time | 50 | 100 |
| `cooldownPeriod` | longer than the usual gap between bursts, to avoid 0 ⇄ N thrash | 30s | 600s |
| worker `terminationGracePeriodSeconds` | > read block + concurrency × job duration | 30s | 30s |

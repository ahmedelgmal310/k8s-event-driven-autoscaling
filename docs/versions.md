# Pinned versions

Every tool, chart, image and action is pinned. Images use `tag@sha256:digest`
where the reference is under this repository's control, actions use a full
commit SHA with the version in a comment, Go modules are locked by `go.sum`.
A version is adopted only after it has been released for at least two weeks;
Dependabot proposes updates weekly with a 14-day cooldown.

Snapshot taken on 2026-10-28. Where each pin lives: CI tools and actions in
`.github/workflows/ci.yml`, platform charts in `e2e/setup.sh`, build images in
`Dockerfile`, Redis in `charts/event-autoscaling/values.yaml`, k6 in
`e2e/k6/job.yaml`, Go modules in `go.mod`.

## Cluster and platform (e2e)

| Component | Version | Released | Pin |
|---|---|---|---|
| kind | v0.33.0 | 2026-08-26 | `helm/kind-action` input |
| Kubernetes (kindest/node) | v1.36.4 | 2026-08-20 | `sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed` |
| KEDA (kedacore/keda chart) | 2.21.0 | 2026-09-23 | chart version |
| metrics-server chart (app v0.9.0) | 3.14.0 | 2026-08-19 | chart version |
| prometheus-community/prometheus chart (Prometheus v3.x, kube-state-metrics subchart) | 29.35.0 | 2026-09-27 | chart version |
| Redis | 8.6.6-alpine | 2026-09-21 | `sha256:23a604374ece8069b34131faac7a1f05ae73c64280e46af1de844f29af2846d4` |
| k6 | 2.3.0 | 2026-09-21 | `sha256:2dd4756ec6e14e62778916e1eb850108e4b499304c02df56760fa0bdb0fba0b8` |

## Service images (`Dockerfile`)

| Stage | Image | Pin |
|---|---|---|
| build | golang 1.27.3-alpine3.24 | `sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb` |
| runtime | gcr.io/distroless/static-debian13:nonroot | `sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99` |

## Go (`go.mod`)

| Module | Version | Released |
|---|---|---|
| Go toolchain | 1.27.3 | |
| github.com/redis/go-redis/v9 | v9.23.0 | 2026-10-05 |
| github.com/prometheus/client_golang | v1.25.0 | 2026-10-08 |
| github.com/alicebob/miniredis/v2 (tests only) | v2.40.0 | 2026-10-10 |

## CI (`.github/workflows/ci.yml`)

| Tool | Version | Pin |
|---|---|---|
| actions/checkout | v7.0.1 | `3d3c42e5aac5ba805825da76410c181273ba90b1` |
| actions/setup-go | v7.0.0 | `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e` |
| actions/upload-artifact | v7.0.2 | `cf430e030ddbb5b0abf93d22962f4752f3646cd9` |
| golangci/golangci-lint-action | v9.3.0 (golangci-lint v2.15.0) | `ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a` |
| azure/setup-helm | v5.0.1 (Helm v4.3.1) | `9bc31f4ebc9c6b171d7bfbaa5d006ae7abdb4310` |
| helm/kind-action | v1.15.1 | `1544676c07bb3570fcb70166beaa07c4528fd4aa` |
| docker/setup-buildx-action | v4.4.1 | `f87e5991a6d7451dcb8d9637bfbc97413f497069` |
| docker/build-push-action | v7.4.0 | `c3c9e263c25d99ce0380d002d59b67737d91b0dc` |
| helm-unittest | v1.2.1 | plugin version |
| kubeconform | v0.8.0 | `sha256:9bc2bffbf71f261128533edaf912153948b7ff238f9a531ae6d34466ec287883` (tarball) |
| datreeio/CRDs-catalog (ScaledObject, PrometheusRule schemas) | commit | `3eb90d47aba8d61919d4419dd38301536e32a94b` |
| govulncheck | v1.9.0 | module version |
| promtool (prom/prometheus) | v3.15.0 | `sha256:efd719c99d83b060d9daefdcf00360461adf279f45ef5391f8d111892118753e` |
| Trivy | 0.76.1 | `sha256:790fd66d1697395d8693d1acaa1ea702406a20f109b6fc7bac8db623eca6c06c` |
| Gitleaks | v8.30.1 | `sha256:c00b6bd0aeb3071cbcb79009cb16a60dd9e0a7c60e2be9ab65d25e6bc8abbb7f` |
| hadolint | v2.15.1 | `sha256:32dac94127fd60b7b7e3fbfc65e1383b9b5e25c9bfd7b8536de7a539fe68a12d` |
| actionlint | 1.7.12 | `sha256:b1934ee5f1c509618f2508e6eb47ee0d3520686341fec936f3b79331f9315667` |
| shellcheck | runner image | preinstalled on `ubuntu-latest` |

Charts installed by `e2e/setup.sh` pull their own images by tag (KEDA,
metrics-server, Prometheus, kube-state-metrics); those tags are fixed by the
pinned chart versions.

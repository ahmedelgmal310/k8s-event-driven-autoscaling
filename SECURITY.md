# Security Policy

## Supported versions

Only the latest commit on `main` receives fixes.

## Reporting a vulnerability

Please report vulnerabilities privately through
[GitHub Security Advisories](https://github.com/ahmedelgmal310/k8s-event-driven-autoscaling/security/advisories/new).
Do not open a public issue. You can expect an acknowledgement within 72 hours.

## Controls in this repository

- Every GitHub Action is pinned to a full commit SHA; base, tool and runtime images are pinned by digest
  (see [docs/versions.md](docs/versions.md)). Workflows default to `contents: read`.
- The service image is distroless, runs as UID 65532 with a read-only root filesystem, no capabilities,
  a `RuntimeDefault` seccomp profile and no service account token.
- NetworkPolicies: the API accepts traffic only from allowed namespaces; Redis accepts connections only
  from the API, the workers and the KEDA operator; egress is limited to DNS and Redis.
- CI gates: `govulncheck`, Trivy (fixable HIGH/CRITICAL fail the build), Gitleaks over the full history,
  hadolint, golangci-lint (gosec included), shellcheck, actionlint.
- The in-chart Redis has no authentication and no TLS. It is meant for dev and CI only; production
  values point at a managed Redis (`redis.enabled: false`). See the README's Limitations section.

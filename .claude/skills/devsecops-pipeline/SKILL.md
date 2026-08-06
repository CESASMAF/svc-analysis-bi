---
name: devsecops-pipeline
description: >
  DevSecOps for Go infrastructure. Covers Docker, CI/CD (GitHub Actions),
  Go modules dependency security, secrets management, and supply chain.
  Use when auditing infrastructure, Dockerfile, CI/CD, or dependencies.
user_invocable: true
---

# DevSecOps Pipeline -- Go

## 6 Pillars

### 1. Go Module Dependency Security
- `go.sum` committed (integrity verification)
- `go mod verify` passes
- `govulncheck ./...` clean (no known vulnerabilities)
- Dependencies from trusted publishers
- Dependabot configured for gomod ecosystem
- Regular `go get -u` for patch updates

### 2. Docker Security

**This service runs on `FROM scratch`, not distroless.** Audit against the shape
below — it is the project's actual `Dockerfile`, and it is the intended design.

```dockerfile
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/svc-analysis-bi ./cmd/server/

FROM scratch
LABEL org.opencontainers.image.source="..."
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /bin/svc-analysis-bi /bin/svc-analysis-bi
EXPOSE 8080
ENTRYPOINT ["/bin/svc-analysis-bi"]
```

**Three things you must NOT flag as findings here** — `scratch` makes each one
either impossible or unnecessary, and reporting them argues against the more
secure choice:

| Not a finding | Why |
|---|---|
| No `USER nonroot:nonroot` | `scratch` has no `/etc/passwd`, so there is no user to switch to. The runtime enforces non-root (`runAsNonRoot` / `--user`), not the image. |
| No `HEALTHCHECK` | `HEALTHCHECK` needs a shell or a probe binary; `scratch` has neither. Liveness/readiness come from `GET /health` and `/ready` at the orchestrator. |
| No `COPY configs/ibge_mesoregions.csv` | The CSV is compiled into the binary via `go:embed` (`configs/embed.go`). Copying it would be dead weight; `GEO_CSV_PATH` overrides at runtime if ever needed. |

Checklist:
- [ ] Base image pinned (not `:latest`) — builder is `golang:1.25-alpine`
- [ ] Minimal runtime — `FROM scratch` (no shell, no package manager, no user db)
- [ ] Multi-stage build (builder + minimal runtime)
- [ ] `CGO_ENABLED=0` — **mandatory**, not conditional: a cgo-linked binary will
      not run on `scratch`. This is what rules out CGo for the DBC encoder.
- [ ] CA certificates copied from the builder (needed for outbound TLS)
- [ ] No secrets in ENV/ARG
- [ ] Binary stripped (`-ldflags="-s -w"`)
- [ ] **GAP:** no `.dockerignore` in the repo — `COPY . .` ships `.git/` and
      `*_test.go` into the build context. This one IS a real finding.

### 3. CI/CD Pipeline (GitHub Actions)
- [ ] Actions pinned by SHA (not tag)
- [ ] `go vet ./...` before build
- [ ] `govulncheck ./...` in pipeline
- [ ] `go test -race -cover` runs BEFORE image push
- [ ] Security scanning step (Trivy container scan)
- [ ] Least privilege permissions on jobs
- [ ] Secrets in GitHub Secrets only

### 4. Secrets Management
- [ ] No secrets in source code (grep for patterns)
- [ ] No fallback credentials compiled into binary
- [ ] `PATIENT_HASH_SALT` treated as secret (never hardcoded)
- [ ] `.env` in `.gitignore`
- [ ] Pre-commit hooks (gitleaks)
- [ ] Different secrets per environment (dev/stg/prod)
- [ ] Bitwarden Secret Manager for prod secrets

### 5. Supply Chain
- [ ] SBOM generation (syft or cyclonedx-gomod)
- [ ] Container image signing (cosign/Sigstore)
- [ ] Immutable image tags (`sha-<commit>`, `vX.Y.Z`)
- [ ] `:latest` only on main, production uses digest
- [ ] `go.sum` provides checksum verification

### 6. Monitoring & Alerting
- [ ] Structured logging with `slog` (not `fmt.Println` or `log`)
- [ ] Health/readiness probes (K8s compatible)
- [ ] Graceful shutdown handler (SIGTERM/SIGINT)
- [ ] NATS consumer monitoring (failed event delivery, DLQ size)
- [ ] Export generation monitoring (timeouts, errors)

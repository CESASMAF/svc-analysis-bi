---
name: auth-session-security
description: >
  Identity & Access Management expert for Go/chi with Authentik OIDC.
  Covers JWT verification, API key validation, NATS authentication, audit trail.
  Use when auditing or implementing authentication/authorization.
user_invocable: true
---

# Auth & Session Security -- Go / chi + Authentik OIDC

> ## Este arquivo e o UNICO que nomeia o IdP
>
> Os agents de security (`auth-auditor`, `pentest-scanner`, `threat-analyst`,
> `security-orchestrator`) referenciam esta skill e **nunca repetem o nome do
> produto**. A regra existe porque a alternativa ja falhou: os quatro passaram
> meses dizendo "Zitadel" enquanto o codigo estava em Authentik, e o
> `auth-auditor` chegava a mandar ler esta skill na primeira linha e a
> contradizer na propria description.
>
> **Estado verificado (2026-08-06):** Authentik. Ancoras no codigo --
> `internal/api/middleware/jwks_validator.go` (RS256-only + JWKS),
> `role_guard.go` (claim `groups` no formato `<system>:<role>`, mais
> `superadmin`), e `.env.example` (`JWKS_URL`, `AUTH_ISSUER` apontando
> `/application/o/<slug>/`).
>
> **O workspace esta migrando para Ory** (Kratos + Hydra + Cerbos): `infra` e
> `app-conecta-web` ja estao la, `svc-people-context` migrou o provisionamento.
> Este servico ainda **nao**. Quando migrar, os dois pontos de quebra sao o
> RS256-only + JWKS de `jwks_validator.go` e o formato da claim `groups` em
> `role_guard.go` -- e este arquivo e o unico que precisa ser reescrito.

## JWT Verification

### Required Claims
```go
type Claims struct {
    Subject  string   `json:"sub"`
    Issuer   string   `json:"iss"`
    Audience []string `json:"aud"`
    Expires  int64    `json:"exp"`
    NotBefore int64   `json:"nbf,omitempty"`
    // Authentik delivers roles as groups "<system>:<role>" (e.g.
    // "analysis-bi:analyst") + the global "superadmin" in the `groups` claim.
    Groups   []string `json:"groups,omitempty"`
}

func (c *Claims) Validate(expectedIssuer, expectedAudience string) error {
    if time.Now().Unix() > c.Expires {
        return ErrTokenExpired
    }
    if c.Issuer != expectedIssuer {
        return ErrInvalidIssuer
    }
    if !containsAudience(c.Audience, expectedAudience) {
        return ErrInvalidAudience
    }
    return nil
}
```

### Authentik Configuration
- **Issuer**: `<AUTHENTIK_URL>/application/o/<slug>/` (e.g. `https://auth.acdg-bv.org.br/application/o/<slug>/`)
- **JWKS**: `<AUTHENTIK_URL>/application/o/<slug>/jwks/`
- **Signing alg**: RS256 (the validator is RS256-only)
- **Roles claim**: `groups` — values are `<system>:<role>` (e.g. `analysis-bi:analyst`) plus the global `superadmin`
- **RBAC is system-scoped**: only `analysis-bi:*` (and bare `admin`/`superadmin`) grant access here; a foreign `social-care:admin` does not (see `RoleGuard`).

### JWT Middleware Pattern
```go
func JWTAuth(jwksURL, issuer, audience string) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            token := extractBearerToken(r)
            if token == "" {
                http.Error(w, "unauthorized", http.StatusUnauthorized)
                return
            }

            claims, err := verifyJWT(token, jwksURL, issuer, audience)
            if err != nil {
                http.Error(w, "unauthorized", http.StatusUnauthorized)
                return
            }

            ctx := context.WithValue(r.Context(), claimsKey, claims)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}
```

## API Key Authentication (for programmatic access)
- API keys for automated consumers (research tools, data pipelines)
- Keys stored hashed (SHA-256), never plaintext
- Constant-time comparison to prevent timing attacks
- Scoped to read-only operations (indicators + export)
- Rate-limited per key

## Route Protection
- `/health`, `/ready` -- public (no auth)
- `/api/v1/indicators/*` -- JWT or API key required
- `/api/v1/export/*` -- JWT or API key required
- `/api/v1/metadata/*` -- JWT or API key required (or public, depending on sensitivity)

## Infrastructure Auth
- PostgreSQL: TLS required (`sslmode=require` in connection string)
- NATS: nkey or token authentication + TLS
- No fallback credentials in code -- fail-fast if env vars missing
- `PATIENT_HASH_SALT` is a secret -- treat as credential

## NATS Consumer Auth
- Dedicated credentials for the consumer (not shared with API)
- Events validated against expected schema before processing
- Consumer uses durable subscription (at-least-once delivery)
- No unauthenticated event injection possible (NATS server enforces auth)

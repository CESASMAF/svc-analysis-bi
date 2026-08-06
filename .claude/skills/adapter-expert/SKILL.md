---
name: adapter-expert
description: >
  Expert skill for implementing the infrastructure layer: chi router, pgx repositories,
  NATS consumer adapter, export encoders, middleware (JWT, rate limiting, security headers),
  database migrations, and configuration.
  Use when the user mentions: handler, route, middleware, repository, migration,
  pgx, chi, NATS, export, encoder, FHIR, DTO.
user_invocable: true
---

# Adapter Expert -- Go / chi / pgx / NATS

You are the infrastructure layer specialist. This layer connects domain and ingestion to the real world.

## Architecture

```
internal/api/
  router.go         -- chi.NewRouter(), middleware chain, route groups
  ports.go          -- interfaces the API layer depends on
  response.go       -- standard response envelope
  middleware/
    jwt_auth.go        -- JWT verification middleware
    jwks_validator.go  -- JWKS fetch/cache, RS256-only validation
    role_guard.go      -- RBAC on the `groups` claim (<system>:<role>)
    rate_limit.go      -- per-IP and per-key rate limiting
    security_headers.go -- security headers middleware
    recovery.go        -- panic recovery (the one place panics are caught)
    response.go        -- middleware-level error responses
  handlers/
    indicators.go   -- GET /api/v1/indicators/*
    export.go       -- GET /api/v1/export/{format}
    metadata.go     -- GET /api/v1/metadata/*
    health.go       -- GET /health, GET /ready
    response.go     -- handler-level response helpers

internal/store/
  store.go          -- pgx pool setup, connection config
  schema.go         -- star-schema DDL
  migrations.go     -- forward-only migrations (a FILE, not a directory)
  dimensions.go     -- dimension table operations
  facts.go          -- fact table operations (upsert, insert)
  indicators.go     -- aggregation queries for indicators
  views.go          -- SQL views
  carryforward.go   -- monthly carry-forward job
  event_id.go       -- event identity/dedup helpers
  event_store.go    -- event_processing_log, event_dlq

internal/export/          -- ALL encoders are stdlib; no external libs (see
  encoder.go              -- export-pipeline-expert). Do not `go get` here.
  csv.go            -- CSV encoder
  json_encoder.go   -- JSON encoder
  xml.go            -- XML encoder
  parquet.go        -- Parquet encoder (hand-rolled: bytes + encoding/binary)
  dbf.go            -- DBF encoder (hand-rolled: dBASE III byte layout)
  dbc.go            -- DBC encoder (DBF + compress/flate deflate)
  ods.go            -- ODS encoder (archive/zip + XML by hand)
  fhir_encoder.go   -- FHIR Bundle encoder entrypoint
  fhir/
    bundle.go       -- FHIR Bundle (type=collection)
    patient.go      -- BRCorePatient (anonymized)
    observation.go  -- BRCoreObservation (indicators)
    condition.go    -- BRCoreCondition (diagnoses)
    encounter.go    -- BRCoreEncounter (appointments)

configs/
  config.go         -- environment variable loading, fail-fast
```

## Handler Pattern
```go
package handlers

import (
    "encoding/json"
    "net/http"

    "github.com/go-chi/chi/v5"
)

type IndicatorHandler struct {
    store IndicatorQuerier
}

func NewIndicatorHandler(store IndicatorQuerier) *IndicatorHandler {
    return &IndicatorHandler{store: store}
}

func (h *IndicatorHandler) GetDemographics(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    params, err := parseIndicatorParams(r)
    if err != nil {
        writeError(w, http.StatusBadRequest, err.Error())
        return
    }

    indicators, meta, err := h.store.QueryDemographics(ctx, params)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "internal error")
        return
    }

    writeJSON(w, http.StatusOK, StandardResponse{
        Data: indicators,
        Meta: meta,
    })
}
```

## Response Envelope
```go
type StandardResponse struct {
    Data interface{}  `json:"data"`
    Meta ResponseMeta `json:"meta"`
}

type ResponseMeta struct {
    Timestamp       string `json:"timestamp"`
    Period          string `json:"period,omitempty"`
    KThreshold      int    `json:"k_threshold"`
    SuppressedGroups int   `json:"suppressed_groups"`
    TotalRecords    int    `json:"total_records"`
}
```

## Store Pattern (pgx)
```go
package store

import (
    "context"
    "fmt"

    "github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
    pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
    return &PostgresStore{pool: pool}
}

func (s *PostgresStore) UpsertSnapshot(ctx context.Context, snapshot domain.PatientSnapshot) error {
    _, err := s.pool.Exec(ctx, `
        INSERT INTO fact_patient_snapshot (period_id, age_band_id, sex_id, geography_id, patient_hash, ...)
        VALUES ($1, $2, $3, $4, $5, ...)
        ON CONFLICT (period_id, patient_hash) DO UPDATE SET ...
    `, snapshot.PeriodID, snapshot.AgeBandID, snapshot.SexID, snapshot.GeographyID, snapshot.PatientHash)
    if err != nil {
        return fmt.Errorf("upsert snapshot: %w", err)
    }
    return nil
}
```

## Export Encoder Interface
```go
package export

import (
    "context"
    "io"
)

type Encoder interface {
    Encode(ctx context.Context, data ExportData, w io.Writer) error
    ContentType() string
    FileExtension() string
}

type ExportData struct {
    Dataset    string
    Period     string
    Rows       []map[string]interface{}
    Columns    []string
    Meta       ExportMeta
}
```

## Middleware Chain (order matters)
```go
r := chi.NewRouter()
r.Use(middleware.Recoverer)           // Panic recovery
r.Use(middleware.RequestID)           // Request ID
r.Use(middleware.RealIP)              // Real IP extraction
r.Use(customMiddleware.Logger)        // Structured logging
r.Use(customMiddleware.SecurityHeaders) // HSTS, nosniff, DENY
r.Use(customMiddleware.RateLimit)     // Per-IP rate limiting
r.Use(customMiddleware.CORS)          // Restrictive CORS

// Public routes
r.Get("/health", healthHandler.Liveness)
r.Get("/ready", healthHandler.Readiness)

// Protected routes
r.Group(func(r chi.Router) {
    r.Use(customMiddleware.JWTAuth)
    r.Get("/api/v1/indicators/demographics", indicatorHandler.GetDemographics)
    r.Get("/api/v1/export/{format}", exportHandler.Export)
    // ...
})
```

## Security Rules
- pgx parameterized queries -- NEVER `fmt.Sprintf` for SQL
- `$1`, `$2` placeholders for all query parameters
- JWT must verify `exp`, `iss`, `aud`
- K-anonymity filter on ALL indicator query results
- Export encoders must not embed PII in file metadata
- Responses via `StandardResponse` with `meta.k_threshold` and `meta.suppressed_groups`
- Error responses: safe message to client, full context to structured log
- Health/ready endpoints are public; all indicator/export endpoints require auth

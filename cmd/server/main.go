package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/acdgbrasil/svc-analysis-bi/configs"
	"github.com/acdgbrasil/svc-analysis-bi/internal/api"
	"github.com/acdgbrasil/svc-analysis-bi/internal/api/middleware"
	"github.com/acdgbrasil/svc-analysis-bi/internal/domain"
	"github.com/acdgbrasil/svc-analysis-bi/internal/export"
	"github.com/acdgbrasil/svc-analysis-bi/internal/ingestion"
	"github.com/acdgbrasil/svc-analysis-bi/internal/store"
	"github.com/nats-io/nats.go"
)

func main() {
	os.Exit(run())
}

func run() int {
	// Structured logger
	logLevel := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))

	// Load configuration
	cfg, err := configs.Load()
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		return 1
	}

	if err := cfg.Validate(); err != nil {
		logger.Error("invalid configuration", "error", err)
		return 1
	}

	// Context with graceful shutdown on SIGINT/SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Load geography CSV lookup. O CSV é embedado no binário (go:embed) — funciona
	// na imagem `scratch`, sem filesystem. GEO_CSV_PATH sobrescreve com um arquivo.
	var geoLookup domain.GeographyLookup
	var csvLookup *domain.CSVGeographyLookup
	if geoCSVPath := os.Getenv("GEO_CSV_PATH"); geoCSVPath != "" {
		csvLookup, err = domain.NewCSVGeographyLookup(geoCSVPath)
	} else {
		csvLookup, err = domain.NewCSVGeographyLookupFromReader(strings.NewReader(configs.IBGEMesoregionsCSV))
	}
	if err != nil {
		logger.Warn("geography CSV not loaded, CEP resolution will return errors", "error", err)
		geoLookup = &errGeographyLookup{}
	} else {
		geoLookup = csvLookup
		logger.Info("geography CSV loaded successfully")
	}

	// Connect to PostgreSQL
	db, err := store.New(ctx, cfg.Database.DSN(), cfg.Database.MaxConns,
		store.WithIdleTimeout(cfg.Database.IdleTimeout))
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		return 1
	}
	defer db.Close()
	logger.Info("database connected", "host", cfg.Database.Host, "port", cfg.Database.Port)

	// Run migrations
	if err := store.RunMigrations(ctx, db, store.AllMigrations()); err != nil {
		logger.Error("migration failed", "error", err)
		return 1
	}
	logger.Info("database migrations applied")

	// Create stores
	factStore := store.NewFactStore(db.Pool())
	eventStore := store.NewEventStore(db.Pool())
	indicatorStore := store.NewIndicatorStore(db.Pool())

	// Export encoder registry
	encoders := export.NewRegistry()

	// NATS consumer (optional — server starts without it for dev/testing)
	var natsChecker api.NATSChecker
	pipelineErr := make(chan error, 1)
	if cfg.NATS.URL != "" {
		nc, natsErr := nats.Connect(cfg.NATS.URL,
			nats.MaxReconnects(-1),
			nats.ReconnectWait(2*time.Second),
			nats.Name("svc-analysis-bi"),
		)
		if natsErr != nil {
			logger.Warn("NATS connection failed, ingestion pipeline disabled", "error", natsErr, "url", cfg.NATS.URL)
		} else {
			defer nc.Close()
			natsChecker = ingestion.NewNATSHealthChecker(nc)

			// Consumer reuses the same NATS connection (no duplicate)
			consumer := ingestion.NewNATSConsumer(nc, ingestion.NATSConsumerConfig{
				URL:          cfg.NATS.URL,
				StreamName:   cfg.NATS.Stream,
				ConsumerName: cfg.NATS.Consumer,
			})

			registry := ingestion.NewEventHandlerRegistry(geoLookup, cfg.PatientHashSalt)

			pipeline := ingestion.NewPipeline(
				ingestion.PipelineConfig{
					RawBufferSize:        100,
					AnonymizedBufferSize: 100,
					AnonymizeWorkers:     4,
					MaterializeWorkers:   2,
				},
				consumer,
				registry,
				factStore,
				eventStore,
				ingestion.WithLogger(&slogAdapter{logger: logger}),
			)

			// Start pipeline — propagate fatal errors to trigger shutdown
			go func() {
				if pipeErr := pipeline.Run(ctx); pipeErr != nil && pipeErr != context.Canceled {
					logger.Error("ingestion pipeline fatal error", "error", pipeErr)
					pipelineErr <- pipeErr
				}
			}()

			logger.Info("ingestion pipeline started",
				"nats_url", cfg.NATS.URL,
				"stream", cfg.NATS.Stream,
				"consumer", cfg.NATS.Consumer)
		}
	} else {
		logger.Warn("NATS not configured, ingestion pipeline disabled")
	}

	// Wire JWT validator
	var jwtValidator middleware.JWTValidator
	jwksURL := strings.TrimSpace(cfg.Auth.JWKSUrl)
	if cfg.Auth.AuthRequired && jwksURL == "" {
		logger.Error("AUTH_REQUIRED is true but JWKS_URL is not configured — refusing to start without authentication")
		return 1
	}
	if cfg.Auth.AuthRequired && jwksURL != "" {
		var jwksOpts []middleware.JWKSValidatorOption
		if iss := strings.TrimSpace(cfg.Auth.ExpectedIssuer); iss != "" {
			jwksOpts = append(jwksOpts, middleware.WithIssuer(iss))
		}
		if aud := strings.TrimSpace(cfg.Auth.ExpectedAudience); aud != "" {
			jwksOpts = append(jwksOpts, middleware.WithAudience(aud))
		}
		jwtValidator = middleware.NewJWKSValidator(jwksURL, jwksOpts...)
		logger.Info("JWT validation enabled", "jwks_url", jwksURL)
	} else if !cfg.Auth.AuthRequired {
		logger.Warn("JWKS_URL not configured, running in dev mode without authentication")
	}

	// Start carry-forward scheduler. Uses UTC and tracks the last period
	// processed to be idempotent and resilient to server restarts. On every
	// tick, it checks if the current UTC month has been carried forward yet.
	carryForward := store.NewCarryForwardJob(db.Pool())
	go func() {
		var lastRunPeriod string
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now().UTC()
				currentPeriod := domain.PeriodFromTime(now)
				periodKey := currentPeriod.YearMonth()

				// Skip if already ran for this period (idempotent)
				if periodKey == lastRunPeriod {
					continue
				}

				count, err := carryForward.Run(ctx, currentPeriod)
				if err != nil {
					logger.Error("carry-forward failed", "error", err, "period", periodKey)
					continue // retry on next tick
				}
				logger.Info("carry-forward completed", "period", periodKey, "rows", count)
				lastRunPeriod = periodKey

				if err := store.RefreshMaterializedViews(ctx, db.Pool()); err != nil {
					logger.Error("materialized view refresh failed", "error", err)
				} else {
					logger.Info("materialized views refreshed")
				}
			}
		}
	}()

	// Wire HTTP router
	router := api.NewRouter(api.RouterDeps{
		Logger:         logger,
		DB:             db,
		NATS:           natsChecker,
		JWTValidator:   jwtValidator,
		RateLimitRPS:   10,
		RateLimitBurst: 20,
		Indicators:     indicatorStore,
		Encoders:       encoders,
	})

	// HTTP server
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Start server in background
	srvErr := make(chan error, 1)
	go func() {
		logger.Info("HTTP server starting", "addr", addr)
		srvErr <- srv.ListenAndServe()
	}()

	// Wait for shutdown signal, server error, or pipeline fatal error
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-srvErr:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error", "error", err)
			return 1
		}
	case err := <-pipelineErr:
		logger.Error("ingestion pipeline failed, initiating shutdown", "error", err)
	}

	// Graceful shutdown with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown error", "error", err)
		return 1
	}

	logger.Info("server stopped gracefully")
	return 0
}

// errGeographyLookup is a fallback that always returns ErrCEPNotFound.
// Used when the CSV file cannot be loaded.
type errGeographyLookup struct{}

func (e *errGeographyLookup) FindByCEP(_ string) (domain.Geography, error) {
	return domain.Geography{}, domain.ErrCEPNotFound
}

// slogAdapter adapts *slog.Logger to the ingestion.Logger interface.
type slogAdapter struct {
	logger *slog.Logger
}

func (s *slogAdapter) Warn(msg string, keysAndValues ...any) {
	s.logger.Warn(msg, keysAndValues...)
}

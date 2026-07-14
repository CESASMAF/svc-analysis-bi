package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/acdgbrasil/svc-analysis-bi/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

// pipeline implements the Pipeline interface, orchestrating the flow from
// raw NATS messages through dedup, handler dispatch, materialization, and ack.
// It uses goroutines + channels for stage processing as per ADR-001.
type pipeline struct {
	cfg        PipelineConfig
	consumer   Consumer
	registry   EventHandlerRegistry
	factStore  FactStore
	eventStore EventProcessingStore
	logger     Logger
}

// NewPipeline creates a Pipeline that wires the Consumer, EventHandlerRegistry,
// FactStore, and EventProcessingStore into a goroutine + channel pipeline.
func NewPipeline(
	cfg PipelineConfig,
	consumer Consumer,
	registry EventHandlerRegistry,
	factStore FactStore,
	eventStore EventProcessingStore,
	opts ...PipelineOption,
) Pipeline {
	p := &pipeline{
		cfg:        cfg,
		consumer:   consumer,
		registry:   registry,
		factStore:  factStore,
		eventStore: eventStore,
		logger:     nopLogger{},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// PipelineOption configures optional pipeline dependencies.
type PipelineOption func(*pipeline)

// WithLogger sets a logger for pipeline observability.
func WithLogger(l Logger) PipelineOption {
	return func(p *pipeline) {
		if l != nil {
			p.logger = l
		}
	}
}

// Run starts the pipeline and blocks until ctx is cancelled or a fatal error
// occurs. It spawns consumer, anonymizer workers, and materializer workers
// connected by buffered channels.
func (p *pipeline) Run(ctx context.Context) error {
	rawBufSize := p.cfg.RawBufferSize
	if rawBufSize <= 0 {
		rawBufSize = 10
	}
	anonBufSize := p.cfg.AnonymizedBufferSize
	if anonBufSize <= 0 {
		anonBufSize = 10
	}
	anonWorkers := p.cfg.AnonymizeWorkers
	if anonWorkers <= 0 {
		anonWorkers = 1
	}
	matWorkers := p.cfg.MaterializeWorkers
	if matWorkers <= 0 {
		matWorkers = 1
	}

	rawCh := make(chan RawMessage, rawBufSize)
	anonCh := make(chan materializeJob, anonBufSize)

	// Spawn consumer goroutine
	consumerErr := make(chan error, 1)
	go func() {
		defer close(rawCh)
		consumerErr <- p.consumer.Subscribe(ctx, rawCh)
	}()

	// Spawn anonymize workers: read from rawCh, write to anonCh
	var anonWg sync.WaitGroup
	anonWg.Add(anonWorkers)
	for i := 0; i < anonWorkers; i++ {
		go func() {
			defer anonWg.Done()
			for msg := range rawCh {
				p.anonymizeStage(ctx, msg, anonCh)
			}
		}()
	}

	// Close anonCh when all anonymize workers are done
	go func() {
		anonWg.Wait()
		close(anonCh)
	}()

	// Spawn materialize workers: read from anonCh
	var matWg sync.WaitGroup
	matWg.Add(matWorkers)
	for i := 0; i < matWorkers; i++ {
		go func() {
			defer matWg.Done()
			for job := range anonCh {
				p.materializeStage(ctx, job)
			}
		}()
	}

	// Wait for all materialize workers to finish
	matWg.Wait()

	// Check consumer error
	select {
	case err := <-consumerErr:
		if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
			return fmt.Errorf("%w: %v", ErrConsumerConnectionFailed, err)
		}
	default:
	}

	return nil
}

// materializeJob carries an anonymized record and the original message's ack
// function through to the materialization stage.
type materializeJob struct {
	record AnonymizedRecord
	ack    AckFunc
}

// anonymizeStage processes a single raw message: dedup, handler dispatch,
// and if successful, sends the result to the materialize channel.
func (p *pipeline) anonymizeStage(ctx context.Context, msg RawMessage, out chan<- materializeJob) {
	// 1. Extract eventID from JSON metadata (best effort)
	eventID, _ := extractEventID(msg.Data)

	// 2. Look up handler by subject
	handler, handlerOK := p.registry[domain.EventType(msg.Subject)]
	if !handlerOK {
		// Unknown event type -> send sanitized metadata to DLQ, then ack
		eventType := msg.Subject
		if eventID == "" {
			eventID = "unknown"
		}
		dlqPayload := sanitizeForDLQ(msg.Data)
		if err := p.eventStore.SendToDLQ(ctx, eventID, eventType, dlqPayload, ErrUnknownEventType.Error()); err != nil {
			p.logger.Warn("failed to send unknown event to DLQ, skipping ack to force redelivery", "eventId", eventID, "error", err)
			return // don't ack — DLQ failed, let NATS redeliver
		}
		if err := msg.Ack(); err != nil {
			p.logger.Warn("failed to ack unknown event", "eventId", eventID, "error", err)
		}
		return
	}

	// 3. Check dedup (only if we have a valid eventID)
	if eventID != "" {
		processed, err := p.eventStore.IsProcessed(ctx, eventID)
		if err != nil {
			p.logger.Warn("failed to check dedup", "eventId", eventID, "error", err)
		}
		if err == nil && processed {
			// Already processed: ack and skip
			if ackErr := msg.Ack(); ackErr != nil {
				p.logger.Warn("failed to ack duplicate event", "eventId", eventID, "error", ackErr)
			}
			return
		}
	}

	// 4. Call handler (anonymize)
	record, err := handler(ctx, msg.Data)
	if err != nil {
		// Deterministic failure (bad JSON, missing fields) -> DLQ + ack to prevent infinite loop
		if eventID == "" {
			eventID = "unknown"
		}
		dlqPayload := sanitizeForDLQ(msg.Data)
		if dlqErr := p.eventStore.SendToDLQ(ctx, eventID, msg.Subject, dlqPayload, err.Error()); dlqErr != nil {
			p.logger.Warn("failed to send handler error to DLQ, skipping ack to force redelivery", "eventId", eventID, "error", dlqErr)
			return // don't ack — DLQ failed, let NATS redeliver
		}
		// Ack only after DLQ persistence succeeds (prevents message loss)
		if ackErr := msg.Ack(); ackErr != nil {
			p.logger.Warn("failed to ack after DLQ routing", "eventId", eventID, "error", ackErr)
		}
		return
	}

	// 5. Send to materialization stage (with context-aware select to prevent
	// blocking on shutdown when anonCh is full)
	select {
	case out <- materializeJob{record: record, ack: msg.Ack}:
	case <-ctx.Done():
		p.logger.Warn("pipeline shutting down, dropping anonymized record", "eventId", record.EventID)
	}
}

// materializeStage persists an anonymized record, marks it processed, and acks.
func (p *pipeline) materializeStage(ctx context.Context, job materializeJob) {
	record := job.record

	// Materialize based on record.Kind
	if err := p.materialize(ctx, record); err != nil {
		// Materialization failed -> DLQ, do NOT ack (let NATS redeliver for transient failures)
		dlqPayload := sanitizeForDLQ(nil) // no raw payload at this stage
		if dlqErr := p.eventStore.SendToDLQ(ctx, record.EventID, string(record.EventType), dlqPayload, err.Error()); dlqErr != nil {
			p.logger.Warn("failed to send materialization error to DLQ", "eventId", record.EventID, "error", dlqErr)
		}
		return
	}

	// Mark as processed (after successful materialization).
	// If this fails, the event will be reprocessed on next delivery (safe due
	// to idempotent UPSERTs), but we must NOT ack — otherwise the dedup marker
	// is lost and the event could be double-counted on schema changes.
	if err := p.eventStore.MarkProcessed(ctx, record.EventID, string(record.EventType)); err != nil {
		if isPermanentDBError(err) {
			// Permanent failure (e.g. event id is not a UUID against the UUID-typed
			// event_processing_log — SQLSTATE 22P02): every redelivery fails identically,
			// which would wedge the durable consumer forever and stall the ack floor.
			// The fact is already materialized (idempotent upsert), so ack to drop the
			// poison message instead of looping. Note: the DLQ tables are also UUID-typed,
			// so a synthetic-id DLQ insert would fail the same way — the structured log
			// below is the audit trail for these malformed events.
			p.logger.Warn("mark-processed hit a permanent data error; acking to drop poison event",
				"eventId", record.EventID, "eventType", record.EventType, "error", err)
			if ackErr := job.ack(); ackErr != nil {
				p.logger.Warn("failed to ack poison event", "eventId", record.EventID, "error", ackErr)
			}
			return
		}
		// Transient failure: skip ack so NATS redelivers (safe due to idempotent UPSERTs).
		p.logger.Warn("failed to mark event as processed, skipping ack", "eventId", record.EventID, "error", err)
		return
	}

	// Ack only after both materialization AND dedup marker succeed
	if err := job.ack(); err != nil {
		p.logger.Warn("failed to ack after successful materialization", "eventId", record.EventID, "error", err)
	}
}

// materialize dispatches the AnonymizedRecord to the appropriate FactStore method.
func (p *pipeline) materialize(ctx context.Context, record AnonymizedRecord) error {
	switch record.Kind {
	case FactKindPatientSnapshot:
		return p.factStore.UpsertPatientSnapshot(ctx, record)
	case FactKindDiagnosis:
		return p.factStore.IncrementDiagnosis(ctx, record)
	case FactKindAppointment:
		return p.factStore.IncrementAppointment(ctx, record)
	case FactKindReferral:
		return p.factStore.IncrementReferral(ctx, record)
	case FactKindViolation:
		return p.factStore.IncrementViolation(ctx, record)
	case FactKindBenefit:
		return p.factStore.IncrementBenefit(ctx, record)
	case FactKindFamilyComposition:
		return p.factStore.UpsertFamilyComposition(ctx, record)
	default:
		return fmt.Errorf("%w: unknown fact kind %q", ErrMaterializationFailed, record.Kind)
	}
}

// extractEventID attempts to pull the event "id" from raw JSON bytes.
// Swift events have "id" as a top-level UUID field (no metadata wrapper).
// Returns the eventID and true if found, or empty string and false on failure.
// isPermanentDBError reports whether err is a Postgres error that will fail
// identically on every retry, so the offending event must be dropped (acked)
// rather than redelivered. SQLSTATE class "22" is "data exception" — e.g. 22P02
// invalid_text_representation, raised when a non-UUID event id is written to a
// UUID column. Transient errors (connection loss, deadlocks) are NOT class 22
// and fall through to the redelivery path.
func isPermanentDBError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return strings.HasPrefix(pgErr.Code, "22")
}

func extractEventID(data []byte) (string, bool) {
	var envelope struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return "", false
	}
	if envelope.ID == "" {
		return "", false
	}
	return envelope.ID, true
}

// sanitizeForDLQ strips PII from raw event data before sending to the dead-letter
// queue. Only metadata (eventId, occurredAt, schemaVersion) is preserved. The event
// subject/type is passed separately to SendToDLQ. This ensures LGPD compliance.
func sanitizeForDLQ(data []byte) []byte {
	if len(data) == 0 {
		return []byte(`{"metadata":{}}`)
	}

	var envelope struct {
		Metadata struct {
			EventID       string `json:"eventId"`
			OccurredAt    string `json:"occurredAt"`
			SchemaVersion string `json:"schemaVersion"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		// Can't parse: return minimal safe payload
		return []byte(`{"metadata":{"parseError":true}}`)
	}

	safe, _ := json.Marshal(map[string]any{
		"metadata": map[string]string{
			"eventId":       envelope.Metadata.EventID,
			"occurredAt":    envelope.Metadata.OccurredAt,
			"schemaVersion": envelope.Metadata.SchemaVersion,
		},
	})
	return safe
}

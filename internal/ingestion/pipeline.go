package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	term   AckFunc
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
			if isPermanentDBError(err) {
				// The DLQ insert itself can never succeed for this datum — redelivering
				// would loop forever. Drop it; the structured log is the audit trail.
				p.dropPoison(eventID, eventType, err, msg.Term, msg.Ack)
				return
			}
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
			if isPermanentDBError(dlqErr) {
				// Most likely poison path: an event with a malformed id is usually
				// malformed elsewhere too, so it fails the handler and lands here —
				// never reaching MarkProcessed.
				p.dropPoison(eventID, msg.Subject, dlqErr, msg.Term, msg.Ack)
				return
			}
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
	case out <- materializeJob{record: record, ack: msg.Ack, term: msg.Term}:
	case <-ctx.Done():
		p.logger.Warn("pipeline shutting down, dropping anonymized record", "eventId", record.EventID)
	}
}

// dropPoison removes a message that can never be processed from the consumer's
// redelivery loop. Prefers Term (JetStream semantics for poison data: stops
// redelivery AND publishes the MSG_TERMINATED advisory) and falls back to Ack
// when the consumer does not provide Term, since leaving the message pending
// would wedge the durable consumer forever.
func (p *pipeline) dropPoison(eventID, eventType string, cause error, term, ack AckFunc) {
	p.logger.Warn("permanent data error; dropping poison event",
		"eventId", eventID, "eventType", eventType, "error", cause)

	drop, how := term, "term"
	if drop == nil {
		drop, how = ack, "ack"
	}
	if drop == nil {
		p.logger.Warn("cannot drop poison event: neither Term nor Ack available", "eventId", eventID)
		return
	}
	if err := drop(); err != nil {
		p.logger.Warn("failed to drop poison event", "eventId", eventID, "via", how, "error", err)
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
			if isPermanentDBError(dlqErr) {
				p.dropPoison(record.EventID, string(record.EventType), dlqErr, job.term, job.ack)
				return
			}
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
			// Defense in depth. Since TICKET-008 the store normalizes the event id
			// (UUIDv5 for non-UUID values), so 22P02 should no longer reach here —
			// the dedup marker IS written even for malformed ids. If some other
			// impossible datum shows up, drop the message instead of letting it
			// redeliver forever and stall the consumer's ack floor.
			//
			// Dropping is safe for the fact already materialized above, but note it
			// leaves NO dedup marker: a later replay would materialize it again, and
			// the Increment* facts accumulate (col = col + EXCLUDED.col) rather than
			// being idempotent. That is why the real fix is the id normalization,
			// not this branch.
			p.dropPoison(record.EventID, string(record.EventType), err, job.term, job.ack)
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
	case FactKindLifecycle:
		return p.factStore.UpdatePatientLifecycle(ctx, record)
	case FactKindNone:
		// Recognized event with no local effect (see acknowledgePIIAnonymized).
		// Returning nil here is what keeps it out of the DLQ.
		return nil
	default:
		return fmt.Errorf("%w: unknown fact kind %q", ErrMaterializationFailed, record.Kind)
	}
}

// pgCodeInvalidTextRepresentation is SQLSTATE 22P02 (invalid_text_representation),
// raised when a value cannot be parsed as the column's type — e.g. a non-UUID
// string written to a UUID column.
//
// Deliberately narrow: the rest of SQLSTATE class 22 ("data exception") includes
// codes such as 22001 (string_data_right_truncation) and 22003
// (numeric_value_out_of_range), which signal a schema/handler mismatch on OUR
// side, not bad data from the producer. Dropping an event on those would discard
// good data to hide our own bug, so they stay on the redelivery path where they
// remain visible.
const pgCodeInvalidTextRepresentation = "22P02"

// isPermanentDBError reports whether err is a Postgres error that will fail
// identically on every retry, meaning redelivery cannot help and the message has
// to leave the consumer's queue. Transient errors (connection loss, deadlocks)
// are not included and fall through to the redelivery path.
func isPermanentDBError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == pgCodeInvalidTextRepresentation
}

// extractEventID attempts to pull the event "id" from raw JSON bytes.
// Swift events have "id" as a top-level UUID field (no metadata wrapper).
// Returns the eventID and true if found, or empty string and false on failure.
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

package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/acdgbrasil/svc-analysis-bi/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---------------------------------------------------------------------------
// Test: Messages flow through consumer -> anonymize -> materialize
// ---------------------------------------------------------------------------

func TestPipeline_HappyPath_MessageFlowsThrough(t *testing.T) {
	eventStore := newFakeEventStore()
	factStore := newFakeFactStore()
	geoLookup := newFakeGeographyLookup()
	salt := "test-salt"

	registry := NewEventHandlerRegistry(geoLookup, salt)

	patientCreatedPayload, _ := json.Marshal(map[string]any{
		"id":         "evt-pipeline-001",
		"occurredAt": "2025-06-15T10:00:00Z",
		"actorId":    "actor-001",
		"patientId":  "pat-uuid-pipeline",
		"personId":   "person-uuid-pipeline",
		"birthDate":  "1990-03-15",
		"sex":        "MALE",
		"cep":        "13083970",
	})

	ackTrack := newAckTracker()

	consumer := newFakeConsumer(RawMessage{
		Subject: string(domain.EventPatientCreated),
		Data:    patientCreatedPayload,
		Ack:     ackTrack.ack,
	})

	cfg := PipelineConfig{
		RawBufferSize:        10,
		AnonymizedBufferSize: 10,
		AnonymizeWorkers:     1,
		MaterializeWorkers:   1,
	}

	pipeline := NewPipeline(cfg, consumer, registry, factStore, eventStore)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := pipeline.Run(ctx)
	// Pipeline should return nil or ErrPipelineShutdown on context cancellation
	if err != nil && !errors.Is(err, ErrPipelineShutdown) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected pipeline error: %v", err)
	}

	// Verify that the fact store received the materialized record
	calls := factStore.getCalls()
	if len(calls) == 0 {
		t.Error("expected at least one fact store call after processing message")
	}

	// Verify ack was called
	if ackTrack.ackCount() == 0 {
		t.Error("expected Ack to be called after successful materialization")
	}

	// Verify event was marked as processed
	if !eventStore.isMarkedProcessed("evt-pipeline-001") {
		t.Error("expected event to be marked as processed")
	}
}

func TestPipeline_HappyPath_MultipleMessages(t *testing.T) {
	eventStore := newFakeEventStore()
	factStore := newFakeFactStore()
	geoLookup := newFakeGeographyLookup()
	salt := "test-salt"

	registry := NewEventHandlerRegistry(geoLookup, salt)

	makePatientEvent := func(eventID, patientID string) []byte {
		data, _ := json.Marshal(map[string]any{
			"id":         eventID,
			"occurredAt": "2025-06-15T10:00:00Z",
			"actorId":    "actor-001",
			"patientId":  patientID,
			"personId":   "person-001",
			"birthDate":  "1990-01-01",
			"sex":        "FEMALE",
			"cep":        "01310100",
		})
		return data
	}

	ack1 := newAckTracker()
	ack2 := newAckTracker()

	consumer := newFakeConsumer(
		RawMessage{
			Subject: string(domain.EventPatientCreated),
			Data:    makePatientEvent("evt-multi-001", "pat-001"),
			Ack:     ack1.ack,
		},
		RawMessage{
			Subject: string(domain.EventPatientCreated),
			Data:    makePatientEvent("evt-multi-002", "pat-002"),
			Ack:     ack2.ack,
		},
	)

	cfg := PipelineConfig{
		RawBufferSize:        10,
		AnonymizedBufferSize: 10,
		AnonymizeWorkers:     1,
		MaterializeWorkers:   1,
	}

	pipeline := NewPipeline(cfg, consumer, registry, factStore, eventStore)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_ = pipeline.Run(ctx)

	calls := factStore.getCalls()
	if len(calls) < 2 {
		t.Errorf("expected at least 2 fact store calls, got %d", len(calls))
	}

	if ack1.ackCount() == 0 {
		t.Error("expected first message to be acked")
	}
	if ack2.ackCount() == 0 {
		t.Error("expected second message to be acked")
	}
}

// ---------------------------------------------------------------------------
// Test: Unknown event type -> DLQ
// ---------------------------------------------------------------------------

func TestPipeline_UnknownEventType_SentToDLQ(t *testing.T) {
	eventStore := newFakeEventStore()
	factStore := newFakeFactStore()
	geoLookup := newFakeGeographyLookup()
	salt := "test-salt"

	registry := NewEventHandlerRegistry(geoLookup, salt)

	ackTrack := newAckTracker()

	unknownPayload := []byte(`{"id":"evt-unknown-001","occurredAt":"2025-06-15T10:00:00Z","actorId":"actor-001"}`)

	consumer := newFakeConsumer(RawMessage{
		Subject: "social-care.unknown.event.type",
		Data:    unknownPayload,
		Ack:     ackTrack.ack,
	})

	cfg := PipelineConfig{
		RawBufferSize:        10,
		AnonymizedBufferSize: 10,
		AnonymizeWorkers:     1,
		MaterializeWorkers:   1,
	}

	pipeline := NewPipeline(cfg, consumer, registry, factStore, eventStore)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = pipeline.Run(ctx)

	// Unknown events should be sent to DLQ
	dlqEntries := eventStore.getDLQ()
	if len(dlqEntries) == 0 {
		t.Error("expected unknown event to be sent to DLQ")
	}

	// Fact store should NOT have been called for the unknown event
	if len(factStore.getCalls()) != 0 {
		t.Error("expected no fact store calls for unknown event type")
	}

	// Ack should still be called to prevent NATS redelivery
	if ackTrack.ackCount() == 0 {
		t.Error("expected Ack to be called after DLQ routing")
	}
}

// ---------------------------------------------------------------------------
// Test: Duplicate event (already processed) -> skipped and acked
// ---------------------------------------------------------------------------

func TestPipeline_DuplicateEvent_SkippedAndAcked(t *testing.T) {
	eventStore := newFakeEventStore()
	// Pre-mark the event as already processed
	eventStore.markAsAlreadyProcessed("evt-dup-001")

	factStore := newFakeFactStore()
	geoLookup := newFakeGeographyLookup()
	salt := "test-salt"

	registry := NewEventHandlerRegistry(geoLookup, salt)

	payload, _ := json.Marshal(map[string]any{
		"id":         "evt-dup-001",
		"occurredAt": "2025-06-15T10:00:00Z",
		"actorId":    "actor-001",
		"patientId":  "pat-dup",
		"personId":   "person-dup",
		"birthDate":  "1990-01-01",
		"sex":        "MALE",
		"cep":        "13083970",
	})

	ackTrack := newAckTracker()

	consumer := newFakeConsumer(RawMessage{
		Subject: string(domain.EventPatientCreated),
		Data:    payload,
		Ack:     ackTrack.ack,
	})

	cfg := PipelineConfig{
		RawBufferSize:        10,
		AnonymizedBufferSize: 10,
		AnonymizeWorkers:     1,
		MaterializeWorkers:   1,
	}

	pipeline := NewPipeline(cfg, consumer, registry, factStore, eventStore)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = pipeline.Run(ctx)

	// Fact store should NOT have been called for duplicate event
	calls := factStore.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected 0 fact store calls for duplicate event, got %d", len(calls))
	}

	// Ack should still be called to consume the duplicate from NATS
	if ackTrack.ackCount() == 0 {
		t.Error("expected Ack to be called for duplicate event")
	}
}

// ---------------------------------------------------------------------------
// Test: Context cancellation -> graceful shutdown
// ---------------------------------------------------------------------------

func TestPipeline_ContextCancellation_GracefulShutdown(t *testing.T) {
	eventStore := newFakeEventStore()
	factStore := newFakeFactStore()
	geoLookup := newFakeGeographyLookup()
	salt := "test-salt"

	registry := NewEventHandlerRegistry(geoLookup, salt)

	// Consumer that blocks until context is cancelled (no messages)
	consumer := newFakeConsumer()

	cfg := PipelineConfig{
		RawBufferSize:        10,
		AnonymizedBufferSize: 10,
		AnonymizeWorkers:     1,
		MaterializeWorkers:   1,
	}

	pipeline := NewPipeline(cfg, consumer, registry, factStore, eventStore)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- pipeline.Run(ctx)
	}()

	// Cancel after a short delay
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		// Should return nil, context.Canceled, or ErrPipelineShutdown
		if err != nil && !errors.Is(err, ErrPipelineShutdown) && !errors.Is(err, context.Canceled) {
			t.Errorf("expected graceful shutdown error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not shut down within 5 seconds")
	}
}

// ---------------------------------------------------------------------------
// Test: Ack called only after successful materialization
// ---------------------------------------------------------------------------

func TestPipeline_AckOnlyAfterMaterialization(t *testing.T) {
	eventStore := newFakeEventStore()
	// FactStore that always fails
	factStore := newFakeFactStoreWithError(errors.New("materialization failure"))
	geoLookup := newFakeGeographyLookup()
	salt := "test-salt"

	registry := NewEventHandlerRegistry(geoLookup, salt)

	payload, _ := json.Marshal(map[string]any{
		"id":         "evt-noack-001",
		"occurredAt": "2025-06-15T10:00:00Z",
		"actorId":    "actor-001",
		"patientId":  "pat-noack",
		"personId":   "person-noack",
		"birthDate":  "1990-01-01",
		"sex":        "MALE",
		"cep":        "13083970",
	})

	ackTrack := newAckTracker()

	consumer := newFakeConsumer(RawMessage{
		Subject: string(domain.EventPatientCreated),
		Data:    payload,
		Ack:     ackTrack.ack,
	})

	cfg := PipelineConfig{
		RawBufferSize:        10,
		AnonymizedBufferSize: 10,
		AnonymizeWorkers:     1,
		MaterializeWorkers:   1,
	}

	pipeline := NewPipeline(cfg, consumer, registry, factStore, eventStore)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = pipeline.Run(ctx)

	// Ack should NOT have been called because materialization failed
	if ackTrack.ackCount() != 0 {
		t.Error("Ack must NOT be called when materialization fails")
	}

	// Event should NOT be marked as processed
	if eventStore.isMarkedProcessed("evt-noack-001") {
		t.Error("event must NOT be marked as processed when materialization fails")
	}
}

// ---------------------------------------------------------------------------
// Test: Consumer connection error propagates
// ---------------------------------------------------------------------------

func TestPipeline_ConsumerConnectionError(t *testing.T) {
	eventStore := newFakeEventStore()
	factStore := newFakeFactStore()
	geoLookup := newFakeGeographyLookup()
	salt := "test-salt"

	registry := NewEventHandlerRegistry(geoLookup, salt)

	consumer := newFakeConsumerWithError(ErrConsumerConnectionFailed)

	cfg := PipelineConfig{
		RawBufferSize:        10,
		AnonymizedBufferSize: 10,
		AnonymizeWorkers:     1,
		MaterializeWorkers:   1,
	}

	pipeline := NewPipeline(cfg, consumer, registry, factStore, eventStore)

	ctx := context.Background()
	err := pipeline.Run(ctx)
	if err == nil {
		t.Fatal("expected error from consumer connection failure")
	}
	if !errors.Is(err, ErrConsumerConnectionFailed) {
		t.Errorf("error = %v, want wrapping %v", err, ErrConsumerConnectionFailed)
	}
}

// ---------------------------------------------------------------------------
// Test: NewPipeline constructor
// ---------------------------------------------------------------------------

func TestNewPipeline_ReturnsNonNil(t *testing.T) {
	eventStore := newFakeEventStore()
	factStore := newFakeFactStore()
	geoLookup := newFakeGeographyLookup()
	salt := "test-salt"

	registry := NewEventHandlerRegistry(geoLookup, salt)

	consumer := newFakeConsumer()

	cfg := PipelineConfig{
		RawBufferSize:        10,
		AnonymizedBufferSize: 10,
		AnonymizeWorkers:     2,
		MaterializeWorkers:   2,
	}

	pipeline := NewPipeline(cfg, consumer, registry, factStore, eventStore)
	if pipeline == nil {
		t.Fatal("NewPipeline must return a non-nil Pipeline")
	}
}

// ---------------------------------------------------------------------------
// Test: a permanent MarkProcessed error (e.g. non-UUID event id, SQLSTATE 22P02)
// must NOT wedge the consumer — the fact is already materialized, so the poison
// message is dropped instead of looping on redelivery forever.
// Regression for the "event with non-UUID id → redelivery loop" gap.
//
// NB (TICKET-008): this RawMessage sets no `Term`, so the case now exercises the
// FALLBACK path — a consumer without Term support drops via Ack, which at least
// unblocks the queue. The preferred path is covered by
// TestPipeline_PermanentMarkError_TerminatesInsteadOfAcking.
// ---------------------------------------------------------------------------

func TestPipeline_PermanentMarkError_AcksToDropPoison(t *testing.T) {
	eventStore := newFakeEventStore()
	// Simulate Postgres rejecting a non-UUID event id (class 22 = data exception).
	eventStore.markErr = &pgconn.PgError{Code: "22P02", Message: "invalid input syntax for type uuid"}
	factStore := newFakeFactStore()
	geoLookup := newFakeGeographyLookup()
	registry := NewEventHandlerRegistry(geoLookup, "test-salt")

	payload, _ := json.Marshal(map[string]any{
		"id":         "not-a-uuid",
		"occurredAt": "2025-06-15T10:00:00Z",
		"actorId":    "actor-001",
		"patientId":  "pat-poison",
		"personId":   "person-poison",
		"birthDate":  "1990-01-01",
		"sex":        "MALE",
		"cep":        "13083970",
	})

	ackTrack := newAckTracker()
	consumer := newFakeConsumer(RawMessage{
		Subject: string(domain.EventPatientCreated),
		Data:    payload,
		Ack:     ackTrack.ack,
	})

	cfg := PipelineConfig{RawBufferSize: 10, AnonymizedBufferSize: 10, AnonymizeWorkers: 1, MaterializeWorkers: 1}
	pipeline := NewPipeline(cfg, consumer, registry, factStore, eventStore)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = pipeline.Run(ctx)

	// The fact is materialized (idempotent), and the poison message is acked so it
	// is NOT redelivered — breaking the loop.
	if ackTrack.ackCount() == 0 {
		t.Error("Ack must be called to drop a poison event with a permanent (class-22) mark error")
	}
}

// A transient MarkProcessed error (not a Postgres data-exception) must still skip
// the ack so NATS redelivers — we only drop on PERMANENT failures.
func TestPipeline_TransientMarkError_SkipsAckForRedelivery(t *testing.T) {
	eventStore := newFakeEventStore()
	eventStore.markErr = errors.New("connection reset by peer") // transient, not a *pgconn.PgError
	factStore := newFakeFactStore()
	geoLookup := newFakeGeographyLookup()
	registry := NewEventHandlerRegistry(geoLookup, "test-salt")

	payload, _ := json.Marshal(map[string]any{
		"id":         "evt-transient-001",
		"occurredAt": "2025-06-15T10:00:00Z",
		"actorId":    "actor-001",
		"patientId":  "pat-transient",
		"personId":   "person-transient",
		"birthDate":  "1990-01-01",
		"sex":        "MALE",
		"cep":        "13083970",
	})

	ackTrack := newAckTracker()
	consumer := newFakeConsumer(RawMessage{
		Subject: string(domain.EventPatientCreated),
		Data:    payload,
		Ack:     ackTrack.ack,
	})

	cfg := PipelineConfig{RawBufferSize: 10, AnonymizedBufferSize: 10, AnonymizeWorkers: 1, MaterializeWorkers: 1}
	pipeline := NewPipeline(cfg, consumer, registry, factStore, eventStore)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = pipeline.Run(ctx)

	if ackTrack.ackCount() != 0 {
		t.Error("Ack must NOT be called on a transient mark error — the event should be redelivered")
	}
}

// Ensure fakeEventStore satisfies EventProcessingStore
var _ EventProcessingStore = (*fakeEventStore)(nil)

// ---------------------------------------------------------------------------
// TICKET-008 / W0 (RED) — descarte de poison message usa Term(), não Ack().
//
// `Ack` significa "processado com sucesso": o servidor não distingue um descarte
// de um sucesso real e o evento some das métricas do consumidor. O JetStream tem
// `Term` exatamente para dado que nunca poderá ser processado — e ele publica
// advisory em $JS.EVENT.ADVISORY.CONSUMER.MSG_TERMINATED.<STREAM>.<CONSUMER>,
// que é o que permite montar DLQ observável.
// ---------------------------------------------------------------------------

func TestPipeline_PermanentMarkError_TerminatesInsteadOfAcking(t *testing.T) {
	eventStore := newFakeEventStore()
	eventStore.markErr = &pgconn.PgError{Code: "22P02", Message: "invalid input syntax for type uuid"}
	factStore := newFakeFactStore()
	geoLookup := newFakeGeographyLookup()
	registry := NewEventHandlerRegistry(geoLookup, "test-salt")

	payload, _ := json.Marshal(map[string]any{
		"id":         "not-a-uuid",
		"occurredAt": "2025-06-15T10:00:00Z",
		"actorId":    "actor-001",
		"patientId":  "pat-poison",
		"personId":   "person-poison",
		"birthDate":  "1990-01-01",
		"sex":        "MALE",
		"cep":        "13083970",
	})

	ackTrack := newAckTracker()
	termTrack := newAckTracker()
	consumer := newFakeConsumer(RawMessage{
		Subject: string(domain.EventPatientCreated),
		Data:    payload,
		Ack:     ackTrack.ack,
		Term:    termTrack.ack,
	})

	cfg := PipelineConfig{RawBufferSize: 10, AnonymizedBufferSize: 10, AnonymizeWorkers: 1, MaterializeWorkers: 1}
	pipeline := NewPipeline(cfg, consumer, registry, factStore, eventStore)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = pipeline.Run(ctx)

	if termTrack.ackCount() == 0 {
		t.Error("Term deve ser chamado para descartar poison event (semântica correta no JetStream)")
	}
	if ackTrack.ackCount() != 0 {
		t.Error("Ack NÃO deve ser chamado: o evento não foi processado com sucesso, foi descartado")
	}
}

// Um erro PERMANENTE no SendToDLQ também não pode travar a fila. Antes do
// TICKET-008 este caminho (handler falha -> DLQ falha) reentregava para sempre,
// e era o mais provável: um evento com id inválido normalmente está malformado
// em outros campos também, então falha no handler e nunca chega ao MarkProcessed.
func TestPipeline_PermanentDLQError_TerminatesInsteadOfLooping(t *testing.T) {
	eventStore := newFakeEventStore()
	eventStore.dlqErr = &pgconn.PgError{Code: "22P02", Message: "invalid input syntax for type uuid"}
	factStore := newFakeFactStore()
	geoLookup := newFakeGeographyLookup()
	registry := NewEventHandlerRegistry(geoLookup, "test-salt")

	// Payload que o handler REJEITA (campos obrigatórios ausentes) -> vai para DLQ.
	payload, _ := json.Marshal(map[string]any{"id": "not-a-uuid"})

	ackTrack := newAckTracker()
	termTrack := newAckTracker()
	consumer := newFakeConsumer(RawMessage{
		Subject: string(domain.EventPatientCreated),
		Data:    payload,
		Ack:     ackTrack.ack,
		Term:    termTrack.ack,
	})

	cfg := PipelineConfig{RawBufferSize: 10, AnonymizedBufferSize: 10, AnonymizeWorkers: 1, MaterializeWorkers: 1}
	pipeline := NewPipeline(cfg, consumer, registry, factStore, eventStore)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = pipeline.Run(ctx)

	if termTrack.ackCount() == 0 {
		t.Error("DLQ com erro permanente deve terminar a mensagem em vez de deixá-la reentregar para sempre")
	}
}

// Erro TRANSITÓRIO no DLQ continua sem ack e sem term — a mensagem DEVE voltar.
func TestPipeline_TransientDLQError_LeavesMessageForRedelivery(t *testing.T) {
	eventStore := newFakeEventStore()
	eventStore.dlqErr = errors.New("connection reset by peer")
	factStore := newFakeFactStore()
	geoLookup := newFakeGeographyLookup()
	registry := NewEventHandlerRegistry(geoLookup, "test-salt")

	payload, _ := json.Marshal(map[string]any{"id": "3f2504e0-4f89-11d3-9a0c-0305e82c3301"})

	ackTrack := newAckTracker()
	termTrack := newAckTracker()
	consumer := newFakeConsumer(RawMessage{
		Subject: string(domain.EventPatientCreated),
		Data:    payload,
		Ack:     ackTrack.ack,
		Term:    termTrack.ack,
	})

	cfg := PipelineConfig{RawBufferSize: 10, AnonymizedBufferSize: 10, AnonymizeWorkers: 1, MaterializeWorkers: 1}
	pipeline := NewPipeline(cfg, consumer, registry, factStore, eventStore)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = pipeline.Run(ctx)

	if ackTrack.ackCount() != 0 || termTrack.ackCount() != 0 {
		t.Error("falha transitória no DLQ não pode consumir a mensagem: sem Ack e sem Term, para o NATS reentregar")
	}
}

// ---------------------------------------------------------------------------
// Test: acknowledged-but-inert event does NOT reach the DLQ (ADR-002)
// ---------------------------------------------------------------------------

func TestPipeline_PIIAnonymized_AcknowledgedNotDLQd(t *testing.T) {
	eventStore := newFakeEventStore()
	factStore := newFakeFactStore()
	registry := NewEventHandlerRegistry(newFakeGeographyLookup(), "test-salt")
	ackTrack := newAckTracker()

	payload := []byte(`{"id":"evt-erasure-001","occurredAt":"2025-06-15T10:00:00Z","actorId":"actor-001","patientId":"pat-x","personId":"person-x"}`)

	consumer := newFakeConsumer(RawMessage{
		Subject: string(domain.EventPatientPIIAnonymized),
		Data:    payload,
		Ack:     ackTrack.ack,
	})

	cfg := PipelineConfig{
		RawBufferSize:        10,
		AnonymizedBufferSize: 10,
		AnonymizeWorkers:     1,
		MaterializeWorkers:   1,
	}

	pipeline := NewPipeline(cfg, consumer, registry, factStore, eventStore)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = pipeline.Run(ctx)

	// The whole point of FactKindNone: a decision to do nothing must look
	// different from an unhandled event. The DLQ is where unhandled goes.
	if entries := eventStore.getDLQ(); len(entries) != 0 {
		t.Errorf("erasure event must not reach the DLQ, got %d entries", len(entries))
	}

	// And it must not write a fact either — there is nothing to record.
	if calls := factStore.getCalls(); len(calls) != 0 {
		t.Errorf("erasure event must not materialize, got calls: %v", calls)
	}

	if ackTrack.ackCount() == 0 {
		t.Error("expected Ack so NATS does not redeliver")
	}
}

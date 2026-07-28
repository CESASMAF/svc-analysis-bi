// Package ingestion defines contracts for the event ingestion pipeline.
//
// The pipeline consumes NATS JetStream events from svc-social-care,
// anonymizes PII at ingestion time (LGPD Art. 46), and materializes
// anonymized data into a PostgreSQL star schema for descriptive analytics.
//
// Architecture: Consumer -> Anonymize -> Materialize (goroutines + channels).
// Business logic lives in internal/domain; this layer orchestrates the stages.
package ingestion

import (
	"context"
	"errors"
	"time"

	"github.com/acdgbrasil/svc-analysis-bi/internal/domain"
)

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

var (
	// ErrDeserializationFailed indicates that a raw NATS message could not be
	// deserialized into a typed domain event struct.
	ErrDeserializationFailed = errors.New("ingestion: event deserialization failed")

	// ErrUnknownEventType indicates that the NATS subject does not map to any
	// registered handler in the EventHandlerRegistry.
	ErrUnknownEventType = errors.New("ingestion: unknown event type")

	// ErrAnonymizationFailed indicates that the anonymization stage (hashing,
	// generalization, or PII suppression) returned an error.
	ErrAnonymizationFailed = errors.New("ingestion: anonymization failed")

	// ErrMaterializationFailed indicates that dimension resolution or fact
	// table persistence returned an error.
	ErrMaterializationFailed = errors.New("ingestion: materialization failed")

	// ErrConsumerConnectionFailed indicates that the NATS JetStream consumer
	// could not establish or maintain its subscription.
	ErrConsumerConnectionFailed = errors.New("ingestion: consumer connection failed")

	// ErrPipelineShutdown indicates that the pipeline was stopped via context
	// cancellation during graceful shutdown.
	ErrPipelineShutdown = errors.New("ingestion: pipeline shut down")
)

// ---------------------------------------------------------------------------
// RawMessage -- envelope for a NATS message entering the pipeline
// ---------------------------------------------------------------------------

// AckFunc is the function signature for acknowledging a NATS message.
// Calling it signals at-least-once delivery completion. It must be called
// only after successful persistence.
type AckFunc func() error

// RawMessage wraps a single NATS JetStream message with the metadata
// needed to route it through the pipeline. The Ack function must be called
// only after the full pipeline (anonymize + materialize) succeeds.
type RawMessage struct {
	// Subject is the NATS subject (e.g. "social-care.patient.created").
	Subject string

	// Data is the raw JSON payload bytes.
	Data []byte

	// Ack acknowledges the message to NATS JetStream.
	// Must be called exactly once, after successful materialization.
	Ack AckFunc

	// Term terminates the message: instructs JetStream to stop redelivering
	// because the data itself can never be processed (poison message).
	//
	// Differs from Ack, which means "processed successfully" — using Ack to drop
	// a message hides the drop among the successes. Term publishes an advisory to
	// $JS.EVENT.ADVISORY.CONSUMER.MSG_TERMINATED.<STREAM>.<CONSUMER>, which is
	// what makes a server-side Dead Letter Queue observable.
	//
	// May be nil (older fakes, consumers that do not support it): the pipeline
	// then falls back to Ack, which at least unblocks the queue.
	Term AckFunc
}

// ---------------------------------------------------------------------------
// AnonymizedRecord -- output of the anonymization stage
// ---------------------------------------------------------------------------

// FactKind discriminates which fact table an AnonymizedRecord targets.
type FactKind string

const (
	FactKindPatientSnapshot    FactKind = "patient_snapshot"
	FactKindDiagnosis          FactKind = "diagnosis"
	FactKindAppointment        FactKind = "appointment"
	FactKindReferral           FactKind = "referral"
	FactKindViolation          FactKind = "violation"
	FactKindBenefit            FactKind = "benefit"
	FactKindFamilyComposition  FactKind = "family_composition"
)

// AnonymizedRecord is the output of the anonymization stage and the input
// to the materialization stage. It carries only generalized, non-PII data.
//
// The Kind field discriminates which fact table the record targets.
// Exactly one of the payload fields (Snapshot, Diagnosis, Appointment,
// Referral, Violation, Benefit, FamilyComposition) is non-nil, matching Kind.
type AnonymizedRecord struct {
	// Kind identifies which fact table this record materializes into.
	Kind FactKind

	// EventID is the unique event identifier used for idempotent processing.
	EventID string

	// EventType is the original NATS subject that produced this record.
	EventType domain.EventType

	// OccurredAt is the timestamp when the event occurred in the source system.
	OccurredAt time.Time

	// Period is the calendar month this record belongs to, derived from OccurredAt.
	Period domain.Period

	// PatientHash is the irreversible SHA-256 digest of the patient ID.
	PatientHash domain.PatientHash

	// Payload -- exactly one is non-nil based on Kind.
	Snapshot          *SnapshotPayload
	Diagnosis         *DiagnosisPayload
	Appointment       *AppointmentPayload
	Referral          *ReferralPayload
	Violation         *ViolationPayload
	Benefit           *BenefitPayload
	FamilyComposition *FamilyCompositionPayload
}

// SnapshotPayload carries the anonymized data for fact_patient_snapshot.
type SnapshotPayload struct {
	AgeBand                domain.AgeBand
	Sex                    domain.Sex
	Geography              domain.Geography
	HousingType            string
	EducationLevel         string
	IncomeBand             domain.IncomeBand
	ReceivesBenefit        bool
	HasDeficiency          bool
	FoodInsecurity         bool
	IsOvercrowded          bool
	FamilySize             int
	AssessmentCompleteness float64
}

// DiagnosisPayload carries the anonymized data for fact_diagnosis.
type DiagnosisPayload struct {
	AgeBand   domain.AgeBand
	Sex       domain.Sex
	Geography domain.Geography
	ICDCode   string
	ICDLabel  string
	Chapter   string
	Block     string
	NewCases  int
}

// AppointmentPayload carries the anonymized data for fact_appointment.
type AppointmentPayload struct {
	Geography       domain.Geography
	AppointmentType string
}

// ReferralPayload carries the anonymized data for fact_referral.
type ReferralPayload struct {
	Geography          domain.Geography
	DestinationService string
}

// ViolationPayload carries the anonymized data for fact_violation.
type ViolationPayload struct {
	Geography     domain.Geography
	ViolationType string
}

// BenefitPayload carries the anonymized data for fact_benefit.
type BenefitPayload struct {
	Geography       domain.Geography
	BenefitType     string
	BeneficiaryDelta int
	Amount           int64
}

// FamilyCompositionPayload carries the anonymized data for fact_family_composition.
type FamilyCompositionPayload struct {
	Geography          domain.Geography
	FamilySizeDelta    int
	IsAddition         bool
	MemberRelationship string
}

// ---------------------------------------------------------------------------
// PipelineConfig -- tuning parameters for the goroutine pipeline
// ---------------------------------------------------------------------------

// PipelineConfig holds tuning parameters for the goroutine + channel pipeline.
type PipelineConfig struct {
	// RawBufferSize is the capacity of the channel between the consumer
	// and the anonymization stage. A larger buffer absorbs bursts.
	RawBufferSize int

	// AnonymizedBufferSize is the capacity of the channel between the
	// anonymization stage and the materialization stage.
	AnonymizedBufferSize int

	// AnonymizeWorkers is the number of goroutines running the
	// anonymization stage concurrently.
	AnonymizeWorkers int

	// MaterializeWorkers is the number of goroutines running the
	// materialization stage concurrently.
	MaterializeWorkers int
}

// ---------------------------------------------------------------------------
// Port interfaces (defined in the consumer package per DIP)
// ---------------------------------------------------------------------------

// Consumer subscribes to NATS JetStream and delivers raw messages to a channel.
// Implementations handle connection management, reconnection, and durable
// subscription setup. The consumer must block until ctx is cancelled or a
// fatal connection error occurs.
type Consumer interface {
	// Subscribe begins consuming messages from the configured NATS stream
	// and sends them to the provided channel. It blocks until ctx is
	// cancelled. The caller owns the channel and must close it after
	// Subscribe returns.
	Subscribe(ctx context.Context, out chan<- RawMessage) error
}

// EventHandler processes a single deserialized event through the full
// anonymize-then-materialize pipeline. Implementations call domain functions
// for anonymization and FactStore for persistence.
type EventHandler interface {
	// Handle deserializes the raw data into a typed event, anonymizes PII,
	// resolves dimension IDs, and persists the result into fact tables.
	// Returns the anonymized record on success for pipeline observability.
	Handle(ctx context.Context, eventType domain.EventType, data []byte) (AnonymizedRecord, error)
}

// FactStore persists anonymized data into the star schema fact tables.
// Each method maps to one fact table. Implementations use pgx and
// DimensionStores to resolve foreign key IDs before inserting.
type FactStore interface {
	// UpsertPatientSnapshot inserts or updates a patient snapshot for the
	// given period. The UNIQUE(period_id, patient_hash) constraint ensures
	// that only one snapshot per patient per period exists.
	UpsertPatientSnapshot(ctx context.Context, record AnonymizedRecord) error

	// IncrementDiagnosis increments (or inserts) a diagnosis fact row
	// for the given period, geography, diagnosis, age band, and sex.
	IncrementDiagnosis(ctx context.Context, record AnonymizedRecord) error

	// IncrementAppointment increments (or inserts) an appointment fact row
	// for the given period, geography, and appointment type.
	IncrementAppointment(ctx context.Context, record AnonymizedRecord) error

	// IncrementReferral increments (or inserts) a referral fact row
	// for the given period, geography, and destination service.
	IncrementReferral(ctx context.Context, record AnonymizedRecord) error

	// IncrementViolation increments (or inserts) a violation fact row
	// for the given period, geography, and violation type.
	IncrementViolation(ctx context.Context, record AnonymizedRecord) error

	// IncrementBenefit increments (or inserts) a benefit fact row
	// for the given period, geography, and benefit type.
	IncrementBenefit(ctx context.Context, record AnonymizedRecord) error

	// UpsertFamilyComposition updates family composition aggregates
	// for the given period and geography.
	UpsertFamilyComposition(ctx context.Context, record AnonymizedRecord) error
}

// ---------------------------------------------------------------------------
// EventHandlerFunc -- handler function signature for the registry
// ---------------------------------------------------------------------------

// EventHandlerFunc is the function signature for a single event type handler.
// It receives the raw JSON bytes and returns an AnonymizedRecord or an error.
// The registry maps domain.EventType to one of these functions.
type EventHandlerFunc func(ctx context.Context, data []byte) (AnonymizedRecord, error)

// EventHandlerRegistry maps each domain.EventType to its corresponding
// handler function. The pipeline looks up the handler by NATS subject
// and returns ErrUnknownEventType if no handler is registered.
type EventHandlerRegistry map[domain.EventType]EventHandlerFunc

// ---------------------------------------------------------------------------
// EventProcessingStore -- dedup and dead-letter tracking (port interface)
// ---------------------------------------------------------------------------

// EventProcessingStore tracks which events have been processed and routes
// failures to a dead-letter queue. Defined here (not imported from store)
// to respect import boundaries: ingestion depends only on domain types and
// its own port interfaces.
type EventProcessingStore interface {
	// IsProcessed returns true if the event has already been processed.
	IsProcessed(ctx context.Context, eventID string) (bool, error)

	// MarkProcessed records an event as successfully processed.
	MarkProcessed(ctx context.Context, eventID string, eventType string) error

	// SendToDLQ persists a failed event to the dead-letter queue.
	// The payload parameter must be PII-safe (sanitized metadata only).
	SendToDLQ(ctx context.Context, eventID string, eventType string, payload []byte, errMsg string) error
}

// ---------------------------------------------------------------------------
// Logger -- minimal logging port for pipeline observability
// ---------------------------------------------------------------------------

// Logger is a minimal interface for pipeline observability. Implementations
// may use slog, zerolog, or any structured logger.
type Logger interface {
	Warn(msg string, keysAndValues ...any)
}

// nopLogger is a no-op logger used when no logger is provided.
type nopLogger struct{}

func (nopLogger) Warn(string, ...any) {}

// ---------------------------------------------------------------------------
// Pipeline -- top-level orchestrator
// ---------------------------------------------------------------------------

// Pipeline is the top-level orchestrator that wires Consumer, EventHandler,
// and FactStore into a goroutine + channel pipeline with graceful shutdown.
type Pipeline interface {
	// Run starts the pipeline and blocks until ctx is cancelled or a
	// fatal error occurs. It spawns consumer, anonymizer, and materializer
	// goroutines connected by channels.
	Run(ctx context.Context) error
}

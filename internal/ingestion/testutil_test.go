package ingestion

import (
	"context"
	"sync"

	"github.com/acdgbrasil/svc-analysis-bi/internal/domain"
)

// ---------------------------------------------------------------------------
// fakeConsumer -- delivers predefined messages to the pipeline
// ---------------------------------------------------------------------------

type fakeConsumer struct {
	messages []RawMessage
	err      error
}

func newFakeConsumer(messages ...RawMessage) *fakeConsumer {
	return &fakeConsumer{messages: messages}
}

func newFakeConsumerWithError(err error) *fakeConsumer {
	return &fakeConsumer{err: err}
}

func (f *fakeConsumer) Subscribe(ctx context.Context, out chan<- RawMessage) error {
	if f.err != nil {
		return f.err
	}
	for _, msg := range f.messages {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- msg:
		}
	}
	// Block until context is cancelled (simulates durable subscription)
	<-ctx.Done()
	return ctx.Err()
}

// ---------------------------------------------------------------------------
// fakeFactStore -- records calls for assertion
// ---------------------------------------------------------------------------

type factStoreCall struct {
	Method string
	Record AnonymizedRecord
}

type fakeFactStore struct {
	mu    sync.Mutex
	calls []factStoreCall
	err   error
}

func newFakeFactStore() *fakeFactStore {
	return &fakeFactStore{}
}

func newFakeFactStoreWithError(err error) *fakeFactStore {
	return &fakeFactStore{err: err}
}

func (f *fakeFactStore) getCalls() []factStoreCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	dst := make([]factStoreCall, len(f.calls))
	copy(dst, f.calls)
	return dst
}

func (f *fakeFactStore) record(method string, rec AnonymizedRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, factStoreCall{Method: method, Record: rec})
	return f.err
}

func (f *fakeFactStore) UpsertPatientSnapshot(ctx context.Context, rec AnonymizedRecord) error {
	return f.record("UpsertPatientSnapshot", rec)
}

func (f *fakeFactStore) UpdatePatientLifecycle(ctx context.Context, rec AnonymizedRecord) error {
	return f.record("UpdatePatientLifecycle", rec)
}

func (f *fakeFactStore) IncrementDiagnosis(ctx context.Context, rec AnonymizedRecord) error {
	return f.record("IncrementDiagnosis", rec)
}

func (f *fakeFactStore) IncrementAppointment(ctx context.Context, rec AnonymizedRecord) error {
	return f.record("IncrementAppointment", rec)
}

func (f *fakeFactStore) IncrementReferral(ctx context.Context, rec AnonymizedRecord) error {
	return f.record("IncrementReferral", rec)
}

func (f *fakeFactStore) IncrementViolation(ctx context.Context, rec AnonymizedRecord) error {
	return f.record("IncrementViolation", rec)
}

func (f *fakeFactStore) IncrementBenefit(ctx context.Context, rec AnonymizedRecord) error {
	return f.record("IncrementBenefit", rec)
}

func (f *fakeFactStore) UpsertFamilyComposition(ctx context.Context, rec AnonymizedRecord) error {
	return f.record("UpsertFamilyComposition", rec)
}

// ---------------------------------------------------------------------------
// fakeGeographyLookup -- returns a fixed geography for any CEP
// ---------------------------------------------------------------------------

type fakeGeographyLookup struct {
	geo domain.Geography
	err error
}

func newFakeGeographyLookup() *fakeGeographyLookup {
	return &fakeGeographyLookup{
		geo: domain.Geography{
			MesoregionCode:  "3515",
			MesoregionName:  "Campinas",
			MicroregionCode: "35038",
			MicroregionName: "Campinas",
			StateCode:       "35",
			StateName:       "Sao Paulo",
			Region:          "Sudeste",
		},
	}
}

func newFakeGeographyLookupWithError(err error) *fakeGeographyLookup {
	return &fakeGeographyLookup{err: err}
}

func (f *fakeGeographyLookup) FindByCEP(cep string) (domain.Geography, error) {
	if f.err != nil {
		return domain.Geography{}, f.err
	}
	return f.geo, nil
}

// ---------------------------------------------------------------------------
// fakeEventStore -- tracks processed event IDs and DLQ entries
// ---------------------------------------------------------------------------

type dlqEntry struct {
	EventID   string
	EventType string
	Payload   []byte
	ErrMsg    string
}

type fakeEventStore struct {
	mu        sync.Mutex
	processed map[string]bool
	dlq       []dlqEntry
	checkErr  error
	markErr   error
	dlqErr    error
}

func newFakeEventStore() *fakeEventStore {
	return &fakeEventStore{
		processed: make(map[string]bool),
	}
}

func (f *fakeEventStore) IsProcessed(ctx context.Context, eventID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checkErr != nil {
		return false, f.checkErr
	}
	return f.processed[eventID], nil
}

func (f *fakeEventStore) MarkProcessed(ctx context.Context, eventID string, eventType string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markErr != nil {
		return f.markErr
	}
	f.processed[eventID] = true
	return nil
}

func (f *fakeEventStore) SendToDLQ(ctx context.Context, eventID string, eventType string, payload []byte, errMsg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dlqErr != nil {
		return f.dlqErr
	}
	f.dlq = append(f.dlq, dlqEntry{
		EventID:   eventID,
		EventType: eventType,
		Payload:   payload,
		ErrMsg:    errMsg,
	})
	return nil
}

func (f *fakeEventStore) markAsAlreadyProcessed(eventID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.processed[eventID] = true
}

func (f *fakeEventStore) getDLQ() []dlqEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	dst := make([]dlqEntry, len(f.dlq))
	copy(dst, f.dlq)
	return dst
}

func (f *fakeEventStore) isMarkedProcessed(eventID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.processed[eventID]
}

// ---------------------------------------------------------------------------
// ackTracker -- tracks whether Ack was called and how many times
// ---------------------------------------------------------------------------

type ackTracker struct {
	mu    sync.Mutex
	count int
}

func newAckTracker() *ackTracker {
	return &ackTracker{}
}

func (a *ackTracker) ack() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.count++
	return nil
}

func (a *ackTracker) ackCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.count
}

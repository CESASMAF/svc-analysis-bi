package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EventStore tracks which NATS events have been processed.
type EventStore interface {
	IsProcessed(ctx context.Context, eventID string) (bool, error)
	MarkProcessed(ctx context.Context, eventID string, eventType string) error
	SendToDLQ(ctx context.Context, eventID string, eventType string, payload []byte, errMsg string) error
}

// PgEventStore implements EventStore using pgx.
type PgEventStore struct {
	pool *pgxpool.Pool
}

// NewEventStore creates a PgEventStore backed by the given pool.
func NewEventStore(pool *pgxpool.Pool) *PgEventStore {
	return &PgEventStore{pool: pool}
}

// As três operações normalizam o id com normalizeEventID antes do SQL. As colunas
// `event_processing_log.event_id` e `event_dlq.event_id` são UUID; um id textual
// (ou o literal "unknown", que o pipeline usa quando não consegue extrair o `id`
// do payload) faria o insert falhar com SQLSTATE 22P02 e — pior que o erro em si —
// deixaria o evento sem marker de dedup, abrindo espaço para materializá-lo mais
// de uma vez. Como a derivação é determinística, IsProcessed e MarkProcessed
// concordam entre si e entre réplicas. Ver internal/store/event_id.go.

func (s *PgEventStore) IsProcessed(ctx context.Context, eventID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM event_processing_log WHERE event_id = $1 AND status = 'processed')",
		normalizeEventID(eventID),
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrEventCheckFailed, err)
	}
	return exists, nil
}

func (s *PgEventStore) MarkProcessed(ctx context.Context, eventID string, eventType string) error {
	tag, err := s.pool.Exec(ctx,
		"INSERT INTO event_processing_log (event_id, event_type, status) VALUES ($1, $2, 'processed') ON CONFLICT (event_id) DO NOTHING",
		normalizeEventID(eventID), eventType,
	)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrEventMarkFailed, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrEventAlreadyProcessed
	}
	return nil
}

func (s *PgEventStore) SendToDLQ(ctx context.Context, eventID string, eventType string, payload []byte, errMsg string) error {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO event_dlq (event_id, event_type, payload, error) VALUES ($1, $2, $3, $4)",
		normalizeEventID(eventID), eventType, payload, errMsg,
	)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDLQInsertFailed, err)
	}
	return nil
}

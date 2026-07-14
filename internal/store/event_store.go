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

func (s *PgEventStore) IsProcessed(ctx context.Context, eventID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM event_processing_log WHERE event_id = $1 AND status = 'processed')",
		eventID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrEventCheckFailed, err)
	}
	return exists, nil
}

func (s *PgEventStore) MarkProcessed(ctx context.Context, eventID string, eventType string) error {
	tag, err := s.pool.Exec(ctx,
		"INSERT INTO event_processing_log (event_id, event_type, status) VALUES ($1, $2, 'processed') ON CONFLICT (event_id) DO NOTHING",
		eventID, eventType,
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
		eventID, eventType, payload, errMsg,
	)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDLQInsertFailed, err)
	}
	return nil
}

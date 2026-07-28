// Package ingestion provides the NATS JetStream consumer adapter.
package ingestion

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// NATSConsumerConfig holds the connection and subscription parameters for
// the NATS JetStream consumer.
type NATSConsumerConfig struct {
	// URL is the NATS server URL (e.g. "nats://localhost:4222").
	URL string

	// StreamName is the JetStream stream to bind to (e.g. "SOCIAL_CARE_EVENTS").
	StreamName string

	// ConsumerName is the durable consumer name for pull-based subscriptions.
	ConsumerName string
}

// natsConsumer implements the Consumer interface using NATS JetStream
// pull-based subscriptions for backpressure control.
type natsConsumer struct {
	nc  *nats.Conn
	cfg NATSConsumerConfig
}

// NewNATSConsumer creates a Consumer that uses an existing NATS connection
// for JetStream pull-based delivery. The connection lifecycle is managed
// externally (by the caller), avoiding duplicate connections.
func NewNATSConsumer(nc *nats.Conn, cfg NATSConsumerConfig) Consumer {
	return &natsConsumer{nc: nc, cfg: cfg}
}

// Subscribe creates a pull subscription on the configured stream and delivers
// messages to the provided channel. It blocks until ctx is cancelled or a
// fatal error occurs. The caller owns the channel and must close it after
// Subscribe returns.
func (c *natsConsumer) Subscribe(ctx context.Context, out chan<- RawMessage) error {
	if c.nc == nil || !c.nc.IsConnected() {
		return fmt.Errorf("%w: connection not available", ErrConsumerConnectionFailed)
	}

	js, err := c.nc.JetStream()
	if err != nil {
		return fmt.Errorf("%w: jetstream: %v", ErrConsumerConnectionFailed, err)
	}

	// Subscribe with durable consumer. Matches subjects published by
	// svc-social-care's NATSEventPublisher: "social-care.events.<EventType>".
	//
	// NOTE: If the durable consumer already exists with a different filter
	// subject, JetStream will reject the subscription. In that case, delete
	// the old consumer first: `nats consumer rm SOCIAL_CARE_EVENTS analysis-bi`
	sub, err := js.PullSubscribe(
		"social-care.events.*",
		c.cfg.ConsumerName,
		nats.BindStream(c.cfg.StreamName),
	)
	if err != nil {
		return fmt.Errorf("%w: subscribe: %v", ErrConsumerConnectionFailed, err)
	}
	defer func() {
		_ = sub.Unsubscribe()
	}()

	// Pull loop with context cancellation and bounded fetch.
	const batchSize = 10
	const fetchTimeout = 2 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msgs, err := sub.Fetch(batchSize, nats.MaxWait(fetchTimeout))
		if err != nil {
			if err == nats.ErrTimeout {
				continue // no messages available, poll again
			}
			// Check if context was cancelled during fetch
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: fetch: %v", ErrConsumerConnectionFailed, err)
		}

		for _, msg := range msgs {
			natsMsg := msg // capture for closure
			raw := RawMessage{
				Subject: natsMsg.Subject,
				Data:    natsMsg.Data,
				Ack: func() error {
					return natsMsg.Ack()
				},
				// Term: poison message — stop redelivering. Emits the
				// MSG_TERMINATED advisory, unlike Ack.
				Term: func() error {
					return natsMsg.Term()
				},
			}

			select {
			case out <- raw:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// NATSHealthChecker wraps a NATS connection for readiness probe checks.
// It implements the api.NATSChecker interface.
type NATSHealthChecker struct {
	nc *nats.Conn
}

// NewNATSHealthChecker creates a health checker from an existing NATS
// connection. The connection is managed externally; the checker only
// inspects its status.
func NewNATSHealthChecker(nc *nats.Conn) *NATSHealthChecker {
	return &NATSHealthChecker{nc: nc}
}

// IsConnected returns true if the NATS connection is active.
func (c *NATSHealthChecker) IsConnected() bool {
	return c.nc != nil && c.nc.IsConnected()
}

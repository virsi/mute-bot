package queue

import (
	"context"
	"encoding/json"
	"fmt"
)

// Publisher marshals payloads to JSON and publishes them to JetStream with
// acknowledged delivery. It is safe for concurrent use — JetStream's Publish
// handles serialization internally.
type Publisher struct {
	c *Conn
}

// NewPublisher constructs a Publisher bound to the given connection.
func NewPublisher(c *Conn) *Publisher { return &Publisher{c: c} }

// Publish JSON-marshals payload and publishes it on subject. It returns
// once the JetStream broker has acknowledged the message — callers do not
// need to wrap with their own ack/flush.
func (p *Publisher) Publish(ctx context.Context, subject string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload for %s: %w", subject, err)
	}
	if _, err := p.c.JS().Publish(ctx, subject, data); err != nil {
		return fmt.Errorf("publish %s: %w", subject, err)
	}
	return nil
}

// PublishRaw publishes pre-encoded bytes — used by the DLQ path where the
// payload is already a JSON envelope produced internally.
func (p *Publisher) PublishRaw(ctx context.Context, subject string, data []byte) error {
	if _, err := p.c.JS().Publish(ctx, subject, data); err != nil {
		return fmt.Errorf("publish %s: %w", subject, err)
	}
	return nil
}

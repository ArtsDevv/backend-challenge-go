package ports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/events"
)

var ErrInboxHashMismatch = errors.New("ports: inbox message redelivered with a different content hash")

type InboxRecord struct {
	ConsumerName string
	MessageID    string
	MessageHash  string
	ReceivedAt   time.Time
	CompletedAt  *time.Time
}

func (r InboxRecord) Completed() bool { return r.CompletedAt != nil }

type InboxStore interface {
	ReserveOrGet(ctx context.Context, consumerName, messageID, messageHash string) (existing InboxRecord, reserved bool, err error)

	MarkCompleted(ctx context.Context, consumerName, messageID string, completedAt time.Time) error
}

type OutboxEvent struct {
	EventID       uuid.UUID
	AggregateID   uuid.UUID
	EventType     string
	CorrelationID uuid.UUID
	CausationID   *uuid.UUID
	Payload       []byte
	OccurredAt    time.Time
	Attempts      int
	NextAttemptAt time.Time
	PublishedAt   *time.Time
}

func NewOutboxEventFromEnvelope(env events.Envelope) (OutboxEvent, error) {
	payload, err := json.Marshal(env)
	if err != nil {
		return OutboxEvent{}, fmt.Errorf("ports: marshal event %s: %w", env.EventType, err)
	}
	return OutboxEvent{
		EventID:       env.EventID,
		AggregateID:   env.AggregateID,
		EventType:     env.EventType,
		CorrelationID: env.CorrelationID,
		CausationID:   env.CausationID,
		Payload:       payload,
		OccurredAt:    env.OccurredAt,
		Attempts:      0,
		NextAttemptAt: env.OccurredAt,
	}, nil
}

type OutboxStore interface {
	Enqueue(ctx context.Context, events ...OutboxEvent) error

	LockPendingBatch(ctx context.Context, limit int) ([]OutboxEvent, error)

	MarkPublished(ctx context.Context, eventID uuid.UUID, publishedAt time.Time) error

	RecordFailedAttempt(ctx context.Context, eventID uuid.UUID, nextAttemptAt time.Time) error
}

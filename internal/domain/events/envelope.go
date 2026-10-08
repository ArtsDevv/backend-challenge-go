package events

import (
	"time"

	"github.com/google/uuid"
)

type Envelope struct {
	EventID       uuid.UUID
	EventType     string
	AggregateID   uuid.UUID
	CorrelationID uuid.UUID
	CausationID   *uuid.UUID
	OccurredAt    time.Time
	Version       int
	Data          interface{}
}

func NewEnvelope(eventType string, aggregateID, correlationID uuid.UUID, causationID *uuid.UUID, occurredAt time.Time, version int, data interface{}) Envelope {
	return Envelope{
		EventID:       uuid.New(),
		EventType:     eventType,
		AggregateID:   aggregateID,
		CorrelationID: correlationID,
		CausationID:   causationID,
		OccurredAt:    occurredAt.UTC(),
		Version:       version,
		Data:          data,
	}
}

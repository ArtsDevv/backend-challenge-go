package events

import (
	"time"

	"backend-challenge-go/internal/domain/wagering"

	"github.com/google/uuid"
)

const EventTypeWagerTransactionRejected = "WagerTransactionRejected"

type WagerTransactionRejectedData struct {
	TransactionID         uuid.UUID
	ExternalTransactionID string
	ProviderID            string
	PlayerID              string
	WalletID              uuid.UUID
	Kind                  wagering.Kind
	FailureCode           wagering.FailureCode
	RejectedAt            time.Time
}

func NewWagerTransactionRejectedEnvelope(correlationID uuid.UUID, causationID *uuid.UUID, occurredAt time.Time, data WagerTransactionRejectedData) Envelope {
	return NewEnvelope(EventTypeWagerTransactionRejected, data.TransactionID, correlationID, causationID, occurredAt, 1, data)
}

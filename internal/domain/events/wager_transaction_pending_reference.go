package events

import (
	"time"

	"backend-challenge-go/internal/domain/wagering"

	"github.com/google/uuid"
)

const EventTypeWagerTransactionPendingReference = "WagerTransactionPendingReference"

type WagerTransactionPendingReferenceData struct {
	TransactionID                  uuid.UUID
	ExternalTransactionID          string
	ProviderID                     string
	PlayerID                       string
	WalletID                       uuid.UUID
	Kind                           wagering.Kind
	ReferenceExternalTransactionID string
	Attempt                        int
	PendingSince                   time.Time
}

func NewWagerTransactionPendingReferenceEnvelope(correlationID uuid.UUID, causationID *uuid.UUID, occurredAt time.Time, data WagerTransactionPendingReferenceData) Envelope {
	return NewEnvelope(EventTypeWagerTransactionPendingReference, data.TransactionID, correlationID, causationID, occurredAt, 1, data)
}

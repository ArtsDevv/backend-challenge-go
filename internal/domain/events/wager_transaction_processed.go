package events

import (
	"time"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagering"

	"github.com/google/uuid"
)

const EventTypeWagerTransactionProcessed = "WagerTransactionProcessed"

type WagerTransactionProcessedData struct {
	TransactionID          uuid.UUID
	ExternalTransactionID  string
	ProviderID             string
	PlayerID               string
	WalletID               uuid.UUID
	RoundID                string
	GameID                 string
	Kind                   wagering.Kind
	Money                  money.Money
	ReferenceTransactionID *uuid.UUID
	ResultBalanceMinor     int64
	ResultWalletVersion    int64
	ProcessedAt            time.Time
}

func NewWagerTransactionProcessedEnvelope(correlationID uuid.UUID, causationID *uuid.UUID, occurredAt time.Time, data WagerTransactionProcessedData) Envelope {
	return NewEnvelope(EventTypeWagerTransactionProcessed, data.TransactionID, correlationID, causationID, occurredAt, 1, data)
}

package events

import (
	"time"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wallet"

	"github.com/google/uuid"
)

const EventTypeWalletBalanceChanged = "WalletBalanceChanged"

type WalletBalanceChangedData struct {
	WalletID      uuid.UUID
	TransactionID uuid.UUID
	Direction     wallet.Direction
	Money         money.Money
	BalanceBefore int64
	BalanceAfter  int64
	WalletVersion int64
	ChangedAt     time.Time
}

func NewWalletBalanceChangedEnvelope(correlationID uuid.UUID, causationID *uuid.UUID, occurredAt time.Time, data WalletBalanceChangedData) Envelope {
	return NewEnvelope(EventTypeWalletBalanceChanged, data.WalletID, correlationID, causationID, occurredAt, 1, data)
}

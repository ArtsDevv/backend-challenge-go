package wagering

import (
	"fmt"
	"time"

	"backend-challenge-go/internal/domain/money"

	"github.com/google/uuid"
)

type Kind string

const (
	KindOpening  Kind = "OPENING"
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

var externalKinds = map[Kind]bool{
	KindBet:      true,
	KindWin:      true,
	KindLoss:     true,
	KindRefund:   true,
	KindRollback: true,
}

func (k Kind) IsReversal() bool {
	return k == KindRefund || k == KindRollback
}

type WagerTransaction struct {
	internalID uuid.UUID

	externalTransactionID string
	providerID            string
	idempotencyKey        string
	payloadHash           string

	walletID uuid.UUID
	playerID string
	roundID  string
	gameID   string

	kind  Kind
	money money.Money

	referenceExternalTransactionID string
	referenceTransactionID         *uuid.UUID

	status      Status
	failureCode *FailureCode

	resultBalanceMinor  *int64
	resultWalletVersion *int64

	pendingReferenceAttempts      int
	pendingReferenceNextAttemptAt *time.Time
	pendingSince                  *time.Time

	createdAt   time.Time
	updatedAt   time.Time
	processedAt *time.Time
}

func (t *WagerTransaction) InternalID() uuid.UUID { return t.internalID }

func (t *WagerTransaction) ExternalTransactionID() string { return t.externalTransactionID }

func (t *WagerTransaction) ProviderID() string { return t.providerID }

func (t *WagerTransaction) IdempotencyKey() string { return t.idempotencyKey }

func (t *WagerTransaction) PayloadHash() string { return t.payloadHash }

func (t *WagerTransaction) WalletID() uuid.UUID { return t.walletID }

func (t *WagerTransaction) PlayerID() string { return t.playerID }

func (t *WagerTransaction) RoundID() string { return t.roundID }

func (t *WagerTransaction) GameID() string { return t.gameID }

func (t *WagerTransaction) Kind() Kind { return t.kind }

func (t *WagerTransaction) Money() money.Money { return t.money }

func (t *WagerTransaction) ReferenceExternalTransactionID() string {
	return t.referenceExternalTransactionID
}

func (t *WagerTransaction) ReferenceTransactionID() *uuid.UUID { return t.referenceTransactionID }

func (t *WagerTransaction) Status() Status { return t.status }

func (t *WagerTransaction) FailureCode() *FailureCode { return t.failureCode }

func (t *WagerTransaction) ResultBalanceMinor() *int64 { return t.resultBalanceMinor }

func (t *WagerTransaction) ResultWalletVersion() *int64 { return t.resultWalletVersion }

func (t *WagerTransaction) PendingReferenceAttempts() int { return t.pendingReferenceAttempts }

func (t *WagerTransaction) PendingReferenceNextAttemptAt() *time.Time {
	return t.pendingReferenceNextAttemptAt
}

func (t *WagerTransaction) PendingSince() *time.Time { return t.pendingSince }

func (t *WagerTransaction) CreatedAt() time.Time { return t.createdAt }

func (t *WagerTransaction) UpdatedAt() time.Time { return t.updatedAt }

func (t *WagerTransaction) ProcessedAt() *time.Time { return t.processedAt }

type RehydrateParams struct {
	InternalID                     uuid.UUID
	ExternalTransactionID          string
	ProviderID                     string
	IdempotencyKey                 string
	PayloadHash                    string
	WalletID                       uuid.UUID
	PlayerID                       string
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
	ReferenceTransactionID         *uuid.UUID
	Status                         Status
	FailureCode                    *FailureCode
	ResultBalanceMinor             *int64
	ResultWalletVersion            *int64
	PendingReferenceAttempts       int
	PendingReferenceNextAttemptAt  *time.Time
	PendingSince                   *time.Time
	CreatedAt                      time.Time
	UpdatedAt                      time.Time
	ProcessedAt                    *time.Time
}

func Rehydrate(p RehydrateParams) (*WagerTransaction, error) {
	if p.InternalID == uuid.Nil {
		return nil, fmt.Errorf("%w: internalId", ErrMissingRequiredField)
	}
	if p.WalletID == uuid.Nil {
		return nil, fmt.Errorf("%w: walletId", ErrMissingRequiredField)
	}
	if p.PlayerID == "" {
		return nil, fmt.Errorf("%w: playerId", ErrMissingRequiredField)
	}
	if p.CreatedAt.IsZero() {
		return nil, fmt.Errorf("%w: createdAt", ErrMissingRequiredField)
	}

	return &WagerTransaction{
		internalID:                     p.InternalID,
		externalTransactionID:          p.ExternalTransactionID,
		providerID:                     p.ProviderID,
		idempotencyKey:                 p.IdempotencyKey,
		payloadHash:                    p.PayloadHash,
		walletID:                       p.WalletID,
		playerID:                       p.PlayerID,
		roundID:                        p.RoundID,
		gameID:                         p.GameID,
		kind:                           p.Kind,
		money:                          p.Money,
		referenceExternalTransactionID: p.ReferenceExternalTransactionID,
		referenceTransactionID:         p.ReferenceTransactionID,
		status:                         p.Status,
		failureCode:                    p.FailureCode,
		resultBalanceMinor:             p.ResultBalanceMinor,
		resultWalletVersion:            p.ResultWalletVersion,
		pendingReferenceAttempts:       p.PendingReferenceAttempts,
		pendingReferenceNextAttemptAt:  p.PendingReferenceNextAttemptAt,
		pendingSince:                   p.PendingSince,
		createdAt:                      p.CreatedAt,
		updatedAt:                      p.UpdatedAt,
		processedAt:                    p.ProcessedAt,
	}, nil
}

type NewExternalTransactionParams struct {
	InternalID                     uuid.UUID
	ExternalTransactionID          string
	ProviderID                     string
	IdempotencyKey                 string
	PayloadHash                    string
	WalletID                       uuid.UUID
	PlayerID                       string
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
	Now                            time.Time
}

func NewExternalTransaction(p NewExternalTransactionParams) (*WagerTransaction, error) {
	if p.Kind == KindOpening || !externalKinds[p.Kind] {
		return nil, fmt.Errorf("%w: %q", ErrInvalidKindForExternal, p.Kind)
	}

	switch {
	case p.ExternalTransactionID == "":
		return nil, fmt.Errorf("%w: externalTransactionId", ErrMissingRequiredField)
	case p.ProviderID == "":
		return nil, fmt.Errorf("%w: providerId", ErrMissingRequiredField)
	case p.IdempotencyKey == "":
		return nil, fmt.Errorf("%w: idempotencyKey", ErrMissingRequiredField)
	case p.PayloadHash == "":
		return nil, fmt.Errorf("%w: payloadHash", ErrMissingRequiredField)
	case p.PlayerID == "":
		return nil, fmt.Errorf("%w: playerId", ErrMissingRequiredField)
	case p.WalletID == uuid.Nil:
		return nil, fmt.Errorf("%w: walletId", ErrMissingRequiredField)
	}

	if err := validateAmountForKind(p.Kind, p.Money); err != nil {
		return nil, err
	}

	if p.Kind.IsReversal() && p.ReferenceExternalTransactionID == "" {
		return nil, ErrReversalRequiresReference
	}

	id := p.InternalID
	if id == uuid.Nil {
		id = uuid.New()
	}
	now := p.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	return &WagerTransaction{
		internalID:                     id,
		externalTransactionID:          p.ExternalTransactionID,
		providerID:                     p.ProviderID,
		idempotencyKey:                 p.IdempotencyKey,
		payloadHash:                    p.PayloadHash,
		walletID:                       p.WalletID,
		playerID:                       p.PlayerID,
		roundID:                        p.RoundID,
		gameID:                         p.GameID,
		kind:                           p.Kind,
		money:                          p.Money,
		referenceExternalTransactionID: p.ReferenceExternalTransactionID,
		status:                         StatusPending,
		createdAt:                      now,
		updatedAt:                      now,
	}, nil
}

type NewOpeningTransactionParams struct {
	InternalID uuid.UUID
	WalletID   uuid.UUID
	PlayerID   string
	Money      money.Money
	Now        time.Time
}

func NewOpeningTransaction(p NewOpeningTransactionParams) (*WagerTransaction, error) {
	if p.WalletID == uuid.Nil {
		return nil, fmt.Errorf("%w: walletId", ErrMissingRequiredField)
	}
	if p.PlayerID == "" {
		return nil, fmt.Errorf("%w: playerId", ErrMissingRequiredField)
	}
	if err := validateAmountForKind(KindOpening, p.Money); err != nil {
		return nil, err
	}

	id := p.InternalID
	if id == uuid.Nil {
		id = uuid.New()
	}
	now := p.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	return &WagerTransaction{
		internalID: id,
		walletID:   p.WalletID,
		playerID:   p.PlayerID,
		kind:       KindOpening,
		money:      p.Money,
		status:     StatusPending,
		createdAt:  now,
		updatedAt:  now,
	}, nil
}

func validateAmountForKind(kind Kind, m money.Money) error {
	switch kind {
	case KindLoss:
		if !m.IsZero() {
			return &InvalidAmountForKindError{Kind: kind, Reason: "LOSS must have a zero amount"}
		}
	case KindBet, KindWin, KindRefund, KindRollback:
		if !m.IsPositive() {
			return &InvalidAmountForKindError{Kind: kind, Reason: "amount must be strictly positive"}
		}
	case KindOpening:
		if m.IsNegative() {
			return &InvalidAmountForKindError{Kind: kind, Reason: "amount must not be negative"}
		}
	default:
		return &InvalidAmountForKindError{Kind: kind, Reason: "unknown kind"}
	}
	return nil
}

func (t *WagerTransaction) ValidateReversalAgainst(reference *WagerTransaction) error {
	if !t.kind.IsReversal() {
		return &InvalidAmountForKindError{Kind: t.kind, Reason: "not a reversal kind"}
	}
	if reference == nil {
		return ErrReferenceNotFound
	}
	if reference.status != StatusProcessed {
		return ErrReferenceNotTerminal
	}

	switch t.kind {
	case KindRefund:
		if reference.kind != KindBet {
			return ErrReferenceMismatch
		}
	case KindRollback:
		if reference.kind != KindBet && reference.kind != KindWin && reference.kind != KindRefund {
			return ErrReferenceMismatch
		}
	}

	if reference.providerID != t.providerID ||
		reference.playerID != t.playerID ||
		reference.walletID != t.walletID ||
		reference.roundID != t.roundID ||
		reference.money.Currency() != t.money.Currency() {
		return ErrReferenceMismatch
	}

	if !reference.money.Equal(t.money) {
		return ErrReferenceAmountMismatch
	}

	return nil
}

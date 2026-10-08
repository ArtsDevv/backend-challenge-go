package http

import (
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/money"
)

type OpenWalletRequest struct {
	PlayerID       string      `json:"playerId"`
	InitialBalance money.Money `json:"initialBalance"`
}

type WalletResponse struct {
	ID       uuid.UUID   `json:"id"`
	PlayerID string      `json:"playerId"`
	Balance  money.Money `json:"balance"`
	Version  int64       `json:"version"`
}

// --- GET /wallets/:walletId/ledger ---------------------------------------------------------

type LedgerEntryResponse struct {
	ID            uuid.UUID   `json:"id"`
	WalletID      uuid.UUID   `json:"walletId"`
	TransactionID uuid.UUID   `json:"transactionId"`
	Direction     string      `json:"direction"`
	Amount        money.Money `json:"amount"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	CreatedAt     time.Time   `json:"createdAt"`
}
type LedgerPageResponse struct {
	Entries    []LedgerEntryResponse `json:"entries"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

// --- POST /wagering/transactions -----------------------------------------------------------

type ProcessTransactionRequest struct {
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	PlayerID                       string      `json:"playerId"`
	WalletID                       uuid.UUID   `json:"walletId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Kind                           string      `json:"kind"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
}
type ProcessTransactionResponse struct {
	TransactionID    uuid.UUID    `json:"transactionId"`
	Status           string       `json:"status"`
	Balance          *money.Money `json:"balance,omitempty"`
	WalletVersion    *int64       `json:"walletVersion,omitempty"`
	FailureCode      *string      `json:"failureCode,omitempty"`
	IdempotentReplay bool         `json:"idempotentReplay"`
}

// --- GET /wagering/transactions/:transactionId and
//     GET /providers/:providerId/wagering/transactions/:externalTransactionId ----------------

type WagerTransactionResponse struct {
	TransactionID                  uuid.UUID    `json:"transactionId"`
	ExternalTransactionID          string       `json:"externalTransactionId,omitempty"`
	ProviderID                     string       `json:"providerId,omitempty"`
	PlayerID                       string       `json:"playerId"`
	WalletID                       uuid.UUID    `json:"walletId"`
	RoundID                        string       `json:"roundId,omitempty"`
	GameID                         string       `json:"gameId,omitempty"`
	Kind                           string       `json:"kind"`
	Money                          money.Money  `json:"money"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId,omitempty"`
	Status                         string       `json:"status"`
	FailureCode                    *string      `json:"failureCode,omitempty"`
	ResultBalance                  *money.Money `json:"resultBalance,omitempty"`
	ResultWalletVersion            *int64       `json:"resultWalletVersion,omitempty"`
	CreatedAt                      time.Time    `json:"createdAt"`
	UpdatedAt                      time.Time    `json:"updatedAt"`
	ProcessedAt                    *time.Time   `json:"processedAt,omitempty"`
}

// --- POST /wallets/:walletId/reconciliation --------------------------------------------------

type ReconciliationResponse struct {
	WalletID          uuid.UUID   `json:"walletId"`
	StoredBalance     money.Money `json:"storedBalance"`
	CalculatedBalance money.Money `json:"calculatedBalance"`
	Difference        money.Money `json:"difference"`
	Consistent        bool        `json:"consistent"`
	CheckedEntries    int         `json:"checkedEntries"`
}

// --- GET /health/live, GET /health/ready -----------------------------------------------------

type HealthStatusResponse struct {
	Status string `json:"status"`
}
type ReadinessResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

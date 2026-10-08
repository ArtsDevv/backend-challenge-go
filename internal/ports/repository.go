package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/wagering"
	"backend-challenge-go/internal/domain/wallet"
)

var (
	ErrNotFound = errors.New("ports: not found")

	ErrWalletLockTimeout = errors.New("ports: wallet lock timeout")

	ErrSerializationFailure = errors.New("ports: serialization failure")

	ErrRepositoryUnavailable = errors.New("ports: repository temporarily unavailable")

	ErrDuplicateLedgerEntry = errors.New("ports: duplicate ledger entry for (walletId, transactionId)")

	ErrExternalTransactionAlreadyExists = errors.New("ports: (providerId, externalTransactionId) already used under a different idempotency key")

	ErrIdempotencyKeyConflict = errors.New("ports: idempotency key reused with a different payload")
)

type UnitOfWork interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

type TxManager = UnitOfWork

type WalletRepository interface {
	LockForUpdate(ctx context.Context, walletID uuid.UUID) (*wallet.Wallet, error)

	FindByID(ctx context.Context, walletID uuid.UUID) (*wallet.Wallet, error)

	FindByPlayerAndCurrency(ctx context.Context, playerID, currency string) (*wallet.Wallet, error)

	Insert(ctx context.Context, w *wallet.Wallet) error

	Update(ctx context.Context, w *wallet.Wallet, expectedVersion int64) error
}

type TransactionRepository interface {
	InsertIfAbsent(ctx context.Context, tx *wagering.WagerTransaction) (inserted bool, err error)

	Insert(ctx context.Context, tx *wagering.WagerTransaction) error

	FindByID(ctx context.Context, transactionID uuid.UUID) (*wagering.WagerTransaction, error)

	FindByProviderAndIdempotencyKey(ctx context.Context, providerID, idempotencyKey string) (*wagering.WagerTransaction, error)

	FindByProviderAndExternalTransactionID(ctx context.Context, providerID, externalTransactionID string) (*wagering.WagerTransaction, error)

	LockForUpdate(ctx context.Context, transactionID uuid.UUID) (*wagering.WagerTransaction, error)

	HasSuccessfulReversal(ctx context.Context, providerID, referenceExternalTransactionID string, kind wagering.Kind) (bool, error)

	Update(ctx context.Context, tx *wagering.WagerTransaction) error

	LockPendingReferenceBatch(ctx context.Context, limit int) ([]*wagering.WagerTransaction, error)
}

type LedgerRepository interface {
	Insert(ctx context.Context, entry wallet.WalletLedgerEntry) error

	ListByWallet(ctx context.Context, walletID uuid.UUID, cursor string, limit int) (entries []wallet.WalletLedgerEntry, nextCursor string, err error)
}

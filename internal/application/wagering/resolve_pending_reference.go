package wagering

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/domain/wagering"
	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/ports"
)

type ResolvePendingReferenceUseCase struct {
	persistence
	Now    func() time.Time
	Config config.PendingReferenceConfig
}

func NewResolvePendingReferenceUseCase(
	wallets ports.WalletRepository,
	transactions ports.TransactionRepository,
	ledger ports.LedgerRepository,
	outbox ports.OutboxStore,
	cfg config.PendingReferenceConfig,
) *ResolvePendingReferenceUseCase {
	return &ResolvePendingReferenceUseCase{
		persistence: persistence{Wallets: wallets, Transactions: transactions, Ledger: ledger, Outbox: outbox},
		Now:         func() time.Time { return time.Now().UTC() },
		Config:      cfg,
	}
}

func (uc *ResolvePendingReferenceUseCase) Resolve(ctx context.Context, tx *wagering.WagerTransaction) error {
	now := uc.Now()
	correlationID := tx.InternalID()

	reference, err := uc.Transactions.FindByProviderAndExternalTransactionID(ctx, tx.ProviderID(), tx.ReferenceExternalTransactionID())
	if err != nil {
		if !errors.Is(err, ports.ErrNotFound) {
			return err
		}
		return uc.retryOrGiveUp(ctx, tx, correlationID, now)
	}

	lockedReference, err := uc.Transactions.LockForUpdate(ctx, reference.InternalID())
	if err != nil {
		return err
	}

	if validateErr := tx.ValidateReversalAgainst(lockedReference); validateErr != nil {
		_, err := uc.rejectAndReturn(ctx, tx, nil, reversalFailureCode(validateErr), correlationID, now)
		return err
	}

	alreadyReversed, err := uc.Transactions.HasSuccessfulReversal(ctx, tx.ProviderID(), tx.ReferenceExternalTransactionID(), tx.Kind())
	if err != nil {
		return err
	}
	if alreadyReversed {
		_, err := uc.rejectAndReturn(ctx, tx, nil, wagering.FailureCodeReferenceAlreadyReversed, correlationID, now)
		return err
	}

	w, err := uc.Wallets.LockForUpdate(ctx, tx.WalletID())
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			_, err := uc.rejectAndReturn(ctx, tx, nil, wagering.FailureCodeWalletNotFound, correlationID, now)
			return err
		}
		return err
	}
	if w.Currency() != tx.Money().Currency() {
		_, err := uc.rejectAndReturn(ctx, tx, w, wagering.FailureCodeCurrencyMismatch, correlationID, now)
		return err
	}

	if err := tx.ResolveReference(lockedReference.InternalID(), now); err != nil {
		return err
	}

	entry, err := applyReversal(w, tx, lockedReference.Kind(), now)
	if err != nil {
		var insufficient *wallet.InsufficientFundsError
		if errors.As(err, &insufficient) {
			_, err := uc.rejectAndReturn(ctx, tx, w, wagering.FailureCodeInsufficientBalanceForReversal, correlationID, now)
			return err
		}
		return err
	}

	_, err = uc.commitMovement(ctx, tx, w, entry, correlationID, now)
	return err
}

func (uc *ResolvePendingReferenceUseCase) retryOrGiveUp(ctx context.Context, tx *wagering.WagerTransaction, correlationID uuid.UUID, now time.Time) error {
	if tx.PendingReferenceExceeded(uc.Config.MaxAttempts, uc.Config.TTL, now) {
		_, err := uc.rejectAndReturn(ctx, tx, nil, wagering.FailureCodeReferenceNotFound, correlationID, now)
		return err
	}

	nextAttempt := computeNextAttempt(uc.Config.Backoff, tx.PendingReferenceAttempts(), now)
	if err := tx.RecordPendingReferenceAttempt(nextAttempt, now); err != nil {
		return err
	}
	return uc.Transactions.Update(ctx, tx)
}

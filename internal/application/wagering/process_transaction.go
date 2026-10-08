package wagering

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/domain/events"
	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagering"
	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/ports"
)

type CanonicalHashInput struct {
	ProviderID                     string
	ExternalTransactionID          string
	PlayerID                       string
	WalletID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           wagering.Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
}

func CanonicalHash(in CanonicalHashInput) (string, error) {
	canonical := map[string]interface{}{
		"providerId":                     in.ProviderID,
		"externalTransactionId":          in.ExternalTransactionID,
		"playerId":                       in.PlayerID,
		"walletId":                       in.WalletID.String(),
		"roundId":                        in.RoundID,
		"gameId":                         in.GameID,
		"kind":                           string(in.Kind),
		"money":                          in.Money, // money.Money.MarshalJSON -> {"amount":"25.00","currency":"BRL"}
		"referenceExternalTransactionId": in.ReferenceExternalTransactionID,
	}

	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("canonical hash: marshal payload: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

type ExternalTransactionAlreadyExistsError struct {
	ProviderID            string
	ExternalTransactionID string
}

func (e *ExternalTransactionAlreadyExistsError) Error() string {
	return fmt.Sprintf("process_transaction: (providerId=%s, externalTransactionId=%s) already used under a different idempotency key", e.ProviderID, e.ExternalTransactionID)
}

func (e *ExternalTransactionAlreadyExistsError) Unwrap() error {
	return ports.ErrExternalTransactionAlreadyExists
}

type IdempotencyKeyConflictError struct {
	ProviderID     string
	IdempotencyKey string
}

func (e *IdempotencyKeyConflictError) Error() string {
	return fmt.Sprintf("process_transaction: idempotency key %q for provider %q was reused with a different payload", e.IdempotencyKey, e.ProviderID)
}

func (e *IdempotencyKeyConflictError) Unwrap() error { return ports.ErrIdempotencyKeyConflict }

type ProcessTransactionCommand struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       string
	WalletID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           wagering.Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
	CorrelationID                  uuid.UUID
}

type ProcessTransactionResult struct {
	TransactionID    uuid.UUID
	Status           wagering.Status
	Balance          money.Money
	WalletVersion    int64
	FailureCode      *wagering.FailureCode
	IdempotentReplay bool
}

type persistence struct {
	Wallets      ports.WalletRepository
	Transactions ports.TransactionRepository
	Ledger       ports.LedgerRepository
	Outbox       ports.OutboxStore
}

type ProcessTransactionUseCase struct {
	persistence
	UoW                     ports.UnitOfWork
	Now                     func() time.Time
	PendingReferenceBackoff config.BackoffConfig
}

func NewProcessTransactionUseCase(
	uow ports.UnitOfWork,
	wallets ports.WalletRepository,
	transactions ports.TransactionRepository,
	ledger ports.LedgerRepository,
	outbox ports.OutboxStore,
	pendingReferenceBackoff config.BackoffConfig,
) *ProcessTransactionUseCase {
	return &ProcessTransactionUseCase{
		persistence:             persistence{Wallets: wallets, Transactions: transactions, Ledger: ledger, Outbox: outbox},
		UoW:                     uow,
		Now:                     func() time.Time { return time.Now().UTC() },
		PendingReferenceBackoff: pendingReferenceBackoff,
	}
}

func (uc *ProcessTransactionUseCase) Execute(ctx context.Context, cmd ProcessTransactionCommand) (ProcessTransactionResult, error) {
	now := uc.Now()

	hash, err := CanonicalHash(CanonicalHashInput{
		ProviderID:                     cmd.ProviderID,
		ExternalTransactionID:          cmd.ExternalTransactionID,
		PlayerID:                       cmd.PlayerID,
		WalletID:                       cmd.WalletID,
		RoundID:                        cmd.RoundID,
		GameID:                         cmd.GameID,
		Kind:                           cmd.Kind,
		Money:                          cmd.Money,
		ReferenceExternalTransactionID: cmd.ReferenceExternalTransactionID,
	})
	if err != nil {
		return ProcessTransactionResult{}, err
	}

	tx, err := wagering.NewExternalTransaction(wagering.NewExternalTransactionParams{
		ExternalTransactionID:          cmd.ExternalTransactionID,
		ProviderID:                     cmd.ProviderID,
		IdempotencyKey:                 cmd.IdempotencyKey,
		PayloadHash:                    hash,
		WalletID:                       cmd.WalletID,
		PlayerID:                       cmd.PlayerID,
		RoundID:                        cmd.RoundID,
		GameID:                         cmd.GameID,
		Kind:                           cmd.Kind,
		Money:                          cmd.Money,
		ReferenceExternalTransactionID: cmd.ReferenceExternalTransactionID,
		Now:                            now,
	})
	if err != nil {
		return ProcessTransactionResult{}, err
	}

	correlationID := cmd.CorrelationID
	if correlationID == uuid.Nil {
		correlationID = uuid.New()
	}

	var result ProcessTransactionResult
	err = uc.UoW.WithinTx(ctx, func(ctx context.Context) error {
		existingByExternalID, err := uc.Transactions.FindByProviderAndExternalTransactionID(ctx, cmd.ProviderID, cmd.ExternalTransactionID)
		switch {
		case err == nil:
			if existingByExternalID.IdempotencyKey() != cmd.IdempotencyKey {
				return &ExternalTransactionAlreadyExistsError{ProviderID: cmd.ProviderID, ExternalTransactionID: cmd.ExternalTransactionID}
			}
		case errors.Is(err, ports.ErrNotFound):
		default:
			return err
		}

		inserted, err := uc.Transactions.InsertIfAbsent(ctx, tx)
		if err != nil {
			return err
		}
		if !inserted {
			existing, err := uc.Transactions.FindByProviderAndIdempotencyKey(ctx, cmd.ProviderID, cmd.IdempotencyKey)
			if err != nil {
				return err
			}
			if existing.PayloadHash() != hash {
				return &IdempotencyKeyConflictError{ProviderID: cmd.ProviderID, IdempotencyKey: cmd.IdempotencyKey}
			}
			replay, err := toReplayResult(existing)
			if err != nil {
				return err
			}
			result = replay
			return nil
		}

		outcome, err := uc.dispatch(ctx, tx, correlationID, now)
		if err != nil {
			return err
		}
		result = outcome
		return nil
	})
	if err != nil {
		return ProcessTransactionResult{}, err
	}
	return result, nil
}

func toReplayResult(existing *wagering.WagerTransaction) (ProcessTransactionResult, error) {
	result := ProcessTransactionResult{
		TransactionID:    existing.InternalID(),
		Status:           existing.Status(),
		FailureCode:      existing.FailureCode(),
		IdempotentReplay: true,
	}
	if existing.ResultBalanceMinor() != nil && existing.ResultWalletVersion() != nil {
		balance, err := money.FromMinorUnits(*existing.ResultBalanceMinor(), existing.Money().Currency())
		if err != nil {
			return ProcessTransactionResult{}, err
		}
		result.Balance = balance
		result.WalletVersion = *existing.ResultWalletVersion()
	}
	return result, nil
}

func (uc *ProcessTransactionUseCase) dispatch(ctx context.Context, tx *wagering.WagerTransaction, correlationID uuid.UUID, now time.Time) (ProcessTransactionResult, error) {
	w, err := uc.Wallets.LockForUpdate(ctx, tx.WalletID())
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			return uc.rejectAndReturn(ctx, tx, nil, wagering.FailureCodeWalletNotFound, correlationID, now)
		}
		return ProcessTransactionResult{}, err
	}
	if w.Currency() != tx.Money().Currency() {
		return uc.rejectAndReturn(ctx, tx, w, wagering.FailureCodeCurrencyMismatch, correlationID, now)
	}

	switch tx.Kind() {
	case wagering.KindBet:
		entry, err := w.Debit(tx.Money(), tx.InternalID(), now)
		if err != nil {
			var insufficient *wallet.InsufficientFundsError
			if errors.As(err, &insufficient) {
				return uc.rejectAndReturn(ctx, tx, w, wagering.FailureCodeInsufficientBalance, correlationID, now)
			}
			return ProcessTransactionResult{}, err
		}
		return uc.commitMovement(ctx, tx, w, entry, correlationID, now)

	case wagering.KindWin:
		entry, err := w.Credit(tx.Money(), tx.InternalID(), now)
		if err != nil {
			return ProcessTransactionResult{}, err
		}
		return uc.commitMovement(ctx, tx, w, entry, correlationID, now)

	case wagering.KindLoss:
		return uc.commitLoss(ctx, tx, w, correlationID, now)

	case wagering.KindRefund, wagering.KindRollback:
		return uc.processReversal(ctx, tx, w, correlationID, now)

	default:
		return ProcessTransactionResult{}, fmt.Errorf("process_transaction: unsupported kind %q", tx.Kind())
	}
}

func (uc *ProcessTransactionUseCase) commitLoss(ctx context.Context, tx *wagering.WagerTransaction, w *wallet.Wallet, correlationID uuid.UUID, now time.Time) (ProcessTransactionResult, error) {
	if err := tx.MarkProcessed(w.Balance().MinorUnits(), w.Version(), now); err != nil {
		return ProcessTransactionResult{}, err
	}
	if err := uc.Transactions.Update(ctx, tx); err != nil {
		return ProcessTransactionResult{}, err
	}

	env := events.NewWagerTransactionProcessedEnvelope(correlationID, nil, now, events.WagerTransactionProcessedData{
		TransactionID:          tx.InternalID(),
		ExternalTransactionID:  tx.ExternalTransactionID(),
		ProviderID:             tx.ProviderID(),
		PlayerID:               tx.PlayerID(),
		WalletID:               tx.WalletID(),
		RoundID:                tx.RoundID(),
		GameID:                 tx.GameID(),
		Kind:                   tx.Kind(),
		Money:                  tx.Money(),
		ReferenceTransactionID: tx.ReferenceTransactionID(),
		ResultBalanceMinor:     *tx.ResultBalanceMinor(),
		ResultWalletVersion:    *tx.ResultWalletVersion(),
		ProcessedAt:            *tx.ProcessedAt(),
	})
	if err := uc.enqueueEvents(ctx, env); err != nil {
		return ProcessTransactionResult{}, err
	}

	return ProcessTransactionResult{
		TransactionID: tx.InternalID(),
		Status:        tx.Status(),
		Balance:       w.Balance(),
		WalletVersion: w.Version(),
	}, nil
}

func (uc *ProcessTransactionUseCase) processReversal(ctx context.Context, tx *wagering.WagerTransaction, w *wallet.Wallet, correlationID uuid.UUID, now time.Time) (ProcessTransactionResult, error) {
	reference, err := uc.Transactions.FindByProviderAndExternalTransactionID(ctx, tx.ProviderID(), tx.ReferenceExternalTransactionID())
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			return uc.enterPendingReference(ctx, tx, w, correlationID, now)
		}
		return ProcessTransactionResult{}, err
	}

	lockedReference, err := uc.Transactions.LockForUpdate(ctx, reference.InternalID())
	if err != nil {
		return ProcessTransactionResult{}, err
	}

	if validateErr := tx.ValidateReversalAgainst(lockedReference); validateErr != nil {
		return uc.rejectAndReturn(ctx, tx, w, reversalFailureCode(validateErr), correlationID, now)
	}

	alreadyReversed, err := uc.Transactions.HasSuccessfulReversal(ctx, tx.ProviderID(), tx.ReferenceExternalTransactionID(), tx.Kind())
	if err != nil {
		return ProcessTransactionResult{}, err
	}
	if alreadyReversed {
		return uc.rejectAndReturn(ctx, tx, w, wagering.FailureCodeReferenceAlreadyReversed, correlationID, now)
	}

	if err := tx.ResolveReference(lockedReference.InternalID(), now); err != nil {
		return ProcessTransactionResult{}, err
	}

	entry, err := applyReversal(w, tx, lockedReference.Kind(), now)
	if err != nil {
		var insufficient *wallet.InsufficientFundsError
		if errors.As(err, &insufficient) {
			return uc.rejectAndReturn(ctx, tx, w, wagering.FailureCodeInsufficientBalanceForReversal, correlationID, now)
		}
		return ProcessTransactionResult{}, err
	}
	return uc.commitMovement(ctx, tx, w, entry, correlationID, now)
}

func (uc *ProcessTransactionUseCase) enterPendingReference(ctx context.Context, tx *wagering.WagerTransaction, w *wallet.Wallet, correlationID uuid.UUID, now time.Time) (ProcessTransactionResult, error) {
	if err := tx.MarkPendingReference(now); err != nil {
		return ProcessTransactionResult{}, err
	}
	nextAttempt := computeNextAttempt(uc.PendingReferenceBackoff, tx.PendingReferenceAttempts(), now)
	if err := tx.RecordPendingReferenceAttempt(nextAttempt, now); err != nil {
		return ProcessTransactionResult{}, err
	}
	if err := uc.Transactions.Update(ctx, tx); err != nil {
		return ProcessTransactionResult{}, err
	}

	env := events.NewWagerTransactionPendingReferenceEnvelope(correlationID, nil, now, events.WagerTransactionPendingReferenceData{
		TransactionID:                  tx.InternalID(),
		ExternalTransactionID:          tx.ExternalTransactionID(),
		ProviderID:                     tx.ProviderID(),
		PlayerID:                       tx.PlayerID(),
		WalletID:                       tx.WalletID(),
		Kind:                           tx.Kind(),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
		Attempt:                        tx.PendingReferenceAttempts(),
		PendingSince:                   *tx.PendingSince(),
	})
	if err := uc.enqueueEvents(ctx, env); err != nil {
		return ProcessTransactionResult{}, err
	}

	return ProcessTransactionResult{
		TransactionID: tx.InternalID(),
		Status:        tx.Status(),
		Balance:       w.Balance(),
		WalletVersion: w.Version(),
	}, nil
}

func applyReversal(w *wallet.Wallet, tx *wagering.WagerTransaction, referenceKind wagering.Kind, now time.Time) (wallet.WalletLedgerEntry, error) {
	if referenceKind == wagering.KindWin || referenceKind == wagering.KindRefund {
		return w.Debit(tx.Money(), tx.InternalID(), now)
	}
	return w.Credit(tx.Money(), tx.InternalID(), now)
}

func reversalFailureCode(err error) wagering.FailureCode {
	switch {
	case errors.Is(err, wagering.ErrReferenceNotTerminal):
		return wagering.FailureCodeReferenceNotTerminal
	case errors.Is(err, wagering.ErrReferenceAmountMismatch):
		return wagering.FailureCodeReferenceAmountMismatch
	default:
		return wagering.FailureCodeReferenceMismatch
	}
}

func computeNextAttempt(backoff config.BackoffConfig, attempt int, now time.Time) time.Time {
	interval := float64(backoff.BaseInterval) * math.Pow(backoff.Factor, float64(attempt))
	if capped := float64(backoff.MaxInterval); interval > capped {
		interval = capped
	}
	jitterRange := interval * backoff.JitterFraction
	jitter := (rand.Float64()*2 - 1) * jitterRange // +/- jitterRange
	delay := time.Duration(interval + jitter)
	if delay < 0 {
		delay = 0
	}
	return now.Add(delay)
}

func (p *persistence) commitMovement(ctx context.Context, tx *wagering.WagerTransaction, w *wallet.Wallet, entry wallet.WalletLedgerEntry, correlationID uuid.UUID, now time.Time) (ProcessTransactionResult, error) {
	if err := p.Wallets.Update(ctx, w, w.Version()-1); err != nil {
		return ProcessTransactionResult{}, err
	}
	if err := p.Ledger.Insert(ctx, entry); err != nil {
		return ProcessTransactionResult{}, err
	}
	if err := tx.MarkProcessed(w.Balance().MinorUnits(), w.Version(), now); err != nil {
		return ProcessTransactionResult{}, err
	}
	if err := p.Transactions.Update(ctx, tx); err != nil {
		return ProcessTransactionResult{}, err
	}

	processedEvent := events.NewWagerTransactionProcessedEnvelope(correlationID, nil, now, events.WagerTransactionProcessedData{
		TransactionID:          tx.InternalID(),
		ExternalTransactionID:  tx.ExternalTransactionID(),
		ProviderID:             tx.ProviderID(),
		PlayerID:               tx.PlayerID(),
		WalletID:               tx.WalletID(),
		RoundID:                tx.RoundID(),
		GameID:                 tx.GameID(),
		Kind:                   tx.Kind(),
		Money:                  tx.Money(),
		ReferenceTransactionID: tx.ReferenceTransactionID(),
		ResultBalanceMinor:     *tx.ResultBalanceMinor(),
		ResultWalletVersion:    *tx.ResultWalletVersion(),
		ProcessedAt:            *tx.ProcessedAt(),
	})
	balanceEvent := events.NewWalletBalanceChangedEnvelope(correlationID, &processedEvent.EventID, now, events.WalletBalanceChangedData{
		WalletID:      w.ID(),
		TransactionID: tx.InternalID(),
		Direction:     entry.Direction(),
		Money:         entry.Amount(),
		BalanceBefore: entry.BalanceBefore().MinorUnits(),
		BalanceAfter:  entry.BalanceAfter().MinorUnits(),
		WalletVersion: w.Version(),
		ChangedAt:     now,
	})
	if err := p.enqueueEvents(ctx, processedEvent, balanceEvent); err != nil {
		return ProcessTransactionResult{}, err
	}

	return ProcessTransactionResult{
		TransactionID: tx.InternalID(),
		Status:        tx.Status(),
		Balance:       w.Balance(),
		WalletVersion: w.Version(),
	}, nil
}

func (p *persistence) rejectAndReturn(ctx context.Context, tx *wagering.WagerTransaction, w *wallet.Wallet, code wagering.FailureCode, correlationID uuid.UUID, now time.Time) (ProcessTransactionResult, error) {
	if err := tx.MarkRejected(code, now); err != nil {
		return ProcessTransactionResult{}, err
	}
	if err := p.Transactions.Update(ctx, tx); err != nil {
		return ProcessTransactionResult{}, err
	}

	env := events.NewWagerTransactionRejectedEnvelope(correlationID, nil, now, events.WagerTransactionRejectedData{
		TransactionID:         tx.InternalID(),
		ExternalTransactionID: tx.ExternalTransactionID(),
		ProviderID:            tx.ProviderID(),
		PlayerID:              tx.PlayerID(),
		WalletID:              tx.WalletID(),
		Kind:                  tx.Kind(),
		FailureCode:           code,
		RejectedAt:            now,
	})
	if err := p.enqueueEvents(ctx, env); err != nil {
		return ProcessTransactionResult{}, err
	}

	result := ProcessTransactionResult{
		TransactionID: tx.InternalID(),
		Status:        tx.Status(),
		FailureCode:   &code,
	}
	if w != nil {
		result.Balance = w.Balance()
		result.WalletVersion = w.Version()
	}
	return result, nil
}

func (p *persistence) enqueueEvents(ctx context.Context, envs ...events.Envelope) error {
	outboxEvents := make([]ports.OutboxEvent, 0, len(envs))
	for _, env := range envs {
		oe, err := ports.NewOutboxEventFromEnvelope(env)
		if err != nil {
			return err
		}
		outboxEvents = append(outboxEvents, oe)
	}
	return p.Outbox.Enqueue(ctx, outboxEvents...)
}

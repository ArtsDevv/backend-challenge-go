package wagering_test

import (
	"testing"
	"time"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagering"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func mustMoney(t *testing.T, minor int64, currency string) money.Money {
	t.Helper()
	m, err := money.FromMinorUnits(minor, currency)
	require.NoError(t, err, "test setup: building Money(%d, %s)", minor, currency)
	return m
}

func validExternalParams(t *testing.T, kind wagering.Kind, minor int64) wagering.NewExternalTransactionParams {
	t.Helper()
	p := wagering.NewExternalTransactionParams{
		ExternalTransactionID: "ext-1",
		ProviderID:            "provider-1",
		IdempotencyKey:        "idem-1",
		PayloadHash:           "deadbeef",
		WalletID:              uuid.New(),
		PlayerID:              "player-1",
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  kind,
		Money:                 mustMoney(t, minor, "BRL"),
		Now:                   time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	if kind.IsReversal() {
		p.ReferenceExternalTransactionID = "ext-0"
	}
	return p
}

func TestNewExternalTransaction_RejectsOpeningKind(t *testing.T) {
	p := validExternalParams(t, wagering.KindOpening, 1000)

	tx, err := wagering.NewExternalTransaction(p)

	require.Nil(t, tx)
	require.ErrorIs(t, err, wagering.ErrInvalidKindForExternal)
}

func TestNewExternalTransaction_RejectsUnknownKind(t *testing.T) {
	p := validExternalParams(t, wagering.Kind("SOMETHING_ELSE"), 1000)

	tx, err := wagering.NewExternalTransaction(p)

	require.Nil(t, tx)
	require.ErrorIs(t, err, wagering.ErrInvalidKindForExternal)
}

func TestNewExternalTransaction_AmountByKind(t *testing.T) {
	tests := []struct {
		name    string
		kind    wagering.Kind
		minor   int64
		wantErr error
	}{
		{"BET positive is accepted", wagering.KindBet, 8000, nil},
		{"BET zero is rejected", wagering.KindBet, 0, wagering.ErrInvalidAmountForKind},
		{"WIN positive is accepted", wagering.KindWin, 5000, nil},
		{"WIN zero is rejected", wagering.KindWin, 0, wagering.ErrInvalidAmountForKind},
		{"LOSS zero is accepted", wagering.KindLoss, 0, nil},
		{"LOSS positive is rejected", wagering.KindLoss, 100, wagering.ErrInvalidAmountForKind},
		{"REFUND positive is accepted", wagering.KindRefund, 8000, nil},
		{"REFUND zero is rejected", wagering.KindRefund, 0, wagering.ErrInvalidAmountForKind},
		{"ROLLBACK positive is accepted", wagering.KindRollback, 8000, nil},
		{"ROLLBACK zero is rejected", wagering.KindRollback, 0, wagering.ErrInvalidAmountForKind},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validExternalParams(t, tt.kind, tt.minor)

			tx, err := wagering.NewExternalTransaction(p)

			if tt.wantErr == nil {
				require.NoError(t, err)
				require.NotNil(t, tx)
				require.Equal(t, wagering.StatusPending, tx.Status())
				return
			}
			require.Nil(t, tx)
			require.ErrorIs(t, err, tt.wantErr)
			var amountErr *wagering.InvalidAmountForKindError
			require.ErrorAs(t, err, &amountErr)
			require.Equal(t, tt.kind, amountErr.Kind)
		})
	}
}

func TestNewExternalTransaction_ReversalRequiresReference(t *testing.T) {
	for _, kind := range []wagering.Kind{wagering.KindRefund, wagering.KindRollback} {
		t.Run(string(kind), func(t *testing.T) {
			p := validExternalParams(t, kind, 8000)
			p.ReferenceExternalTransactionID = ""

			tx, err := wagering.NewExternalTransaction(p)

			require.Nil(t, tx)
			require.ErrorIs(t, err, wagering.ErrReversalRequiresReference)
		})
	}
}

func TestNewExternalTransaction_RequiredFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*wagering.NewExternalTransactionParams)
	}{
		{"missing externalTransactionId", func(p *wagering.NewExternalTransactionParams) { p.ExternalTransactionID = "" }},
		{"missing providerId", func(p *wagering.NewExternalTransactionParams) { p.ProviderID = "" }},
		{"missing idempotencyKey", func(p *wagering.NewExternalTransactionParams) { p.IdempotencyKey = "" }},
		{"missing payloadHash", func(p *wagering.NewExternalTransactionParams) { p.PayloadHash = "" }},
		{"missing playerId", func(p *wagering.NewExternalTransactionParams) { p.PlayerID = "" }},
		{"missing walletId", func(p *wagering.NewExternalTransactionParams) { p.WalletID = uuid.Nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validExternalParams(t, wagering.KindBet, 8000)
			tt.mutate(&p)

			tx, err := wagering.NewExternalTransaction(p)

			require.Nil(t, tx)
			require.ErrorIs(t, err, wagering.ErrMissingRequiredField)
		})
	}
}

func TestNewOpeningTransaction_ZeroAndPositiveAccepted_NegativeRejected(t *testing.T) {
	walletID := uuid.New()

	zero, err := wagering.NewOpeningTransaction(wagering.NewOpeningTransactionParams{
		WalletID: walletID,
		PlayerID: "player-1",
		Money:    mustMoney(t, 0, "BRL"),
	})
	require.NoError(t, err)
	require.Equal(t, wagering.KindOpening, zero.Kind())
	require.Equal(t, wagering.StatusPending, zero.Status())

	positive, err := wagering.NewOpeningTransaction(wagering.NewOpeningTransactionParams{
		WalletID: walletID,
		PlayerID: "player-1",
		Money:    mustMoney(t, 10000, "BRL"),
	})
	require.NoError(t, err)
	require.Equal(t, wagering.StatusPending, positive.Status())

	negative := mustMoney(t, -1, "BRL")
	tx, err := wagering.NewOpeningTransaction(wagering.NewOpeningTransactionParams{
		WalletID: walletID,
		PlayerID: "player-1",
		Money:    negative,
	})
	require.Nil(t, tx)
	require.ErrorIs(t, err, wagering.ErrInvalidAmountForKind)
}

func TestNewOpeningTransaction_RequiredFields(t *testing.T) {
	_, err := wagering.NewOpeningTransaction(wagering.NewOpeningTransactionParams{
		WalletID: uuid.Nil,
		PlayerID: "player-1",
		Money:    mustMoney(t, 0, "BRL"),
	})
	require.ErrorIs(t, err, wagering.ErrMissingRequiredField)

	_, err = wagering.NewOpeningTransaction(wagering.NewOpeningTransactionParams{
		WalletID: uuid.New(),
		PlayerID: "",
		Money:    mustMoney(t, 0, "BRL"),
	})
	require.ErrorIs(t, err, wagering.ErrMissingRequiredField)
}

func newPendingTx(t *testing.T) *wagering.WagerTransaction {
	t.Helper()
	tx, err := wagering.NewExternalTransaction(validExternalParams(t, wagering.KindBet, 8000))
	require.NoError(t, err)
	return tx
}

func TestStateMachine_PendingToProcessed(t *testing.T) {
	tx := newPendingTx(t)
	now := time.Now().UTC()

	err := tx.MarkProcessed(2000, 2, now)

	require.NoError(t, err)
	require.Equal(t, wagering.StatusProcessed, tx.Status())
	require.True(t, tx.IsTerminal())
	require.NotNil(t, tx.ResultBalanceMinor())
	require.Equal(t, int64(2000), *tx.ResultBalanceMinor())
	require.NotNil(t, tx.ResultWalletVersion())
	require.Equal(t, int64(2), *tx.ResultWalletVersion())
	require.NotNil(t, tx.ProcessedAt())
}

func TestStateMachine_PendingToPendingReferenceToProcessed(t *testing.T) {
	tx := newPendingTx(t)
	now := time.Now().UTC()

	require.NoError(t, tx.MarkPendingReference(now))
	require.Equal(t, wagering.StatusPendingReference, tx.Status())
	require.NotNil(t, tx.PendingSince())

	require.NoError(t, tx.MarkProcessed(2000, 2, now.Add(time.Minute)))
	require.Equal(t, wagering.StatusProcessed, tx.Status())
}

func TestStateMachine_RejectedRequiresFailureCode(t *testing.T) {
	tx := newPendingTx(t)

	err := tx.MarkRejected("", time.Now().UTC())

	require.ErrorIs(t, err, wagering.ErrMissingRequiredField)
	require.Equal(t, wagering.StatusPending, tx.Status())
}

func TestStateMachine_MarkRejected(t *testing.T) {
	tx := newPendingTx(t)
	now := time.Now().UTC()

	err := tx.MarkRejected(wagering.FailureCodeInsufficientBalance, now)

	require.NoError(t, err)
	require.Equal(t, wagering.StatusRejected, tx.Status())
	require.NotNil(t, tx.FailureCode())
	require.Equal(t, wagering.FailureCodeInsufficientBalance, *tx.FailureCode())
}

func TestStateMachine_TerminalTransactionIsImmutable(t *testing.T) {
	tx := newPendingTx(t)
	now := time.Now().UTC()
	require.NoError(t, tx.MarkProcessed(2000, 2, now))

	for _, attempt := range []func() error{
		func() error { return tx.MarkProcessed(1, 1, now) },
		func() error { return tx.MarkRejected(wagering.FailureCodeInsufficientBalance, now) },
		func() error { return tx.MarkFailed(wagering.FailureCodeWalletNotFound, now) },
		func() error { return tx.MarkPendingReference(now) },
	} {
		err := attempt()
		require.Error(t, err)
		require.ErrorIs(t, err, wagering.ErrTerminalTransaction)
		var terminalErr *wagering.TerminalTransactionError
		require.ErrorAs(t, err, &terminalErr)
		require.Equal(t, wagering.StatusProcessed, terminalErr.Status)
		require.Equal(t, wagering.StatusProcessed, tx.Status())
	}
}

func TestStateMachine_InvalidTransitionIsClassifiable(t *testing.T) {
	tx := newPendingTx(t)
	now := time.Now().UTC()
	require.NoError(t, tx.MarkPendingReference(now))

	err := tx.MarkPendingReference(now)

	require.Error(t, err)
	require.ErrorIs(t, err, wagering.ErrInvalidTransition)
	var invalidErr *wagering.InvalidTransitionError
	require.ErrorAs(t, err, &invalidErr)
	require.Equal(t, wagering.StatusPendingReference, invalidErr.From)
	require.Equal(t, wagering.StatusPendingReference, invalidErr.To)
}

func TestStateMachine_RecordPendingReferenceAttemptRequiresPendingReferenceStatus(t *testing.T) {
	tx := newPendingTx(t)

	err := tx.RecordPendingReferenceAttempt(time.Now().UTC(), time.Now().UTC())

	require.ErrorIs(t, err, wagering.ErrNotPendingReference)
}

func TestStateMachine_PendingReferenceExceeded(t *testing.T) {
	tx := newPendingTx(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, tx.MarkPendingReference(start))

	require.False(t, tx.PendingReferenceExceeded(10, 30*time.Minute, start))

	for i := 0; i < 9; i++ {
		require.NoError(t, tx.RecordPendingReferenceAttempt(start.Add(time.Duration(i+1)*time.Second), start))
	}
	require.False(t, tx.PendingReferenceExceeded(10, 30*time.Minute, start), "9 attempts must not yet exceed a max of 10")

	require.NoError(t, tx.RecordPendingReferenceAttempt(start.Add(10*time.Second), start))
	require.True(t, tx.PendingReferenceExceeded(10, 30*time.Minute, start), "10th attempt must hit the max-attempts cutoff")

	tx2 := newPendingTx(t)
	require.NoError(t, tx2.MarkPendingReference(start))
	require.True(t, tx2.PendingReferenceExceeded(100, 30*time.Minute, start.Add(31*time.Minute)))
}

func referenceTx(t *testing.T, kind wagering.Kind, status wagering.Status, overrideParams func(*wagering.NewExternalTransactionParams)) *wagering.WagerTransaction {
	t.Helper()
	p := validExternalParams(t, kind, 8000)
	if overrideParams != nil {
		overrideParams(&p)
	}
	tx, err := wagering.NewExternalTransaction(p)
	require.NoError(t, err)
	if status == wagering.StatusProcessed {
		require.NoError(t, tx.MarkProcessed(2000, 2, time.Now().UTC()))
	}
	return tx
}

func TestValidateReversalAgainst(t *testing.T) {
	t.Run("nil reference", func(t *testing.T) {
		reversal, err := wagering.NewExternalTransaction(validExternalParams(t, wagering.KindRefund, 8000))
		require.NoError(t, err)

		err = reversal.ValidateReversalAgainst(nil)

		require.ErrorIs(t, err, wagering.ErrReferenceNotFound)
	})

	t.Run("reference not terminal", func(t *testing.T) {
		reversal, err := wagering.NewExternalTransaction(validExternalParams(t, wagering.KindRefund, 8000))
		require.NoError(t, err)
		pendingBet := referenceTx(t, wagering.KindBet, wagering.StatusPending, nil)

		err = reversal.ValidateReversalAgainst(pendingBet)

		require.ErrorIs(t, err, wagering.ErrReferenceNotTerminal)
	})

	t.Run("REFUND cannot reference a WIN", func(t *testing.T) {
		reversal, err := wagering.NewExternalTransaction(validExternalParams(t, wagering.KindRefund, 8000))
		require.NoError(t, err)
		processedWin := referenceTx(t, wagering.KindWin, wagering.StatusProcessed, nil)

		err = reversal.ValidateReversalAgainst(processedWin)

		require.ErrorIs(t, err, wagering.ErrReferenceMismatch)
	})

	t.Run("ROLLBACK may reference a WIN", func(t *testing.T) {
		sharedWalletID := uuid.New()
		processedWin := referenceTx(t, wagering.KindWin, wagering.StatusProcessed, func(p *wagering.NewExternalTransactionParams) {
			p.WalletID = sharedWalletID
		})
		rollbackParams := validExternalParams(t, wagering.KindRollback, 8000)
		rollbackParams.WalletID = sharedWalletID
		rollback, err := wagering.NewExternalTransaction(rollbackParams)
		require.NoError(t, err)

		err = rollback.ValidateReversalAgainst(processedWin)

		require.NoError(t, err)
	})

	t.Run("mismatched provider is rejected", func(t *testing.T) {
		reversal, err := wagering.NewExternalTransaction(validExternalParams(t, wagering.KindRefund, 8000))
		require.NoError(t, err)
		processedBet := referenceTx(t, wagering.KindBet, wagering.StatusProcessed, func(p *wagering.NewExternalTransactionParams) {
			p.WalletID = reversal.WalletID()
			p.ProviderID = "another-provider"
		})

		err = reversal.ValidateReversalAgainst(processedBet)

		require.ErrorIs(t, err, wagering.ErrReferenceMismatch)
	})

	t.Run("mismatched currency is rejected", func(t *testing.T) {
		reversal, err := wagering.NewExternalTransaction(validExternalParams(t, wagering.KindRefund, 8000))
		require.NoError(t, err)
		processedBet := referenceTx(t, wagering.KindBet, wagering.StatusProcessed, func(p *wagering.NewExternalTransactionParams) {
			p.WalletID = reversal.WalletID()
			p.Money = mustMoney(t, 8000, "USD")
		})

		err = reversal.ValidateReversalAgainst(processedBet)

		require.ErrorIs(t, err, wagering.ErrReferenceMismatch)
	})

	t.Run("amount mismatch is rejected (no partial reversals)", func(t *testing.T) {
		reversal, err := wagering.NewExternalTransaction(validExternalParams(t, wagering.KindRefund, 4000))
		require.NoError(t, err)
		processedBet := referenceTx(t, wagering.KindBet, wagering.StatusProcessed, func(p *wagering.NewExternalTransactionParams) {
			p.WalletID = reversal.WalletID()
		})

		err = reversal.ValidateReversalAgainst(processedBet)

		require.ErrorIs(t, err, wagering.ErrReferenceAmountMismatch)
	})

	t.Run("matching reversal is valid", func(t *testing.T) {
		reversal, err := wagering.NewExternalTransaction(validExternalParams(t, wagering.KindRefund, 8000))
		require.NoError(t, err)
		processedBet := referenceTx(t, wagering.KindBet, wagering.StatusProcessed, func(p *wagering.NewExternalTransactionParams) {
			p.WalletID = reversal.WalletID()
		})

		err = reversal.ValidateReversalAgainst(processedBet)

		require.NoError(t, err)
	})
}

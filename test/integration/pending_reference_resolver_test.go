//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wageringapp "backend-challenge-go/internal/application/wagering"
	"backend-challenge-go/internal/application/wallets"
	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/domain/wagering"
	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/workers"
)

func TestPendingReferenceResolver_GivesUpAfterMaxAttempts(t *testing.T) {
	skipIfUnavailable(t)
	ctx := context.Background()

	playerID := "player-pending-ref-" + uuid.NewString()
	providerID := "provider-pending-ref-" + uuid.NewString()

	openResult, err := openWalletUC.Execute(ctx, wallets.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: mustMoney(t, "100.00", "BRL"),
	})
	require.NoError(t, err)
	walletID := openResult.WalletID

	missingExternalTransactionID := "does-not-exist-" + uuid.NewString()

	rollbackResult, err := processTransactionUC.Execute(ctx, wageringapp.ProcessTransactionCommand{
		ProviderID:                     providerID,
		ExternalTransactionID:          "ext-rollback-" + uuid.NewString(),
		IdempotencyKey:                 "idem-rollback-" + uuid.NewString(),
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        "round-pending-ref",
		GameID:                         "game-pending-ref",
		Kind:                           wagering.KindRollback,
		Money:                          mustMoney(t, "10.00", "BRL"),
		ReferenceExternalTransactionID: missingExternalTransactionID,
	})
	require.NoError(t, err)
	require.Equal(t, wagering.StatusPendingReference, rollbackResult.Status)
	transactionID := rollbackResult.TransactionID

	cfg := config.PendingReferenceConfig{
		Backoff: config.BackoffConfig{
			BaseInterval:   20 * time.Millisecond,
			Factor:         1.0,
			MaxInterval:    20 * time.Millisecond,
			JitterFraction: 0,
		},
		MaxAttempts:     2,
		TTL:             time.Hour,
		PollInterval:    20 * time.Millisecond,
		MaxPollInterval: 100 * time.Millisecond,
		BatchSize:       10,
	}

	resolveUC := wageringapp.NewResolvePendingReferenceUseCase(walletRepo, transactionRepo, ledgerRepo, outboxStore, cfg)
	resolver := workers.NewPendingReferenceResolver(uow, transactionRepo, resolveUC, cfg, testLogger(), testMetrics())

	go resolver.Run()
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, resolver.Stop(stopCtx))
	}()

	deadline := time.Now().Add(5 * time.Second)
	var finalTx *wagering.WagerTransaction
	for time.Now().Before(deadline) {
		found, findErr := transactionRepo.FindByID(ctx, transactionID)
		require.NoError(t, findErr)
		if found.Status() != wagering.StatusPendingReference {
			finalTx = found
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if finalTx == nil {
		t.Fatal("pending reference transaction did not reach a terminal status within the timeout")
	}

	require.Equal(t, wagering.StatusRejected, finalTx.Status())
	require.NotNil(t, finalTx.FailureCode())
	require.Equal(t, wagering.FailureCodeReferenceNotFound, *finalTx.FailureCode())
	require.GreaterOrEqual(t, finalTx.PendingReferenceAttempts(), cfg.MaxAttempts)

	walletAfter, err := walletRepo.FindByID(ctx, walletID)
	require.NoError(t, err)
	require.Equal(t, openResult.Balance.MinorUnits(), walletAfter.Balance().MinorUnits())
}

func TestPendingReferenceResolver_ResolvesSuccessfullyOnceReferenceArrives(t *testing.T) {
	skipIfUnavailable(t)
	ctx := context.Background()

	playerID := "player-pending-ref-success-" + uuid.NewString()
	providerID := "provider-pending-ref-success-" + uuid.NewString()

	openResult, err := openWalletUC.Execute(ctx, wallets.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: mustMoney(t, "100.00", "BRL"),
	})
	require.NoError(t, err)
	walletID := openResult.WalletID

	laterExternalTransactionID := "ext-bet-arriving-later-" + uuid.NewString()

	refundResult, err := processTransactionUC.Execute(ctx, wageringapp.ProcessTransactionCommand{
		ProviderID:                     providerID,
		ExternalTransactionID:          "ext-refund-" + uuid.NewString(),
		IdempotencyKey:                 "idem-refund-" + uuid.NewString(),
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        "round-pending-ref-success",
		GameID:                         "game-pending-ref-success",
		Kind:                           wagering.KindRefund,
		Money:                          mustMoney(t, "30.00", "BRL"),
		ReferenceExternalTransactionID: laterExternalTransactionID,
	})
	require.NoError(t, err)
	require.Equal(t, wagering.StatusPendingReference, refundResult.Status)
	refundTransactionID := refundResult.TransactionID

	betResult, err := processTransactionUC.Execute(ctx, wageringapp.ProcessTransactionCommand{
		ProviderID:            providerID,
		ExternalTransactionID: laterExternalTransactionID,
		IdempotencyKey:        "idem-bet-" + uuid.NewString(),
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-pending-ref-success",
		GameID:                "game-pending-ref-success",
		Kind:                  wagering.KindBet,
		Money:                 mustMoney(t, "30.00", "BRL"),
	})
	require.NoError(t, err)
	require.Equal(t, wagering.StatusProcessed, betResult.Status)
	require.Equal(t, int64(7000), betResult.Balance.MinorUnits())

	cfg := config.PendingReferenceConfig{
		Backoff: config.BackoffConfig{
			BaseInterval:   20 * time.Millisecond,
			Factor:         1.0,
			MaxInterval:    20 * time.Millisecond,
			JitterFraction: 0,
		},
		MaxAttempts:     10,
		TTL:             time.Hour,
		PollInterval:    20 * time.Millisecond,
		MaxPollInterval: 100 * time.Millisecond,
		BatchSize:       10,
	}

	resolveUC := wageringapp.NewResolvePendingReferenceUseCase(walletRepo, transactionRepo, ledgerRepo, outboxStore, cfg)
	resolver := workers.NewPendingReferenceResolver(uow, transactionRepo, resolveUC, cfg, testLogger(), testMetrics())

	go resolver.Run()
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, resolver.Stop(stopCtx))
	}()

	deadline := time.Now().Add(5 * time.Second)
	var finalTx *wagering.WagerTransaction
	for time.Now().Before(deadline) {
		found, findErr := transactionRepo.FindByID(ctx, refundTransactionID)
		require.NoError(t, findErr)
		if found.Status() != wagering.StatusPendingReference {
			finalTx = found
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if finalTx == nil {
		t.Fatal("pending reference transaction did not reach a terminal status within the timeout")
	}

	require.Equal(t, wagering.StatusProcessed, finalTx.Status(), "once the referenced BET exists and validates, the resolver must successfully apply the REFUND rather than giving up")
	require.Nil(t, finalTx.FailureCode())
	require.NotNil(t, finalTx.ResultBalanceMinor())
	assert.Equal(t, int64(10000), *finalTx.ResultBalanceMinor(), "the REFUND must restore the full 30.00 BRL bet amount back to 100.00 BRL")

	walletAfter, err := walletRepo.FindByID(ctx, walletID)
	require.NoError(t, err)
	assert.Equal(t, int64(10000), walletAfter.Balance().MinorUnits())

	entries, _, err := ledgerRepo.ListByWallet(ctx, walletID, "", 10)
	require.NoError(t, err)
	require.Len(t, entries, 3, "one CREDIT for opening the wallet, one DEBIT for the BET, one CREDIT for the resolved REFUND")
	assert.Equal(t, wallet.DirectionCredit, entries[0].Direction())
	assert.Equal(t, wallet.DirectionDebit, entries[1].Direction())
	assert.Equal(t, wallet.DirectionCredit, entries[2].Direction())
}

package wagering

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/platform/metrics"
)

func TestReconciliation_ConsistentWalletReportsZeroDifferenceAndNeverMutatesBalance(t *testing.T) {
	walletRepo := newFakeWalletRepo()
	ledgerRepo := &fakeLedgerRepo{}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	w, err := wallet.NewWallet(uuid.New(), "player-1", mustMoney(t, "100.00", "BRL"), now)
	require.NoError(t, err)
	walletRepo.put(w)

	entry, err := wallet.NewWalletLedgerEntry(uuid.New(), w.ID(), uuid.New(), wallet.DirectionCredit,
		mustMoney(t, "100.00", "BRL"), mustMoney(t, "0.00", "BRL"), mustMoney(t, "100.00", "BRL"), now)
	require.NoError(t, err)
	require.NoError(t, ledgerRepo.Insert(context.Background(), entry))

	m := metrics.New(prometheus.NewRegistry())
	uc := NewReconciliationUseCase(walletRepo, ledgerRepo, m)

	result, err := uc.Execute(context.Background(), w.ID())

	require.NoError(t, err)
	assert.True(t, result.Consistent)
	assert.Equal(t, int64(0), result.Difference.MinorUnits())
	assert.Equal(t, 1, result.CheckedEntries)
	assert.Equal(t, int64(10000), result.StoredBalance.MinorUnits())
	assert.Equal(t, int64(10000), result.CalculatedBalance.MinorUnits())
	assert.Equal(t, int64(10000), w.Balance().MinorUnits(), "reconciliation must never mutate the wallet's stored balance")
	assert.Equal(t, int64(1), w.Version(), "reconciliation must never bump the wallet version")

	assert.Equal(t, float64(1), testutil.ToFloat64(m.ReconciliationChecks.WithLabelValues("true")))
}

func TestReconciliation_DivergentWalletReportsNonZeroDifference(t *testing.T) {
	walletRepo := newFakeWalletRepo()
	ledgerRepo := &fakeLedgerRepo{}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	w, err := wallet.NewWallet(uuid.New(), "player-1", mustMoney(t, "100.00", "BRL"), now)
	require.NoError(t, err)
	walletRepo.put(w)

	entry, err := wallet.NewWalletLedgerEntry(uuid.New(), w.ID(), uuid.New(), wallet.DirectionCredit,
		mustMoney(t, "50.00", "BRL"), mustMoney(t, "0.00", "BRL"), mustMoney(t, "50.00", "BRL"), now)
	require.NoError(t, err)
	require.NoError(t, ledgerRepo.Insert(context.Background(), entry))

	m := metrics.New(prometheus.NewRegistry())
	uc := NewReconciliationUseCase(walletRepo, ledgerRepo, m)

	result, err := uc.Execute(context.Background(), w.ID())

	require.NoError(t, err)
	assert.False(t, result.Consistent)
	assert.Equal(t, int64(5000), result.Difference.MinorUnits(), "stored (100.00) minus recalculated (50.00) must be exactly 50.00")
	assert.Equal(t, int64(10000), w.Balance().MinorUnits(), "reconciliation must never correct the divergence itself")

	assert.Equal(t, float64(1), testutil.ToFloat64(m.ReconciliationChecks.WithLabelValues("false")))
}

func TestReconciliation_UnknownWalletReturnsNotFound(t *testing.T) {
	walletRepo := newFakeWalletRepo()
	ledgerRepo := &fakeLedgerRepo{}

	uc := NewReconciliationUseCase(walletRepo, ledgerRepo, nil)

	_, err := uc.Execute(context.Background(), uuid.New())

	require.Error(t, err)
}

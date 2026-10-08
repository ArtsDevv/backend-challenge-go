//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	pgadapter "backend-challenge-go/internal/adapters/postgres"
	"backend-challenge-go/internal/application/wallets"
	"backend-challenge-go/internal/platform/metrics"
	"backend-challenge-go/internal/ports"
)

func TestConcurrencyConflicts_LockTimeoutIncrementsMetric(t *testing.T) {
	skipIfUnavailable(t)
	ctx := context.Background()

	playerID := "player-conflict-metric-" + uuid.NewString()
	openResult, err := openWalletUC.Execute(ctx, wallets.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: mustMoney(t, "100.00", "BRL"),
	})
	require.NoError(t, err)
	walletID := openResult.WalletID

	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()

	rawTx, err := conn.Begin(ctx)
	require.NoError(t, err)
	defer rawTx.Rollback(ctx)

	_, err = rawTx.Exec(ctx, "SELECT id FROM wallets WHERE id = $1 FOR UPDATE", walletID)
	require.NoError(t, err)

	m := testMetrics()
	txMgr := pgadapter.NewTxManager(pool, pgadapter.WithMetrics(m))

	lockCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	err = txMgr.WithinTx(lockCtx, func(ctx context.Context) error {
		_, err := walletRepo.LockForUpdate(ctx, walletID)
		return err
	})

	require.Error(t, err)
	require.ErrorIs(t, err, ports.ErrWalletLockTimeout)

	require.Equal(t, float64(1), testutil.ToFloat64(m.ConcurrencyConflicts.WithLabelValues(metrics.ComponentPostgres, metrics.ConflictLockTimeout)))
}

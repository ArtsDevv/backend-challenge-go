//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"backend-challenge-go/internal/application/wallets"
)

func TestLedgerImmutability_UpdateAndDeleteAreRejected(t *testing.T) {
	skipIfUnavailable(t)
	ctx := context.Background()

	playerID := "player-ledger-immutability-" + uuid.NewString()
	openResult, err := openWalletUC.Execute(ctx, wallets.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: mustMoney(t, "100.00", "BRL"),
	})
	require.NoError(t, err)
	walletID := openResult.WalletID

	entries, _, err := ledgerRepo.ListByWallet(ctx, walletID, "", 50)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	entry := entries[0]

	_, updateErr := pool.Exec(ctx, "UPDATE wallet_ledger_entries SET amount_minor = amount_minor WHERE id = $1", entry.ID())
	require.Error(t, updateErr)
	assert.True(t, strings.Contains(strings.ToLower(updateErr.Error()), "append-only"), "update error should mention append-only, got: %v", updateErr)

	var updatePgErr *pgconn.PgError
	if errors.As(updateErr, &updatePgErr) {
		assert.Equal(t, "23000", updatePgErr.Code)
	}

	_, deleteErr := pool.Exec(ctx, "DELETE FROM wallet_ledger_entries WHERE id = $1", entry.ID())
	require.Error(t, deleteErr)
	assert.True(t, strings.Contains(strings.ToLower(deleteErr.Error()), "append-only"), "delete error should mention append-only, got: %v", deleteErr)

	var deletePgErr *pgconn.PgError
	if errors.As(deleteErr, &deletePgErr) {
		assert.Equal(t, "23000", deletePgErr.Code)
	}

	entriesAfter, _, err := ledgerRepo.ListByWallet(ctx, walletID, "", 50)
	require.NoError(t, err)
	require.Len(t, entriesAfter, 1)
	entryAfter := entriesAfter[0]

	assert.Equal(t, entry.Amount(), entryAfter.Amount())
	assert.Equal(t, entry.BalanceBefore(), entryAfter.BalanceBefore())
	assert.Equal(t, entry.BalanceAfter(), entryAfter.BalanceAfter())
}

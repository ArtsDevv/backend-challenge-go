package wallet_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wallet"
)

func mustMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	m, err := money.NewMoneyFromString(amount, currency)
	require.NoError(t, err)
	return m
}

func TestNewWallet_Success(t *testing.T) {
	now := time.Now().UTC()
	initial := mustMoney(t, "100.00", "BRL")

	w, err := wallet.NewWallet(uuid.New(), "player-1", initial, now)
	require.NoError(t, err)

	assert.Equal(t, "player-1", w.PlayerID())
	assert.Equal(t, "BRL", w.Currency())
	assert.True(t, w.Balance().Equal(initial))
	assert.Equal(t, int64(1), w.Version())
	assert.Equal(t, now, w.CreatedAt())
	assert.Equal(t, now, w.UpdatedAt())
}

func TestNewWallet_ZeroInitialBalanceIsAccepted(t *testing.T) {
	zero := mustMoney(t, "0.00", "BRL")

	w, err := wallet.NewWallet(uuid.New(), "player-1", zero, time.Now().UTC())
	require.NoError(t, err)
	assert.True(t, w.Balance().IsZero())
	assert.Equal(t, int64(1), w.Version())
}

func TestNewWallet_RejectsNegativeInitialBalance(t *testing.T) {
	negative, err := money.FromMinorUnits(-1, "BRL")
	require.NoError(t, err)

	_, err = wallet.NewWallet(uuid.New(), "player-1", negative, time.Now().UTC())
	require.Error(t, err)
	assert.True(t, errors.Is(err, wallet.ErrInvalidWallet))
}

func TestNewWallet_RejectsEmptyPlayerID(t *testing.T) {
	_, err := wallet.NewWallet(uuid.New(), "", mustMoney(t, "0.00", "BRL"), time.Now().UTC())
	require.Error(t, err)
	assert.True(t, errors.Is(err, wallet.ErrInvalidWallet))
}

func TestRehydrate_DoesNotReapplyAnyLogic(t *testing.T) {
	id := uuid.New()
	createdAt := time.Now().UTC().Add(-24 * time.Hour)
	updatedAt := time.Now().UTC()

	w, err := wallet.Rehydrate(id, "player-1", "BRL", 2000, 5, createdAt, updatedAt)
	require.NoError(t, err)

	assert.Equal(t, id, w.ID())
	assert.Equal(t, "player-1", w.PlayerID())
	assert.Equal(t, "BRL", w.Currency())
	assert.Equal(t, int64(2000), w.Balance().MinorUnits())
	assert.Equal(t, int64(5), w.Version())
	assert.Equal(t, createdAt, w.CreatedAt())
	assert.Equal(t, updatedAt, w.UpdatedAt())
}

func TestRehydrate_RejectsInvalidState(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name         string
		playerID     string
		balanceMinor int64
		version      int64
	}{
		{"empty player id", "", 1000, 1},
		{"negative balance", "player-1", -1, 1},
		{"zero version", "player-1", 1000, 0},
		{"negative version", "player-1", 1000, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := wallet.Rehydrate(uuid.New(), tt.playerID, "BRL", tt.balanceMinor, tt.version, now, now)
			require.Error(t, err)
			assert.True(t, errors.Is(err, wallet.ErrInvalidWallet))
		})
	}
}

func TestDebit_Success(t *testing.T) {
	now := time.Now().UTC()
	w, err := wallet.NewWallet(uuid.New(), "player-1", mustMoney(t, "100.00", "BRL"), now)
	require.NoError(t, err)

	txID := uuid.New()
	later := now.Add(time.Minute)
	entry, err := w.Debit(mustMoney(t, "80.00", "BRL"), txID, later)
	require.NoError(t, err)

	assert.Equal(t, wallet.DirectionDebit, entry.Direction())
	assert.Equal(t, w.ID(), entry.WalletID())
	assert.Equal(t, txID, entry.TransactionID())
	assert.True(t, entry.Amount().Equal(mustMoney(t, "80.00", "BRL")))
	assert.True(t, entry.BalanceBefore().Equal(mustMoney(t, "100.00", "BRL")))
	assert.True(t, entry.BalanceAfter().Equal(mustMoney(t, "20.00", "BRL")))
	assert.Equal(t, later, entry.CreatedAt())

	assert.True(t, w.Balance().Equal(mustMoney(t, "20.00", "BRL")))
	assert.Equal(t, int64(2), w.Version())
	assert.Equal(t, later, w.UpdatedAt())
}

func TestDebit_InsufficientFundsLeavesWalletUntouched(t *testing.T) {
	now := time.Now().UTC()
	w, err := wallet.NewWallet(uuid.New(), "player-1", mustMoney(t, "100.00", "BRL"), now)
	require.NoError(t, err)

	firstEntry, err := w.Debit(mustMoney(t, "80.00", "BRL"), uuid.New(), now)
	require.NoError(t, err)
	require.True(t, firstEntry.BalanceAfter().Equal(mustMoney(t, "20.00", "BRL")))

	balanceBeforeSecondAttempt := w.Balance()
	versionBeforeSecondAttempt := w.Version()
	updatedAtBeforeSecondAttempt := w.UpdatedAt()

	_, err = w.Debit(mustMoney(t, "80.00", "BRL"), uuid.New(), now.Add(time.Minute))
	require.Error(t, err)
	assert.True(t, errors.Is(err, wallet.ErrInsufficientFunds))

	var insufficientErr *wallet.InsufficientFundsError
	require.True(t, errors.As(err, &insufficientErr))
	assert.True(t, insufficientErr.Available.Equal(mustMoney(t, "20.00", "BRL")))
	assert.True(t, insufficientErr.Requested.Equal(mustMoney(t, "80.00", "BRL")))

	assert.True(t, w.Balance().Equal(balanceBeforeSecondAttempt))
	assert.Equal(t, versionBeforeSecondAttempt, w.Version())
	assert.Equal(t, updatedAtBeforeSecondAttempt, w.UpdatedAt())

	assert.True(t, w.Balance().Equal(mustMoney(t, "20.00", "BRL")))
	assert.Equal(t, int64(2), w.Version())
}

func TestDebit_ExactBalanceIsAllowed(t *testing.T) {
	w, err := wallet.NewWallet(uuid.New(), "player-1", mustMoney(t, "50.00", "BRL"), time.Now().UTC())
	require.NoError(t, err)

	entry, err := w.Debit(mustMoney(t, "50.00", "BRL"), uuid.New(), time.Now().UTC())
	require.NoError(t, err)
	assert.True(t, entry.BalanceAfter().IsZero())
	assert.True(t, w.Balance().IsZero())
}

func TestDebit_CurrencyMismatch(t *testing.T) {
	w, err := wallet.NewWallet(uuid.New(), "player-1", mustMoney(t, "100.00", "BRL"), time.Now().UTC())
	require.NoError(t, err)

	_, err = w.Debit(mustMoney(t, "10.00", "USD"), uuid.New(), time.Now().UTC())
	require.Error(t, err)
	assert.True(t, errors.Is(err, wallet.ErrCurrencyMismatch))

	var mismatchErr *wallet.CurrencyMismatchError
	require.True(t, errors.As(err, &mismatchErr))
	assert.Equal(t, "BRL", mismatchErr.WalletCurrency)
	assert.Equal(t, "USD", mismatchErr.MovementCurrency)

	assert.Equal(t, int64(1), w.Version())
}

func TestDebit_RejectsNonPositiveAmount(t *testing.T) {
	w, err := wallet.NewWallet(uuid.New(), "player-1", mustMoney(t, "100.00", "BRL"), time.Now().UTC())
	require.NoError(t, err)

	zero, err := money.Zero("BRL")
	require.NoError(t, err)

	_, err = w.Debit(zero, uuid.New(), time.Now().UTC())
	require.Error(t, err)
	assert.True(t, errors.Is(err, wallet.ErrInvalidAmount))
	assert.Equal(t, int64(1), w.Version())
}

func TestCredit_Success(t *testing.T) {
	now := time.Now().UTC()
	w, err := wallet.NewWallet(uuid.New(), "player-1", mustMoney(t, "100.00", "BRL"), now)
	require.NoError(t, err)

	txID := uuid.New()
	later := now.Add(time.Minute)
	entry, err := w.Credit(mustMoney(t, "25.00", "BRL"), txID, later)
	require.NoError(t, err)

	assert.Equal(t, wallet.DirectionCredit, entry.Direction())
	assert.True(t, entry.BalanceBefore().Equal(mustMoney(t, "100.00", "BRL")))
	assert.True(t, entry.BalanceAfter().Equal(mustMoney(t, "125.00", "BRL")))

	assert.True(t, w.Balance().Equal(mustMoney(t, "125.00", "BRL")))
	assert.Equal(t, int64(2), w.Version())
}

func TestCredit_CurrencyMismatch(t *testing.T) {
	w, err := wallet.NewWallet(uuid.New(), "player-1", mustMoney(t, "100.00", "BRL"), time.Now().UTC())
	require.NoError(t, err)

	_, err = w.Credit(mustMoney(t, "10.00", "USD"), uuid.New(), time.Now().UTC())
	require.Error(t, err)
	assert.True(t, errors.Is(err, wallet.ErrCurrencyMismatch))
	assert.Equal(t, int64(1), w.Version())
}

func TestCredit_RejectsNonPositiveAmount(t *testing.T) {
	w, err := wallet.NewWallet(uuid.New(), "player-1", mustMoney(t, "100.00", "BRL"), time.Now().UTC())
	require.NoError(t, err)

	zero, err := money.Zero("BRL")
	require.NoError(t, err)

	_, err = w.Credit(zero, uuid.New(), time.Now().UTC())
	require.Error(t, err)
	assert.True(t, errors.Is(err, wallet.ErrInvalidAmount))
}

func TestDifferentWalletsAreIndependent(t *testing.T) {
	now := time.Now().UTC()
	w1, err := wallet.NewWallet(uuid.New(), "player-1", mustMoney(t, "100.00", "BRL"), now)
	require.NoError(t, err)
	w2, err := wallet.NewWallet(uuid.New(), "player-2", mustMoney(t, "50.00", "BRL"), now)
	require.NoError(t, err)

	_, err = w1.Debit(mustMoney(t, "100.00", "BRL"), uuid.New(), now)
	require.NoError(t, err)

	assert.True(t, w1.Balance().IsZero())
	assert.True(t, w2.Balance().Equal(mustMoney(t, "50.00", "BRL")))
	assert.Equal(t, int64(1), w2.Version())
}

func TestNewWalletLedgerEntry_ValidCredit(t *testing.T) {
	entry, err := wallet.NewWalletLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		wallet.DirectionCredit,
		mustMoney(t, "25.00", "BRL"),
		mustMoney(t, "100.00", "BRL"),
		mustMoney(t, "125.00", "BRL"),
		time.Now().UTC(),
	)
	require.NoError(t, err)
	assert.True(t, entry.Amount().Equal(mustMoney(t, "25.00", "BRL")))
}

func TestNewWalletLedgerEntry_ValidDebit(t *testing.T) {
	entry, err := wallet.NewWalletLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		wallet.DirectionDebit,
		mustMoney(t, "25.00", "BRL"),
		mustMoney(t, "100.00", "BRL"),
		mustMoney(t, "75.00", "BRL"),
		time.Now().UTC(),
	)
	require.NoError(t, err)
	assert.True(t, entry.Amount().Equal(mustMoney(t, "25.00", "BRL")))
}

func TestNewWalletLedgerEntry_RejectsArithmeticMismatch(t *testing.T) {
	_, err := wallet.NewWalletLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		wallet.DirectionCredit,
		mustMoney(t, "25.00", "BRL"),
		mustMoney(t, "100.00", "BRL"),
		mustMoney(t, "200.00", "BRL"), // wrong: should be 125.00
		time.Now().UTC(),
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, wallet.ErrInvalidLedgerEntry))
}

func TestNewWalletLedgerEntry_RejectsNonPositiveAmount(t *testing.T) {
	zero, err := money.Zero("BRL")
	require.NoError(t, err)

	_, err = wallet.NewWalletLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		wallet.DirectionCredit,
		zero,
		mustMoney(t, "100.00", "BRL"),
		mustMoney(t, "100.00", "BRL"),
		time.Now().UTC(),
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, wallet.ErrInvalidLedgerEntry))
}

func TestNewWalletLedgerEntry_RejectsUnknownDirection(t *testing.T) {
	_, err := wallet.NewWalletLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		wallet.Direction("SIDEWAYS"),
		mustMoney(t, "25.00", "BRL"),
		mustMoney(t, "100.00", "BRL"),
		mustMoney(t, "125.00", "BRL"),
		time.Now().UTC(),
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, wallet.ErrInvalidLedgerEntry))
}

func TestNewWalletLedgerEntry_RejectsCurrencyMismatch(t *testing.T) {
	_, err := wallet.NewWalletLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		wallet.DirectionCredit,
		mustMoney(t, "25.00", "USD"),
		mustMoney(t, "100.00", "BRL"),
		mustMoney(t, "125.00", "BRL"),
		time.Now().UTC(),
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, wallet.ErrCurrencyMismatch))
}

func TestNewWalletLedgerEntry_RejectsNegativeBalances(t *testing.T) {
	negativeBefore, err := money.FromMinorUnits(-100, "BRL")
	require.NoError(t, err)

	_, err = wallet.NewWalletLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		wallet.DirectionCredit,
		mustMoney(t, "25.00", "BRL"),
		negativeBefore,
		mustMoney(t, "125.00", "BRL"),
		time.Now().UTC(),
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, wallet.ErrInvalidLedgerEntry))
}

func TestWalletAlreadyExistsError_ClassifiableViaIsAndAs(t *testing.T) {
	var err error = &wallet.WalletAlreadyExistsError{PlayerID: "player-1", Currency: "BRL"}

	assert.True(t, errors.Is(err, wallet.ErrWalletAlreadyExists))

	var typed *wallet.WalletAlreadyExistsError
	require.True(t, errors.As(err, &typed))
	assert.Equal(t, "player-1", typed.PlayerID)
	assert.Equal(t, "BRL", typed.Currency)
}

func TestVersionConflictError_ClassifiableViaIsAndAs(t *testing.T) {
	var err error = &wallet.VersionConflictError{WalletID: "wallet-1", Expected: 3, Actual: 4}

	assert.True(t, errors.Is(err, wallet.ErrWalletVersionConflict))

	var typed *wallet.VersionConflictError
	require.True(t, errors.As(err, &typed))
	assert.Equal(t, int64(3), typed.Expected)
	assert.Equal(t, int64(4), typed.Actual)
}

package wallets

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagering"
	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/ports"
)

type fakeUnitOfWork struct{}

func (fakeUnitOfWork) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

type fakeWalletRepo struct {
	mu      sync.Mutex
	wallets map[uuid.UUID]*wallet.Wallet
}

func newFakeWalletRepo() *fakeWalletRepo {
	return &fakeWalletRepo{wallets: map[uuid.UUID]*wallet.Wallet{}}
}

func (r *fakeWalletRepo) LockForUpdate(_ context.Context, walletID uuid.UUID) (*wallet.Wallet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.wallets[walletID]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return w, nil
}

func (r *fakeWalletRepo) FindByID(ctx context.Context, walletID uuid.UUID) (*wallet.Wallet, error) {
	return r.LockForUpdate(ctx, walletID)
}

func (r *fakeWalletRepo) FindByPlayerAndCurrency(_ context.Context, playerID, currency string) (*wallet.Wallet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, w := range r.wallets {
		if w.PlayerID() == playerID && w.Currency() == currency {
			return w, nil
		}
	}
	return nil, ports.ErrNotFound
}

func (r *fakeWalletRepo) Insert(_ context.Context, w *wallet.Wallet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.wallets {
		if existing.PlayerID() == w.PlayerID() && existing.Currency() == w.Currency() {
			return &wallet.WalletAlreadyExistsError{PlayerID: w.PlayerID(), Currency: w.Currency()}
		}
	}
	r.wallets[w.ID()] = w
	return nil
}

func (r *fakeWalletRepo) Update(_ context.Context, w *wallet.Wallet, _ int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.wallets[w.ID()]; !ok {
		return ports.ErrNotFound
	}
	return nil
}

type fakeTransactionRepo struct {
	mu   sync.Mutex
	byID map[uuid.UUID]*wagering.WagerTransaction
}

func newFakeTransactionRepo() *fakeTransactionRepo {
	return &fakeTransactionRepo{byID: map[uuid.UUID]*wagering.WagerTransaction{}}
}

func (r *fakeTransactionRepo) InsertIfAbsent(_ context.Context, tx *wagering.WagerTransaction) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[tx.InternalID()]; exists {
		return false, nil
	}
	cp := *tx
	r.byID[tx.InternalID()] = &cp
	return true, nil
}

func (r *fakeTransactionRepo) Insert(_ context.Context, tx *wagering.WagerTransaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *tx
	r.byID[tx.InternalID()] = &cp
	return nil
}

func (r *fakeTransactionRepo) FindByID(_ context.Context, transactionID uuid.UUID) (*wagering.WagerTransaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, ok := r.byID[transactionID]
	if !ok {
		return nil, ports.ErrNotFound
	}
	cp := *tx
	return &cp, nil
}

func (r *fakeTransactionRepo) FindByProviderAndIdempotencyKey(_ context.Context, _, _ string) (*wagering.WagerTransaction, error) {
	return nil, ports.ErrNotFound
}

func (r *fakeTransactionRepo) FindByProviderAndExternalTransactionID(_ context.Context, _, _ string) (*wagering.WagerTransaction, error) {
	return nil, ports.ErrNotFound
}

func (r *fakeTransactionRepo) LockForUpdate(ctx context.Context, transactionID uuid.UUID) (*wagering.WagerTransaction, error) {
	return r.FindByID(ctx, transactionID)
}

func (r *fakeTransactionRepo) HasSuccessfulReversal(_ context.Context, _, _ string, _ wagering.Kind) (bool, error) {
	return false, nil
}

func (r *fakeTransactionRepo) Update(_ context.Context, tx *wagering.WagerTransaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[tx.InternalID()]; !ok {
		return ports.ErrNotFound
	}
	cp := *tx
	r.byID[tx.InternalID()] = &cp
	return nil
}

func (r *fakeTransactionRepo) LockPendingReferenceBatch(_ context.Context, _ int) ([]*wagering.WagerTransaction, error) {
	return nil, nil
}

func (r *fakeTransactionRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byID)
}

type fakeLedgerRepo struct {
	mu      sync.Mutex
	entries []wallet.WalletLedgerEntry
}

func (r *fakeLedgerRepo) Insert(_ context.Context, entry wallet.WalletLedgerEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.WalletID() == entry.WalletID() && e.TransactionID() == entry.TransactionID() {
			return ports.ErrDuplicateLedgerEntry
		}
	}
	r.entries = append(r.entries, entry)
	return nil
}

func (r *fakeLedgerRepo) ListByWallet(_ context.Context, walletID uuid.UUID, _ string, _ int) ([]wallet.WalletLedgerEntry, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []wallet.WalletLedgerEntry
	for _, e := range r.entries {
		if e.WalletID() == walletID {
			out = append(out, e)
		}
	}
	return out, "", nil
}

func (r *fakeLedgerRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

type fakeOutboxStore struct {
	mu     sync.Mutex
	events []ports.OutboxEvent
}

func (s *fakeOutboxStore) Enqueue(_ context.Context, events ...ports.OutboxEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, events...)
	return nil
}

func (s *fakeOutboxStore) LockPendingBatch(_ context.Context, _ int) ([]ports.OutboxEvent, error) {
	return nil, nil
}

func (s *fakeOutboxStore) MarkPublished(_ context.Context, _ uuid.UUID, _ time.Time) error {
	return nil
}

func (s *fakeOutboxStore) RecordFailedAttempt(_ context.Context, _ uuid.UUID, _ time.Time) error {
	return nil
}

func (s *fakeOutboxStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// --- test scaffolding ---------------------------------------------------------------------------

func mustMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	m, err := money.NewMoneyFromString(amount, currency)
	require.NoError(t, err)
	return m
}

type testHarness struct {
	uc      *OpenWalletUseCase
	wallets *fakeWalletRepo
	txs     *fakeTransactionRepo
	ledger  *fakeLedgerRepo
	outbox  *fakeOutboxStore
}

func newTestHarness() *testHarness {
	h := &testHarness{
		wallets: newFakeWalletRepo(),
		txs:     newFakeTransactionRepo(),
		ledger:  &fakeLedgerRepo{},
		outbox:  &fakeOutboxStore{},
	}
	h.uc = NewOpenWalletUseCase(fakeUnitOfWork{}, h.wallets, h.txs, h.ledger, h.outbox)
	h.uc.Now = func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) }
	return h
}

// --- tests ---------------------------------------------------------------------------------------

func TestOpenWallet_ZeroInitialBalanceCreatesBareWallet(t *testing.T) {
	h := newTestHarness()

	result, err := h.uc.Execute(context.Background(), OpenWalletCommand{
		PlayerID:       "player-1",
		InitialBalance: mustMoney(t, "0.00", "BRL"),
	})

	require.NoError(t, err)
	assert.Equal(t, "player-1", result.PlayerID)
	assert.Equal(t, int64(1), result.Version)
	assert.True(t, result.Balance.IsZero())

	assert.Equal(t, 0, h.txs.count())
	assert.Equal(t, 0, h.ledger.count())
	assert.Equal(t, 0, h.outbox.count())
}

func TestOpenWallet_PositiveInitialBalanceRecordsOpeningCreditInSameCommit(t *testing.T) {
	h := newTestHarness()

	result, err := h.uc.Execute(context.Background(), OpenWalletCommand{
		PlayerID:       "player-1",
		InitialBalance: mustMoney(t, "25.00", "BRL"),
	})

	require.NoError(t, err)
	assert.Equal(t, int64(2500), result.Balance.MinorUnits())
	assert.Equal(t, int64(1), result.Version)

	require.Equal(t, 1, h.txs.count(), "expects exactly one OPENING transaction")
	require.Equal(t, 1, h.ledger.count(), "expects exactly one ledger entry for the opening credit")
	entry := h.ledger.entries[0]
	assert.Equal(t, wallet.DirectionCredit, entry.Direction())
	assert.Equal(t, int64(0), entry.BalanceBefore().MinorUnits())
	assert.Equal(t, int64(2500), entry.BalanceAfter().MinorUnits())

	assert.Equal(t, 2, h.outbox.count(), "expects WagerTransactionProcessed + WalletBalanceChanged")
}

func TestOpenWallet_ReopeningSamePlayerAndCurrencyConflicts(t *testing.T) {
	h := newTestHarness()

	_, err := h.uc.Execute(context.Background(), OpenWalletCommand{
		PlayerID:       "player-1",
		InitialBalance: mustMoney(t, "10.00", "BRL"),
	})
	require.NoError(t, err)

	_, err = h.uc.Execute(context.Background(), OpenWalletCommand{
		PlayerID:       "player-1",
		InitialBalance: mustMoney(t, "0.00", "BRL"),
	})

	require.Error(t, err)
	assert.True(t, errors.Is(err, wallet.ErrWalletAlreadyExists))

	assert.Equal(t, 1, h.txs.count())
	assert.Equal(t, 1, h.ledger.count())
}

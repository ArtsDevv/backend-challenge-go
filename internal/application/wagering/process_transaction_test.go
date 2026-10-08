package wagering

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagering"
	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/ports"
)

type fakeUnitOfWork struct{}

func (fakeUnitOfWork) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

func providerKey(providerID, key string) string { return providerID + "|" + key }

type fakeWalletRepo struct {
	mu      sync.Mutex
	wallets map[uuid.UUID]*wallet.Wallet
}

func newFakeWalletRepo() *fakeWalletRepo {
	return &fakeWalletRepo{wallets: map[uuid.UUID]*wallet.Wallet{}}
}

func (r *fakeWalletRepo) put(w *wallet.Wallet) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wallets[w.ID()] = w
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
	mu                       sync.Mutex
	byID                     map[uuid.UUID]*wagering.WagerTransaction
	byProviderIdempotencyKey map[string]uuid.UUID
	byProviderExternalID     map[string]uuid.UUID
}

func newFakeTransactionRepo() *fakeTransactionRepo {
	return &fakeTransactionRepo{
		byID:                     map[uuid.UUID]*wagering.WagerTransaction{},
		byProviderIdempotencyKey: map[string]uuid.UUID{},
		byProviderExternalID:     map[string]uuid.UUID{},
	}
}

func (r *fakeTransactionRepo) InsertIfAbsent(_ context.Context, tx *wagering.WagerTransaction) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	extKey := providerKey(tx.ProviderID(), tx.ExternalTransactionID())
	if existingID, exists := r.byProviderExternalID[extKey]; exists {
		if r.byID[existingID].IdempotencyKey() != tx.IdempotencyKey() {
			return false, ports.ErrExternalTransactionAlreadyExists
		}
	}

	idemKey := providerKey(tx.ProviderID(), tx.IdempotencyKey())
	if _, exists := r.byProviderIdempotencyKey[idemKey]; exists {
		return false, nil
	}

	cp := *tx
	r.byID[tx.InternalID()] = &cp
	r.byProviderIdempotencyKey[idemKey] = tx.InternalID()
	r.byProviderExternalID[extKey] = tx.InternalID()
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

func (r *fakeTransactionRepo) FindByProviderAndIdempotencyKey(_ context.Context, providerID, idempotencyKey string) (*wagering.WagerTransaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byProviderIdempotencyKey[providerKey(providerID, idempotencyKey)]
	if !ok {
		return nil, ports.ErrNotFound
	}
	cp := *r.byID[id]
	return &cp, nil
}

func (r *fakeTransactionRepo) FindByProviderAndExternalTransactionID(_ context.Context, providerID, externalTransactionID string) (*wagering.WagerTransaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byProviderExternalID[providerKey(providerID, externalTransactionID)]
	if !ok {
		return nil, ports.ErrNotFound
	}
	cp := *r.byID[id]
	return &cp, nil
}

func (r *fakeTransactionRepo) LockForUpdate(ctx context.Context, transactionID uuid.UUID) (*wagering.WagerTransaction, error) {
	return r.FindByID(ctx, transactionID)
}

func (r *fakeTransactionRepo) HasSuccessfulReversal(_ context.Context, providerID, referenceExternalTransactionID string, kind wagering.Kind) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tx := range r.byID {
		if tx.ProviderID() == providerID &&
			tx.ReferenceExternalTransactionID() == referenceExternalTransactionID &&
			tx.Kind() == kind &&
			tx.Status() == wagering.StatusProcessed {
			return true, nil
		}
	}
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

func (r *fakeTransactionRepo) LockPendingReferenceBatch(_ context.Context, limit int) ([]*wagering.WagerTransaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*wagering.WagerTransaction
	for _, tx := range r.byID {
		if tx.Status() == wagering.StatusPendingReference {
			cp := *tx
			out = append(out, &cp)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
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

func (s *fakeOutboxStore) LockPendingBatch(_ context.Context, limit int) ([]ports.OutboxEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ports.OutboxEvent
	for _, e := range s.events {
		if e.PublishedAt == nil {
			out = append(out, e)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (s *fakeOutboxStore) MarkPublished(_ context.Context, eventID uuid.UUID, publishedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.events {
		if s.events[i].EventID == eventID {
			s.events[i].PublishedAt = &publishedAt
		}
	}
	return nil
}

func (s *fakeOutboxStore) RecordFailedAttempt(_ context.Context, eventID uuid.UUID, nextAttemptAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.events {
		if s.events[i].EventID == eventID {
			s.events[i].Attempts++
			s.events[i].NextAttemptAt = nextAttemptAt
		}
	}
	return nil
}

func (s *fakeOutboxStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func mustMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	m, err := money.NewMoneyFromString(amount, currency)
	require.NoError(t, err)
	return m
}

type testHarness struct {
	uc      *ProcessTransactionUseCase
	wallets *fakeWalletRepo
	txs     *fakeTransactionRepo
	ledger  *fakeLedgerRepo
	outbox  *fakeOutboxStore
	now     time.Time
}

func newTestHarness() *testHarness {
	fixedNow := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	h := &testHarness{
		wallets: newFakeWalletRepo(),
		txs:     newFakeTransactionRepo(),
		ledger:  &fakeLedgerRepo{},
		outbox:  &fakeOutboxStore{},
		now:     fixedNow,
	}
	h.uc = NewProcessTransactionUseCase(fakeUnitOfWork{}, h.wallets, h.txs, h.ledger, h.outbox, config.BackoffConfig{
		BaseInterval:   time.Second,
		Factor:         2,
		MaxInterval:    time.Minute,
		JitterFraction: 0,
	})
	h.uc.Now = func() time.Time { return h.now }
	return h
}

func (h *testHarness) openWallet(t *testing.T, playerID string, balance money.Money) *wallet.Wallet {
	t.Helper()
	w, err := wallet.NewWallet(uuid.New(), playerID, balance, h.now)
	require.NoError(t, err)
	h.wallets.put(w)
	return w
}

func betCommand(providerID, externalID, idempotencyKey, playerID string, walletID uuid.UUID, amount money.Money) ProcessTransactionCommand {
	return ProcessTransactionCommand{
		ProviderID:            providerID,
		ExternalTransactionID: externalID,
		IdempotencyKey:        idempotencyKey,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  wagering.KindBet,
		Money:                 amount,
	}
}

func winCommand(providerID, externalID, idempotencyKey, playerID string, walletID uuid.UUID, amount money.Money) ProcessTransactionCommand {
	cmd := betCommand(providerID, externalID, idempotencyKey, playerID, walletID, amount)
	cmd.Kind = wagering.KindWin
	return cmd
}

func reversalCommand(kind wagering.Kind, providerID, externalID, idempotencyKey, playerID string, walletID uuid.UUID, amount money.Money, referenceExternalID string) ProcessTransactionCommand {
	cmd := betCommand(providerID, externalID, idempotencyKey, playerID, walletID, amount)
	cmd.Kind = kind
	cmd.ReferenceExternalTransactionID = referenceExternalID
	return cmd
}

// --- tests ---------------------------------------------------------------------------------------

func TestProcessTransaction_BetDebitsWallet(t *testing.T) {
	h := newTestHarness()
	w := h.openWallet(t, "player-1", mustMoney(t, "100.00", "BRL"))

	cmd := betCommand("provider-a", "ext-1", "idem-1", "player-1", w.ID(), mustMoney(t, "30.00", "BRL"))
	result, err := h.uc.Execute(context.Background(), cmd)

	require.NoError(t, err)
	assert.Equal(t, wagering.StatusProcessed, result.Status)
	assert.False(t, result.IdempotentReplay)
	assert.Equal(t, int64(7000), result.Balance.MinorUnits())
	assert.Equal(t, int64(2), result.WalletVersion)
	assert.Equal(t, 2, h.outbox.count(), "expects WagerTransactionProcessed + WalletBalanceChanged")
}

func TestProcessTransaction_BetInsufficientBalanceIsRejectedNotPanicked(t *testing.T) {
	h := newTestHarness()
	w := h.openWallet(t, "player-1", mustMoney(t, "10.00", "BRL"))

	cmd := betCommand("provider-a", "ext-1", "idem-1", "player-1", w.ID(), mustMoney(t, "80.00", "BRL"))
	result, err := h.uc.Execute(context.Background(), cmd)

	require.NoError(t, err, "a business rejection must never surface as a Go error")
	assert.Equal(t, wagering.StatusRejected, result.Status)
	require.NotNil(t, result.FailureCode)
	assert.Equal(t, wagering.FailureCodeInsufficientBalance, *result.FailureCode)
	// The wallet itself must be left completely untouched by a rejected debit.
	assert.Equal(t, int64(1000), w.Balance().MinorUnits())
	assert.Equal(t, int64(1), w.Version())
}

func TestProcessTransaction_IdempotentReplaySamePayload(t *testing.T) {
	h := newTestHarness()
	w := h.openWallet(t, "player-1", mustMoney(t, "100.00", "BRL"))

	cmd := betCommand("provider-a", "ext-1", "idem-1", "player-1", w.ID(), mustMoney(t, "30.00", "BRL"))

	first, err := h.uc.Execute(context.Background(), cmd)
	require.NoError(t, err)
	require.False(t, first.IdempotentReplay)

	second, err := h.uc.Execute(context.Background(), cmd)
	require.NoError(t, err)

	assert.True(t, second.IdempotentReplay)
	assert.Equal(t, first.TransactionID, second.TransactionID)
	assert.Equal(t, first.Status, second.Status)
	assert.True(t, first.Balance.Equal(second.Balance), "replay must return the balance of the original processing, not a recomputed one")
	assert.Equal(t, first.WalletVersion, second.WalletVersion)

	assert.Equal(t, int64(7000), w.Balance().MinorUnits())
	assert.Equal(t, int64(2), w.Version())
	assert.Equal(t, 2, h.outbox.count(), "the replay must not enqueue a second set of events")
}

func TestProcessTransaction_ReplayOfRejectedTransactionIsAlsoIdempotent(t *testing.T) {
	h := newTestHarness()
	w := h.openWallet(t, "player-1", mustMoney(t, "10.00", "BRL"))

	cmd := betCommand("provider-a", "ext-1", "idem-1", "player-1", w.ID(), mustMoney(t, "80.00", "BRL"))

	first, err := h.uc.Execute(context.Background(), cmd)
	require.NoError(t, err)
	require.False(t, first.IdempotentReplay)
	require.Equal(t, wagering.StatusRejected, first.Status)

	second, err := h.uc.Execute(context.Background(), cmd)
	require.NoError(t, err)

	assert.True(t, second.IdempotentReplay)
	assert.Equal(t, wagering.StatusRejected, second.Status)
	require.NotNil(t, second.FailureCode)
	assert.Equal(t, *first.FailureCode, *second.FailureCode)
	assert.Equal(t, money.Money{}, second.Balance)
	assert.Equal(t, 1, h.outbox.count())
}

func TestProcessTransaction_IdempotencyKeyConflict(t *testing.T) {
	h := newTestHarness()
	w := h.openWallet(t, "player-1", mustMoney(t, "100.00", "BRL"))

	first := betCommand("provider-a", "ext-1", "idem-1", "player-1", w.ID(), mustMoney(t, "30.00", "BRL"))
	_, err := h.uc.Execute(context.Background(), first)
	require.NoError(t, err)

	conflicting := betCommand("provider-a", "ext-1", "idem-1", "player-1", w.ID(), mustMoney(t, "31.00", "BRL"))
	_, err = h.uc.Execute(context.Background(), conflicting)

	require.Error(t, err)
	assert.True(t, errors.Is(err, ports.ErrIdempotencyKeyConflict))
	var conflictErr *IdempotencyKeyConflictError
	require.True(t, errors.As(err, &conflictErr))
	assert.Equal(t, "provider-a", conflictErr.ProviderID)
	assert.Equal(t, "idem-1", conflictErr.IdempotencyKey)

	assert.Equal(t, int64(7000), w.Balance().MinorUnits())
	assert.Equal(t, 2, h.outbox.count())
}

func TestProcessTransaction_ExternalTransactionAlreadyExists(t *testing.T) {
	h := newTestHarness()
	w := h.openWallet(t, "player-1", mustMoney(t, "100.00", "BRL"))

	first := betCommand("provider-a", "ext-1", "idem-1", "player-1", w.ID(), mustMoney(t, "30.00", "BRL"))
	_, err := h.uc.Execute(context.Background(), first)
	require.NoError(t, err)

	reapplied := betCommand("provider-a", "ext-1", "idem-2", "player-1", w.ID(), mustMoney(t, "30.00", "BRL"))
	_, err = h.uc.Execute(context.Background(), reapplied)

	require.Error(t, err)
	assert.True(t, errors.Is(err, ports.ErrExternalTransactionAlreadyExists))
	var alreadyExistsErr *ExternalTransactionAlreadyExistsError
	require.True(t, errors.As(err, &alreadyExistsErr))
	assert.Equal(t, "provider-a", alreadyExistsErr.ProviderID)
	assert.Equal(t, "ext-1", alreadyExistsErr.ExternalTransactionID)

	assert.Equal(t, int64(7000), w.Balance().MinorUnits())
	assert.Equal(t, 2, h.outbox.count())
}

func TestProcessTransaction_RejectsInternalOpeningKind(t *testing.T) {
	h := newTestHarness()
	w := h.openWallet(t, "player-1", mustMoney(t, "0.00", "BRL"))

	cmd := betCommand("provider-a", "ext-1", "idem-1", "player-1", w.ID(), mustMoney(t, "10.00", "BRL"))
	cmd.Kind = wagering.KindOpening

	_, err := h.uc.Execute(context.Background(), cmd)

	require.Error(t, err)
	assert.True(t, errors.Is(err, wagering.ErrInvalidKindForExternal))
	assert.Equal(t, 0, h.outbox.count())
}

func TestProcessTransaction_RefundSuccessfullyReversesProcessedBet(t *testing.T) {
	h := newTestHarness()
	w := h.openWallet(t, "player-1", mustMoney(t, "100.00", "BRL"))

	betCmd := betCommand("provider-a", "ext-bet", "idem-bet", "player-1", w.ID(), mustMoney(t, "30.00", "BRL"))
	betResult, err := h.uc.Execute(context.Background(), betCmd)
	require.NoError(t, err)
	require.Equal(t, wagering.StatusProcessed, betResult.Status)
	require.Equal(t, int64(7000), betResult.Balance.MinorUnits())

	refundCmd := reversalCommand(wagering.KindRefund, "provider-a", "ext-refund", "idem-refund", "player-1", w.ID(), mustMoney(t, "30.00", "BRL"), "ext-bet")
	refundResult, err := h.uc.Execute(context.Background(), refundCmd)

	require.NoError(t, err)
	assert.Equal(t, wagering.StatusProcessed, refundResult.Status)
	assert.Equal(t, int64(10000), refundResult.Balance.MinorUnits(), "a REFUND must restore the exact BET amount")
	assert.Equal(t, int64(3), refundResult.WalletVersion)
	assert.Equal(t, int64(10000), w.Balance().MinorUnits())
	assert.Equal(t, int64(3), w.Version())

	entries, _, err := h.ledger.ListByWallet(context.Background(), w.ID(), "", 10)
	require.NoError(t, err)
	require.Len(t, entries, 2, "one DEBIT for the BET, one CREDIT for the REFUND")
	assert.Equal(t, wallet.DirectionDebit, entries[0].Direction())
	assert.Equal(t, wallet.DirectionCredit, entries[1].Direction())
}

func TestProcessTransaction_RollbackSuccessfullyReversesProcessedWin(t *testing.T) {
	h := newTestHarness()
	w := h.openWallet(t, "player-1", mustMoney(t, "100.00", "BRL"))

	winCmd := winCommand("provider-a", "ext-win", "idem-win", "player-1", w.ID(), mustMoney(t, "20.00", "BRL"))
	winResult, err := h.uc.Execute(context.Background(), winCmd)
	require.NoError(t, err)
	require.Equal(t, wagering.StatusProcessed, winResult.Status)
	require.Equal(t, int64(12000), winResult.Balance.MinorUnits())

	rollbackCmd := reversalCommand(wagering.KindRollback, "provider-a", "ext-rollback", "idem-rollback", "player-1", w.ID(), mustMoney(t, "20.00", "BRL"), "ext-win")
	rollbackResult, err := h.uc.Execute(context.Background(), rollbackCmd)

	require.NoError(t, err)
	assert.Equal(t, wagering.StatusProcessed, rollbackResult.Status)
	assert.Equal(t, int64(10000), rollbackResult.Balance.MinorUnits(), "a ROLLBACK of a WIN must debit the exact WIN amount back out")
	assert.Equal(t, int64(3), rollbackResult.WalletVersion)
}

func TestProcessTransaction_SecondReversalOfSameReferenceIsRejected(t *testing.T) {
	h := newTestHarness()
	w := h.openWallet(t, "player-1", mustMoney(t, "100.00", "BRL"))

	betCmd := betCommand("provider-a", "ext-bet", "idem-bet", "player-1", w.ID(), mustMoney(t, "30.00", "BRL"))
	_, err := h.uc.Execute(context.Background(), betCmd)
	require.NoError(t, err)

	firstRefund := reversalCommand(wagering.KindRefund, "provider-a", "ext-refund-1", "idem-refund-1", "player-1", w.ID(), mustMoney(t, "30.00", "BRL"), "ext-bet")
	firstResult, err := h.uc.Execute(context.Background(), firstRefund)
	require.NoError(t, err)
	require.Equal(t, wagering.StatusProcessed, firstResult.Status)

	secondRefund := reversalCommand(wagering.KindRefund, "provider-a", "ext-refund-2", "idem-refund-2", "player-1", w.ID(), mustMoney(t, "30.00", "BRL"), "ext-bet")
	secondResult, err := h.uc.Execute(context.Background(), secondRefund)

	require.NoError(t, err, "a business rejection must never surface as a Go error")
	assert.Equal(t, wagering.StatusRejected, secondResult.Status)
	require.NotNil(t, secondResult.FailureCode)
	assert.Equal(t, wagering.FailureCodeReferenceAlreadyReversed, *secondResult.FailureCode)
	assert.Equal(t, int64(10000), w.Balance().MinorUnits(), "the rejected duplicate reversal must not move money a second time")
	assert.Equal(t, int64(3), w.Version())
}

func TestProcessTransaction_RollbackRejectedForInsufficientBalanceUsesDistinctFailureCode(t *testing.T) {
	h := newTestHarness()
	w := h.openWallet(t, "player-1", mustMoney(t, "100.00", "BRL"))

	winCmd := winCommand("provider-a", "ext-win", "idem-win", "player-1", w.ID(), mustMoney(t, "50.00", "BRL"))
	_, err := h.uc.Execute(context.Background(), winCmd)
	require.NoError(t, err)

	betCmd := betCommand("provider-a", "ext-bet", "idem-bet", "player-1", w.ID(), mustMoney(t, "140.00", "BRL"))
	betResult, err := h.uc.Execute(context.Background(), betCmd)
	require.NoError(t, err)
	require.Equal(t, wagering.StatusProcessed, betResult.Status)
	require.Equal(t, int64(1000), betResult.Balance.MinorUnits())

	rollbackCmd := reversalCommand(wagering.KindRollback, "provider-a", "ext-rollback", "idem-rollback", "player-1", w.ID(), mustMoney(t, "50.00", "BRL"), "ext-win")
	rollbackResult, err := h.uc.Execute(context.Background(), rollbackCmd)

	require.NoError(t, err)
	assert.Equal(t, wagering.StatusRejected, rollbackResult.Status)
	require.NotNil(t, rollbackResult.FailureCode)
	assert.Equal(t, wagering.FailureCodeInsufficientBalanceForReversal, *rollbackResult.FailureCode)
	assert.NotEqual(t, wagering.FailureCodeInsufficientBalance, *rollbackResult.FailureCode,
		"a reversal rejected for lack of balance must use a distinct failureCode from a plain BET rejection")
	assert.Equal(t, int64(1000), w.Balance().MinorUnits(), "the failed rollback must leave the wallet untouched")
}

package wallet

import (
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/money"
)

type Wallet struct {
	id        uuid.UUID
	playerID  string
	currency  string
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

func NewWallet(id uuid.UUID, playerID string, initialBalance money.Money, now time.Time) (*Wallet, error) {
	if playerID == "" {
		return nil, wrapInvalidWallet("playerID is required")
	}
	if initialBalance.IsNegative() {
		return nil, wrapInvalidWallet("initial balance must not be negative")
	}

	return &Wallet{
		id:        id,
		playerID:  playerID,
		currency:  initialBalance.Currency(),
		balance:   initialBalance,
		version:   1,
		createdAt: now,
		updatedAt: now,
	}, nil
}

func Rehydrate(
	id uuid.UUID,
	playerID string,
	currency string,
	balanceMinor int64,
	version int64,
	createdAt time.Time,
	updatedAt time.Time,
) (*Wallet, error) {
	if playerID == "" {
		return nil, wrapInvalidWallet("playerID is required")
	}
	if version < 1 {
		return nil, wrapInvalidWallet("version must be >= 1")
	}

	balance, err := money.FromMinorUnits(balanceMinor, currency)
	if err != nil {
		return nil, err
	}
	if balance.IsNegative() {
		return nil, wrapInvalidWallet("persisted balance must not be negative")
	}

	return &Wallet{
		id:        id,
		playerID:  playerID,
		currency:  balance.Currency(),
		balance:   balance,
		version:   version,
		createdAt: createdAt,
		updatedAt: updatedAt,
	}, nil
}

func (w *Wallet) ID() uuid.UUID { return w.id }

func (w *Wallet) PlayerID() string { return w.playerID }

func (w *Wallet) Currency() string { return w.currency }

func (w *Wallet) Balance() money.Money { return w.balance }

func (w *Wallet) Version() int64 { return w.version }

func (w *Wallet) CreatedAt() time.Time { return w.createdAt }

func (w *Wallet) UpdatedAt() time.Time { return w.updatedAt }

func (w *Wallet) Debit(amount money.Money, transactionID uuid.UUID, now time.Time) (WalletLedgerEntry, error) {
	if err := w.checkMovement(amount); err != nil {
		return WalletLedgerEntry{}, err
	}

	newBalance, err := w.balance.Sub(amount)
	if err != nil {
		return WalletLedgerEntry{}, err
	}
	if newBalance.IsNegative() {
		return WalletLedgerEntry{}, &InsufficientFundsError{
			WalletID:  w.id.String(),
			Available: w.balance,
			Requested: amount,
		}
	}

	entry, err := NewWalletLedgerEntry(uuid.New(), w.id, transactionID, DirectionDebit, amount, w.balance, newBalance, now)
	if err != nil {
		return WalletLedgerEntry{}, err
	}

	w.applyBalanceChange(newBalance, now)
	return entry, nil
}

func (w *Wallet) Credit(amount money.Money, transactionID uuid.UUID, now time.Time) (WalletLedgerEntry, error) {
	if err := w.checkMovement(amount); err != nil {
		return WalletLedgerEntry{}, err
	}

	newBalance, err := w.balance.Add(amount)
	if err != nil {
		return WalletLedgerEntry{}, err
	}

	entry, err := NewWalletLedgerEntry(uuid.New(), w.id, transactionID, DirectionCredit, amount, w.balance, newBalance, now)
	if err != nil {
		return WalletLedgerEntry{}, err
	}

	w.applyBalanceChange(newBalance, now)
	return entry, nil
}

func (w *Wallet) checkMovement(amount money.Money) error {
	if amount.Currency() != w.currency {
		return &CurrencyMismatchError{WalletCurrency: w.currency, MovementCurrency: amount.Currency()}
	}
	if !amount.IsPositive() {
		return ErrInvalidAmount
	}
	return nil
}

func (w *Wallet) applyBalanceChange(newBalance money.Money, now time.Time) {
	w.balance = newBalance
	w.version++
	w.updatedAt = now
}

func wrapInvalidWallet(reason string) error {
	return &invalidWalletError{reason: reason}
}

type invalidWalletError struct {
	reason string
}

func (e *invalidWalletError) Error() string { return "wallet: invalid wallet state: " + e.reason }

func (e *invalidWalletError) Unwrap() error { return ErrInvalidWallet }

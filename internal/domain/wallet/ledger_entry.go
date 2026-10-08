package wallet

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/money"
)

type Direction string

const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

type WalletLedgerEntry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	createdAt     time.Time
}

func NewWalletLedgerEntry(
	id uuid.UUID,
	walletID uuid.UUID,
	transactionID uuid.UUID,
	direction Direction,
	amount money.Money,
	balanceBefore money.Money,
	balanceAfter money.Money,
	createdAt time.Time,
) (WalletLedgerEntry, error) {
	if direction != DirectionDebit && direction != DirectionCredit {
		return WalletLedgerEntry{}, fmt.Errorf("%w: unknown direction %q", ErrInvalidLedgerEntry, direction)
	}
	if !amount.IsPositive() {
		return WalletLedgerEntry{}, fmt.Errorf("%w: amount must be positive, got %s", ErrInvalidLedgerEntry, amount.String())
	}
	if balanceBefore.Currency() != amount.Currency() || balanceAfter.Currency() != amount.Currency() {
		return WalletLedgerEntry{}, &CurrencyMismatchError{
			WalletCurrency:   balanceBefore.Currency(),
			MovementCurrency: amount.Currency(),
		}
	}
	if balanceBefore.IsNegative() || balanceAfter.IsNegative() {
		return WalletLedgerEntry{}, fmt.Errorf("%w: balances must never be negative", ErrInvalidLedgerEntry)
	}

	var expectedAfter money.Money
	var err error
	switch direction {
	case DirectionCredit:
		expectedAfter, err = balanceBefore.Add(amount)
	case DirectionDebit:
		expectedAfter, err = balanceBefore.Sub(amount)
	}
	if err != nil {
		return WalletLedgerEntry{}, fmt.Errorf("%w: %v", ErrInvalidLedgerEntry, err)
	}
	if !expectedAfter.Equal(balanceAfter) {
		return WalletLedgerEntry{}, fmt.Errorf(
			"%w: balanceAfter (%s) does not equal balanceBefore (%s) %s amount (%s)",
			ErrInvalidLedgerEntry, balanceAfter.String(), balanceBefore.String(), direction, amount.String(),
		)
	}

	return WalletLedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     createdAt,
	}, nil
}

func (e WalletLedgerEntry) ID() uuid.UUID { return e.id }

func (e WalletLedgerEntry) WalletID() uuid.UUID { return e.walletID }

func (e WalletLedgerEntry) TransactionID() uuid.UUID { return e.transactionID }

func (e WalletLedgerEntry) Direction() Direction { return e.direction }

func (e WalletLedgerEntry) Amount() money.Money { return e.amount }

func (e WalletLedgerEntry) BalanceBefore() money.Money { return e.balanceBefore }

func (e WalletLedgerEntry) BalanceAfter() money.Money { return e.balanceAfter }

func (e WalletLedgerEntry) CreatedAt() time.Time { return e.createdAt }

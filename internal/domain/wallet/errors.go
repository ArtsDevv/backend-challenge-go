package wallet

import (
	"errors"
	"fmt"

	"backend-challenge-go/internal/domain/money"
)

var (
	ErrInsufficientFunds     = errors.New("wallet: insufficient funds")
	ErrCurrencyMismatch      = errors.New("wallet: currency mismatch")
	ErrWalletAlreadyExists   = errors.New("wallet: wallet already exists")
	ErrWalletVersionConflict = errors.New("wallet: version conflict")
	ErrInvalidAmount         = errors.New("wallet: amount must be positive")
	ErrInvalidWallet         = errors.New("wallet: invalid wallet state")
	ErrInvalidLedgerEntry    = errors.New("wallet: invalid ledger entry")
)

type InsufficientFundsError struct {
	WalletID  string
	Available money.Money
	Requested money.Money
}

func (e *InsufficientFundsError) Error() string {
	return fmt.Sprintf("wallet %s: insufficient funds: available=%s requested=%s",
		e.WalletID, e.Available.String(), e.Requested.String())
}

func (e *InsufficientFundsError) Unwrap() error { return ErrInsufficientFunds }

type CurrencyMismatchError struct {
	WalletCurrency   string
	MovementCurrency string
}

func (e *CurrencyMismatchError) Error() string {
	return fmt.Sprintf("wallet: currency mismatch: wallet=%s movement=%s",
		e.WalletCurrency, e.MovementCurrency)
}

func (e *CurrencyMismatchError) Unwrap() error { return ErrCurrencyMismatch }

type WalletAlreadyExistsError struct {
	PlayerID string
	Currency string
}

func (e *WalletAlreadyExistsError) Error() string {
	return fmt.Sprintf("wallet: wallet already exists for playerId=%s currency=%s", e.PlayerID, e.Currency)
}

func (e *WalletAlreadyExistsError) Unwrap() error { return ErrWalletAlreadyExists }

type VersionConflictError struct {
	WalletID string
	Expected int64
	Actual   int64
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("wallet %s: version conflict: expected=%d actual=%d", e.WalletID, e.Expected, e.Actual)
}

func (e *VersionConflictError) Unwrap() error { return ErrWalletVersionConflict }

package wagering

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidTransition = errors.New("wagering: invalid state transition")

	ErrTerminalTransaction = errors.New("wagering: transaction is in a terminal state")

	ErrInvalidAmountForKind = errors.New("wagering: amount invalid for transaction kind")

	ErrInvalidKindForExternal = errors.New("wagering: kind not allowed for an external transaction")

	ErrMissingRequiredField = errors.New("wagering: missing required field")

	ErrReversalRequiresReference = errors.New("wagering: REFUND/ROLLBACK requires a reference external transaction id")

	ErrReferenceNotFound = errors.New("wagering: referenced transaction not found")

	ErrReferenceNotTerminal = errors.New("wagering: referenced transaction is not in a terminal processed state")

	ErrReferenceMismatch = errors.New("wagering: reversal does not match the referenced transaction")

	ErrReferenceAmountMismatch = errors.New("wagering: reversal amount does not match the referenced amount")

	ErrReferenceAlreadyReversed = errors.New("wagering: reference already has a successful reversal of this kind")

	ErrNotPendingReference = errors.New("wagering: transaction is not awaiting reference resolution")

	ErrInsufficientBalance            = errors.New("wagering: insufficient balance")
	ErrInsufficientBalanceForReversal = errors.New("wagering: insufficient balance for reversal")
)

type FailureCode string

const (
	FailureCodeInsufficientBalance            FailureCode = "INSUFFICIENT_BALANCE"
	FailureCodeInsufficientBalanceForReversal FailureCode = "INSUFFICIENT_BALANCE_FOR_REVERSAL"
	FailureCodeReferenceNotFound              FailureCode = "REFERENCE_NOT_FOUND"
	FailureCodeReferenceMismatch              FailureCode = "REFERENCE_MISMATCH"
	FailureCodeReferenceNotTerminal           FailureCode = "REFERENCE_NOT_TERMINAL"
	FailureCodeReferenceAmountMismatch        FailureCode = "REFERENCE_AMOUNT_MISMATCH"
	FailureCodeReferenceAlreadyReversed       FailureCode = "REFERENCE_ALREADY_REVERSED"
	FailureCodeCurrencyMismatch               FailureCode = "CURRENCY_MISMATCH"
	FailureCodeWalletNotFound                 FailureCode = "WALLET_NOT_FOUND"
)

type InvalidTransitionError struct {
	From Status
	To   Status
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("wagering: cannot transition from %s to %s", e.From, e.To)
}

func (e *InvalidTransitionError) Unwrap() error { return ErrInvalidTransition }

type TerminalTransactionError struct {
	Status Status
}

func (e *TerminalTransactionError) Error() string {
	return fmt.Sprintf("wagering: transaction is terminal (status=%s), it is immutable", e.Status)
}

func (e *TerminalTransactionError) Unwrap() error { return ErrTerminalTransaction }

type InvalidAmountForKindError struct {
	Kind   Kind
	Reason string
}

func (e *InvalidAmountForKindError) Error() string {
	return fmt.Sprintf("wagering: invalid amount for kind %s: %s", e.Kind, e.Reason)
}

func (e *InvalidAmountForKindError) Unwrap() error { return ErrInvalidAmountForKind }

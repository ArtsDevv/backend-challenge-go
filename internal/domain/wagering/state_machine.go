package wagering

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusPending          Status = "PENDING"
	StatusPendingReference Status = "PENDING_REFERENCE"
	StatusProcessed        Status = "PROCESSED"
	StatusRejected         Status = "REJECTED"
	StatusFailed           Status = "FAILED"
)

func (t *WagerTransaction) IsTerminal() bool {
	return isTerminalStatus(t.status)
}

func isTerminalStatus(s Status) bool {
	switch s {
	case StatusProcessed, StatusRejected, StatusFailed:
		return true
	default:
		return false
	}
}

var allowedTransitions = map[Status]map[Status]bool{
	StatusPending: {
		StatusPendingReference: true,
		StatusProcessed:        true,
		StatusRejected:         true,
		StatusFailed:           true,
	},
	StatusPendingReference: {
		StatusProcessed: true,
		StatusRejected:  true,
		StatusFailed:    true,
	},
}

func (t *WagerTransaction) transitionTo(to Status, now time.Time) error {
	if t.IsTerminal() {
		return &TerminalTransactionError{Status: t.status}
	}
	if !allowedTransitions[t.status][to] {
		return &InvalidTransitionError{From: t.status, To: to}
	}
	t.status = to
	t.updatedAt = now
	return nil
}

func (t *WagerTransaction) MarkPendingReference(now time.Time) error {
	if err := t.transitionTo(StatusPendingReference, now); err != nil {
		return err
	}
	if t.pendingSince == nil {
		since := now
		t.pendingSince = &since
	}
	return nil
}

func (t *WagerTransaction) RecordPendingReferenceAttempt(nextAttemptAt, now time.Time) error {
	if t.status != StatusPendingReference {
		return ErrNotPendingReference
	}
	t.pendingReferenceAttempts++
	t.pendingReferenceNextAttemptAt = &nextAttemptAt
	t.updatedAt = now
	return nil
}

func (t *WagerTransaction) PendingReferenceExceeded(maxAttempts int, ttl time.Duration, now time.Time) bool {
	if t.status != StatusPendingReference {
		return false
	}
	if t.pendingReferenceAttempts >= maxAttempts {
		return true
	}
	if t.pendingSince != nil && now.Sub(*t.pendingSince) >= ttl {
		return true
	}
	return false
}

func (t *WagerTransaction) ResolveReference(referenceTransactionID uuid.UUID, now time.Time) error {
	if t.IsTerminal() {
		return &TerminalTransactionError{Status: t.status}
	}
	id := referenceTransactionID
	t.referenceTransactionID = &id
	t.updatedAt = now
	return nil
}

func (t *WagerTransaction) MarkProcessed(resultBalanceMinor, resultWalletVersion int64, now time.Time) error {
	if err := t.transitionTo(StatusProcessed, now); err != nil {
		return err
	}
	t.resultBalanceMinor = &resultBalanceMinor
	t.resultWalletVersion = &resultWalletVersion
	t.processedAt = &now
	return nil
}

func (t *WagerTransaction) MarkRejected(code FailureCode, now time.Time) error {
	if code == "" {
		return fmt.Errorf("%w: failureCode", ErrMissingRequiredField)
	}
	if err := t.transitionTo(StatusRejected, now); err != nil {
		return err
	}
	t.failureCode = &code
	t.processedAt = &now
	return nil
}

func (t *WagerTransaction) MarkFailed(code FailureCode, now time.Time) error {
	if code == "" {
		return fmt.Errorf("%w: failureCode", ErrMissingRequiredField)
	}
	if err := t.transitionTo(StatusFailed, now); err != nil {
		return err
	}
	t.failureCode = &code
	t.processedAt = &now
	return nil
}

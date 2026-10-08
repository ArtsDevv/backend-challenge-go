package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"backend-challenge-go/internal/platform/metrics"
	"backend-challenge-go/internal/ports"
)

const (
	sqlStateLockTimeout       = "55P03" // SET LOCAL lock_timeout expired waiting on a row lock.
	sqlStateSerializationFail = "40001" // serialization_failure
	sqlStateDeadlockDetected  = "40P01" // deadlock_detected
	sqlStateUniqueViolation   = "23505" // unique_violation; each repository inspects ConstraintName itself.
)

const lockTimeout = "3s"

const (
	maxTxAttempts = 3
	retryMinDelay = 10 * time.Millisecond
	retryMaxDelay = 50 * time.Millisecond
)

type txCtxKey struct{}

func txFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txCtxKey{}).(pgx.Tx)
	return tx, ok
}

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func dbFromContext(ctx context.Context, pool *pgxpool.Pool) querier {
	if tx, ok := txFromContext(ctx); ok {
		return tx
	}
	return pool
}

func requireTx(ctx context.Context, method string) (pgx.Tx, error) {
	tx, ok := txFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("postgres: %s: no transaction in context; must be called inside UnitOfWork.WithinTx", method)
	}
	return tx, nil
}

func classifyError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case sqlStateLockTimeout:
			return fmt.Errorf("postgres: %w", ports.ErrWalletLockTimeout)
		case sqlStateSerializationFail, sqlStateDeadlockDetected:
			return fmt.Errorf("postgres: %w", ports.ErrSerializationFailure)
		default:
			return err
		}
	}
	return fmt.Errorf("postgres: %v: %w", err, ports.ErrRepositoryUnavailable)
}

type TxManager struct {
	pool    *pgxpool.Pool
	metrics *metrics.Metrics
}

var _ ports.UnitOfWork = (*TxManager)(nil)

type TxManagerOption func(*TxManager)

func WithMetrics(m *metrics.Metrics) TxManagerOption {
	return func(t *TxManager) { t.metrics = m }
}

func NewTxManager(pool *pgxpool.Pool, opts ...TxManagerOption) *TxManager {
	t := &TxManager{pool: pool}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

func (m *TxManager) recordConflict(err error) {
	if m.metrics == nil {
		return
	}
	switch {
	case errors.Is(err, ports.ErrWalletLockTimeout):
		m.metrics.ConcurrencyConflicts.WithLabelValues(metrics.ComponentPostgres, metrics.ConflictLockTimeout).Inc()
	case errors.Is(err, ports.ErrSerializationFailure):
		m.metrics.ConcurrencyConflicts.WithLabelValues(metrics.ComponentPostgres, metrics.ConflictSerializationFailure).Inc()
	}
}

func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	var lastErr error
	for attempt := 0; attempt < maxTxAttempts; attempt++ {
		err := m.attempt(ctx, fn)
		if err == nil {
			return nil
		}
		lastErr = err
		m.recordConflict(err)
		if attempt < maxTxAttempts-1 && errors.Is(err, ports.ErrSerializationFailure) {
			select {
			case <-time.After(randomRetryDelay()):
			case <-ctx.Done():
				return lastErr
			}
			continue
		}
		return err
	}
	return lastErr
}

func (m *TxManager) attempt(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	tx, beginErr := m.pool.Begin(ctx)
	if beginErr != nil {
		return classifyError(beginErr)
	}

	defer func() {
		if p := recover(); p != nil {
			// context.WithoutCancel: even if the caller's ctx is already done (e.g. a shutdown in
			// progress), the rollback still needs to reach the server so the connection returns to
			// the pool in a clean state instead of being discarded.
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
	}()

	if _, execErr := tx.Exec(ctx, "SET LOCAL lock_timeout = '"+lockTimeout+"'"); execErr != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return classifyError(execErr)
	}

	txCtx := context.WithValue(ctx, txCtxKey{}, tx)
	if fnErr := fn(txCtx); fnErr != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return fnErr
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return classifyError(commitErr)
	}
	return nil
}

func randomRetryDelay() time.Duration {
	span := retryMaxDelay - retryMinDelay
	return retryMinDelay + time.Duration(rand.Int63n(int64(span)+1))
}

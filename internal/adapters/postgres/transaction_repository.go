package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagering"
	"backend-challenge-go/internal/ports"
)

const wagerTransactionColumns = `
	id, external_transaction_id, provider_id, idempotency_key, payload_hash,
	wallet_id, player_id, round_id, game_id, kind, amount_minor, currency,
	reference_external_transaction_id, reference_transaction_id, status, failure_code,
	result_balance_minor, result_wallet_version, pending_reference_attempts,
	pending_reference_next_attempt_at, pending_since, created_at, updated_at, processed_at`

type TransactionRepository struct {
	pool *pgxpool.Pool
}

var _ ports.TransactionRepository = (*TransactionRepository)(nil)

func NewTransactionRepository(pool *pgxpool.Pool) *TransactionRepository {
	return &TransactionRepository{pool: pool}
}

func (r *TransactionRepository) InsertIfAbsent(ctx context.Context, t *wagering.WagerTransaction) (bool, error) {
	tx, err := requireTx(ctx, "TransactionRepository.InsertIfAbsent")
	if err != nil {
		return false, err
	}

	const q = `
		INSERT INTO wager_transactions (
			id, external_transaction_id, provider_id, idempotency_key, payload_hash,
			wallet_id, player_id, round_id, game_id, kind, amount_minor, currency,
			reference_external_transaction_id, status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (provider_id, idempotency_key) DO NOTHING
		RETURNING id`

	var returnedID uuid.UUID
	err = tx.QueryRow(ctx, q,
		t.InternalID(),
		nullableString(t.ExternalTransactionID()),
		nullableString(t.ProviderID()),
		nullableString(t.IdempotencyKey()),
		nullableString(t.PayloadHash()),
		t.WalletID(),
		t.PlayerID(),
		nullableString(t.RoundID()),
		nullableString(t.GameID()),
		string(t.Kind()),
		t.Money().MinorUnits(),
		t.Money().Currency(),
		nullableString(t.ReferenceExternalTransactionID()),
		string(t.Status()),
		t.CreatedAt(),
		t.UpdatedAt(),
	).Scan(&returnedID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// ON CONFLICT DO NOTHING produced no row: another (committed) transaction already owns
			// this (providerId, idempotencyKey) pair.
			return false, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == sqlStateUniqueViolation && pgErr.ConstraintName == "wt_provider_external_tx_uidx" {
			return false, ports.ErrExternalTransactionAlreadyExists
		}
		return false, classifyError(err)
	}
	return true, nil
}

func (r *TransactionRepository) Insert(ctx context.Context, t *wagering.WagerTransaction) error {
	tx, err := requireTx(ctx, "TransactionRepository.Insert")
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO wager_transactions (
			id, wallet_id, player_id, kind, amount_minor, currency, status,
			result_balance_minor, result_wallet_version, created_at, updated_at, processed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	_, err = tx.Exec(ctx, q,
		t.InternalID(), t.WalletID(), t.PlayerID(), string(t.Kind()), t.Money().MinorUnits(), t.Money().Currency(), string(t.Status()),
		t.ResultBalanceMinor(), t.ResultWalletVersion(), t.CreatedAt(), t.UpdatedAt(), t.ProcessedAt(),
	)
	if err != nil {
		return classifyError(err)
	}
	return nil
}

func (r *TransactionRepository) FindByID(ctx context.Context, transactionID uuid.UUID) (*wagering.WagerTransaction, error) {
	const q = `SELECT ` + wagerTransactionColumns + ` FROM wager_transactions WHERE id = $1`
	return scanWagerTransaction(dbFromContext(ctx, r.pool).QueryRow(ctx, q, transactionID))
}

func (r *TransactionRepository) FindByProviderAndIdempotencyKey(ctx context.Context, providerID, idempotencyKey string) (*wagering.WagerTransaction, error) {
	const q = `SELECT ` + wagerTransactionColumns + ` FROM wager_transactions WHERE provider_id = $1 AND idempotency_key = $2`
	return scanWagerTransaction(dbFromContext(ctx, r.pool).QueryRow(ctx, q, providerID, idempotencyKey))
}

func (r *TransactionRepository) FindByProviderAndExternalTransactionID(ctx context.Context, providerID, externalTransactionID string) (*wagering.WagerTransaction, error) {
	const q = `SELECT ` + wagerTransactionColumns + ` FROM wager_transactions WHERE provider_id = $1 AND external_transaction_id = $2`
	return scanWagerTransaction(dbFromContext(ctx, r.pool).QueryRow(ctx, q, providerID, externalTransactionID))
}

func (r *TransactionRepository) LockForUpdate(ctx context.Context, transactionID uuid.UUID) (*wagering.WagerTransaction, error) {
	tx, err := requireTx(ctx, "TransactionRepository.LockForUpdate")
	if err != nil {
		return nil, err
	}
	const q = `SELECT ` + wagerTransactionColumns + ` FROM wager_transactions WHERE id = $1 FOR UPDATE`
	return scanWagerTransaction(tx.QueryRow(ctx, q, transactionID))
}

func (r *TransactionRepository) HasSuccessfulReversal(ctx context.Context, providerID, referenceExternalTransactionID string, kind wagering.Kind) (bool, error) {
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM wager_transactions
			WHERE provider_id = $1
			  AND reference_external_transaction_id = $2
			  AND kind = $3
			  AND status = 'PROCESSED'
		)`
	var exists bool
	err := dbFromContext(ctx, r.pool).QueryRow(ctx, q, providerID, referenceExternalTransactionID, string(kind)).Scan(&exists)
	if err != nil {
		return false, classifyError(err)
	}
	return exists, nil
}

func (r *TransactionRepository) Update(ctx context.Context, t *wagering.WagerTransaction) error {
	tx, err := requireTx(ctx, "TransactionRepository.Update")
	if err != nil {
		return err
	}
	const q = `
		UPDATE wager_transactions SET
			status = $1,
			failure_code = $2,
			result_balance_minor = $3,
			result_wallet_version = $4,
			reference_transaction_id = $5,
			pending_reference_attempts = $6,
			pending_reference_next_attempt_at = $7,
			pending_since = $8,
			updated_at = $9,
			processed_at = $10
		WHERE id = $11`

	var failureCode *string
	if t.FailureCode() != nil {
		code := string(*t.FailureCode())
		failureCode = &code
	}

	tag, err := tx.Exec(ctx, q,
		string(t.Status()), failureCode, t.ResultBalanceMinor(), t.ResultWalletVersion(), t.ReferenceTransactionID(),
		t.PendingReferenceAttempts(), t.PendingReferenceNextAttemptAt(), t.PendingSince(), t.UpdatedAt(), t.ProcessedAt(),
		t.InternalID(),
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == sqlStateUniqueViolation && pgErr.ConstraintName == "wt_single_successful_reversal_per_kind_uidx" {
			return errors.Join(wagering.ErrReferenceAlreadyReversed, err)
		}
		return classifyError(err)
	}
	if tag.RowsAffected() == 0 {
		return ports.ErrNotFound
	}
	return nil
}

func (r *TransactionRepository) LockPendingReferenceBatch(ctx context.Context, limit int) ([]*wagering.WagerTransaction, error) {
	tx, err := requireTx(ctx, "TransactionRepository.LockPendingReferenceBatch")
	if err != nil {
		return nil, err
	}
	const q = `
		SELECT ` + wagerTransactionColumns + `
		FROM wager_transactions
		WHERE status = 'PENDING_REFERENCE' AND pending_reference_next_attempt_at <= now()
		ORDER BY pending_reference_next_attempt_at
		FOR UPDATE SKIP LOCKED
		LIMIT $1`
	rows, err := tx.Query(ctx, q, limit)
	if err != nil {
		return nil, classifyError(err)
	}
	defer rows.Close()

	var batch []*wagering.WagerTransaction
	for rows.Next() {
		t, err := scanWagerTransaction(rows)
		if err != nil {
			return nil, err
		}
		batch = append(batch, t)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyError(err)
	}
	return batch, nil
}

func scanWagerTransaction(row pgx.Row) (*wagering.WagerTransaction, error) {
	var (
		id                             uuid.UUID
		externalTransactionID          *string
		providerID                     *string
		idempotencyKey                 *string
		payloadHash                    *string
		walletID                       uuid.UUID
		playerID                       string
		roundID                        *string
		gameID                         *string
		kind                           string
		amountMinor                    int64
		currency                       string
		referenceExternalTransactionID *string
		referenceTransactionID         *uuid.UUID
		status                         string
		failureCode                    *string
		resultBalanceMinor             *int64
		resultWalletVersion            *int64
		pendingReferenceAttempts       int
		pendingReferenceNextAttemptAt  *time.Time
		pendingSince                   *time.Time
		createdAt                      time.Time
		updatedAt                      time.Time
		processedAt                    *time.Time
	)
	err := row.Scan(
		&id, &externalTransactionID, &providerID, &idempotencyKey, &payloadHash,
		&walletID, &playerID, &roundID, &gameID, &kind, &amountMinor, &currency,
		&referenceExternalTransactionID, &referenceTransactionID, &status, &failureCode,
		&resultBalanceMinor, &resultWalletVersion, &pendingReferenceAttempts,
		&pendingReferenceNextAttemptAt, &pendingSince, &createdAt, &updatedAt, &processedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ports.ErrNotFound
		}
		return nil, classifyError(err)
	}

	m, err := money.FromMinorUnits(amountMinor, currency)
	if err != nil {
		return nil, err
	}

	var fc *wagering.FailureCode
	if failureCode != nil {
		code := wagering.FailureCode(*failureCode)
		fc = &code
	}

	return wagering.Rehydrate(wagering.RehydrateParams{
		InternalID:                     id,
		ExternalTransactionID:          derefString(externalTransactionID),
		ProviderID:                     derefString(providerID),
		IdempotencyKey:                 derefString(idempotencyKey),
		PayloadHash:                    derefString(payloadHash),
		WalletID:                       walletID,
		PlayerID:                       playerID,
		RoundID:                        derefString(roundID),
		GameID:                         derefString(gameID),
		Kind:                           wagering.Kind(kind),
		Money:                          m,
		ReferenceExternalTransactionID: derefString(referenceExternalTransactionID),
		ReferenceTransactionID:         referenceTransactionID,
		Status:                         wagering.Status(status),
		FailureCode:                    fc,
		ResultBalanceMinor:             resultBalanceMinor,
		ResultWalletVersion:            resultWalletVersion,
		PendingReferenceAttempts:       pendingReferenceAttempts,
		PendingReferenceNextAttemptAt:  pendingReferenceNextAttemptAt,
		PendingSince:                   pendingSince,
		CreatedAt:                      createdAt,
		UpdatedAt:                      updatedAt,
		ProcessedAt:                    processedAt,
	})
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

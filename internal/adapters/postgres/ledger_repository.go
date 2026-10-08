package postgres

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/ports"
)

type LedgerRepository struct {
	pool *pgxpool.Pool
}

var _ ports.LedgerRepository = (*LedgerRepository)(nil)

func NewLedgerRepository(pool *pgxpool.Pool) *LedgerRepository {
	return &LedgerRepository{pool: pool}
}

func (r *LedgerRepository) Insert(ctx context.Context, entry wallet.WalletLedgerEntry) error {
	tx, err := requireTx(ctx, "LedgerRepository.Insert")
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, amount_minor, currency,
			balance_before_minor, balance_after_minor, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	_, err = tx.Exec(ctx, q,
		entry.ID(), entry.WalletID(), entry.TransactionID(), string(entry.Direction()),
		entry.Amount().MinorUnits(), entry.Amount().Currency(),
		entry.BalanceBefore().MinorUnits(), entry.BalanceAfter().MinorUnits(), entry.CreatedAt(),
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == sqlStateUniqueViolation && pgErr.ConstraintName == "wle_wallet_transaction_unique" {
			return ports.ErrDuplicateLedgerEntry
		}
		return classifyError(err)
	}
	return nil
}

func (r *LedgerRepository) ListByWallet(ctx context.Context, walletID uuid.UUID, cursor string, limit int) ([]wallet.WalletLedgerEntry, string, error) {
	if limit <= 0 {
		limit = 50
	}

	db := dbFromContext(ctx, r.pool)
	const cols = `id, transaction_id, direction, amount_minor, currency, balance_before_minor, balance_after_minor, created_at`

	var (
		q    string
		args []any
	)
	if cursor == "" {
		q = `SELECT ` + cols + ` FROM wallet_ledger_entries WHERE wallet_id = $1 ORDER BY created_at, id LIMIT $2`
		args = []any{walletID, limit + 1}
	} else {
		c, err := decodeLedgerCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		q = `SELECT ` + cols + ` FROM wallet_ledger_entries
			WHERE wallet_id = $1 AND (created_at, id) > ($2, $3)
			ORDER BY created_at, id
			LIMIT $4`
		args = []any{walletID, c.createdAt, c.id, limit + 1}
	}

	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		return nil, "", classifyError(err)
	}
	defer rows.Close()

	var entries []wallet.WalletLedgerEntry
	for rows.Next() {
		var (
			id, transactionID                     uuid.UUID
			direction                             string
			amountMinor                           int64
			currency                              string
			balanceBeforeMinor, balanceAfterMinor int64
			createdAt                             time.Time
		)
		if err := rows.Scan(&id, &transactionID, &direction, &amountMinor, &currency, &balanceBeforeMinor, &balanceAfterMinor, &createdAt); err != nil {
			return nil, "", classifyError(err)
		}

		amount, err := money.FromMinorUnits(amountMinor, currency)
		if err != nil {
			return nil, "", err
		}
		before, err := money.FromMinorUnits(balanceBeforeMinor, currency)
		if err != nil {
			return nil, "", err
		}
		after, err := money.FromMinorUnits(balanceAfterMinor, currency)
		if err != nil {
			return nil, "", err
		}
		entry, err := wallet.NewWalletLedgerEntry(id, walletID, transactionID, wallet.Direction(direction), amount, before, after, createdAt)
		if err != nil {
			return nil, "", err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, "", classifyError(err)
	}

	var nextCursor string
	if len(entries) > limit {
		last := entries[limit-1]
		nextCursor = encodeLedgerCursor(ledgerCursor{createdAt: last.CreatedAt(), id: last.ID()})
		entries = entries[:limit]
	}
	return entries, nextCursor, nil
}

type ledgerCursor struct {
	createdAt time.Time
	id        uuid.UUID
}

func encodeLedgerCursor(c ledgerCursor) string {
	raw := c.createdAt.UTC().Format(time.RFC3339Nano) + "|" + c.id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeLedgerCursor(s string) (ledgerCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return ledgerCursor{}, fmt.Errorf("postgres: invalid ledger cursor: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return ledgerCursor{}, fmt.Errorf("postgres: invalid ledger cursor: malformed")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return ledgerCursor{}, fmt.Errorf("postgres: invalid ledger cursor: %w", err)
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return ledgerCursor{}, fmt.Errorf("postgres: invalid ledger cursor: %w", err)
	}
	return ledgerCursor{createdAt: t, id: id}, nil
}

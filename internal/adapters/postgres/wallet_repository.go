package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/ports"
)

const walletColumns = `id, player_id, currency, balance_minor, version, created_at, updated_at`

type WalletRepository struct {
	pool *pgxpool.Pool
}

var _ ports.WalletRepository = (*WalletRepository)(nil)

func NewWalletRepository(pool *pgxpool.Pool) *WalletRepository {
	return &WalletRepository{pool: pool}
}

func (r *WalletRepository) LockForUpdate(ctx context.Context, walletID uuid.UUID) (*wallet.Wallet, error) {
	tx, err := requireTx(ctx, "WalletRepository.LockForUpdate")
	if err != nil {
		return nil, err
	}
	const q = `SELECT ` + walletColumns + ` FROM wallets WHERE id = $1 FOR UPDATE`
	return scanWallet(tx.QueryRow(ctx, q, walletID))
}

func (r *WalletRepository) FindByID(ctx context.Context, walletID uuid.UUID) (*wallet.Wallet, error) {
	const q = `SELECT ` + walletColumns + ` FROM wallets WHERE id = $1`
	return scanWallet(dbFromContext(ctx, r.pool).QueryRow(ctx, q, walletID))
}

func (r *WalletRepository) FindByPlayerAndCurrency(ctx context.Context, playerID, currency string) (*wallet.Wallet, error) {
	const q = `SELECT ` + walletColumns + ` FROM wallets WHERE player_id = $1 AND currency = $2`
	return scanWallet(dbFromContext(ctx, r.pool).QueryRow(ctx, q, playerID, currency))
}

func (r *WalletRepository) Insert(ctx context.Context, w *wallet.Wallet) error {
	tx, err := requireTx(ctx, "WalletRepository.Insert")
	if err != nil {
		return err
	}
	const q = `INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
	_, err = tx.Exec(ctx, q, w.ID(), w.PlayerID(), w.Currency(), w.Balance().MinorUnits(), w.Version(), w.CreatedAt(), w.UpdatedAt())
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == sqlStateUniqueViolation && pgErr.ConstraintName == "wallets_player_currency_unique" {
			return &wallet.WalletAlreadyExistsError{PlayerID: w.PlayerID(), Currency: w.Currency()}
		}
		return classifyError(err)
	}
	return nil
}

func (r *WalletRepository) Update(ctx context.Context, w *wallet.Wallet, expectedVersion int64) error {
	tx, err := requireTx(ctx, "WalletRepository.Update")
	if err != nil {
		return err
	}
	const q = `UPDATE wallets SET balance_minor = $1, version = $2, updated_at = $3 WHERE id = $4 AND version = $5`
	tag, err := tx.Exec(ctx, q, w.Balance().MinorUnits(), w.Version(), w.UpdatedAt(), w.ID(), expectedVersion)
	if err != nil {
		return classifyError(err)
	}
	if tag.RowsAffected() == 0 {
		return &wallet.VersionConflictError{WalletID: w.ID().String(), Expected: expectedVersion, Actual: w.Version()}
	}
	return nil
}

func scanWallet(row pgx.Row) (*wallet.Wallet, error) {
	var (
		id                    uuid.UUID
		playerID, currency    string
		balanceMinor, version int64
		createdAt, updatedAt  time.Time
	)
	if err := row.Scan(&id, &playerID, &currency, &balanceMinor, &version, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ports.ErrNotFound
		}
		return nil, classifyError(err)
	}
	return wallet.Rehydrate(id, playerID, currency, balanceMinor, version, createdAt, updatedAt)
}

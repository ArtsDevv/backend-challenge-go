package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"backend-challenge-go/internal/ports"
)

type InboxRepository struct {
	pool *pgxpool.Pool
}

var _ ports.InboxStore = (*InboxRepository)(nil)

func NewInboxRepository(pool *pgxpool.Pool) *InboxRepository {
	return &InboxRepository{pool: pool}
}

func (r *InboxRepository) ReserveOrGet(ctx context.Context, consumerName, messageID, messageHash string) (ports.InboxRecord, bool, error) {
	tx, err := requireTx(ctx, "InboxRepository.ReserveOrGet")
	if err != nil {
		return ports.InboxRecord{}, false, err
	}

	const insertQ = `
		INSERT INTO inbox_messages (consumer_name, message_id, message_hash, received_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (consumer_name, message_id) DO NOTHING
		RETURNING consumer_name, message_id, message_hash, received_at, completed_at`

	rec, scanErr := scanInboxRecord(tx.QueryRow(ctx, insertQ, consumerName, messageID, messageHash, time.Now().UTC()))
	if scanErr == nil {
		return rec, true, nil
	}
	if !errors.Is(scanErr, pgx.ErrNoRows) {
		return ports.InboxRecord{}, false, classifyError(scanErr)
	}

	const selectQ = `
		SELECT consumer_name, message_id, message_hash, received_at, completed_at
		FROM inbox_messages
		WHERE consumer_name = $1 AND message_id = $2`
	existing, err := scanInboxRecord(tx.QueryRow(ctx, selectQ, consumerName, messageID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ports.InboxRecord{}, false, classifyError(errors.New("postgres: inbox row vanished between insert conflict and select"))
		}
		return ports.InboxRecord{}, false, classifyError(err)
	}
	if existing.MessageHash != messageHash {
		return ports.InboxRecord{}, false, ports.ErrInboxHashMismatch
	}
	return existing, false, nil
}

func (r *InboxRepository) MarkCompleted(ctx context.Context, consumerName, messageID string, completedAt time.Time) error {
	tx, err := requireTx(ctx, "InboxRepository.MarkCompleted")
	if err != nil {
		return err
	}
	const q = `UPDATE inbox_messages SET completed_at = $1 WHERE consumer_name = $2 AND message_id = $3`
	tag, err := tx.Exec(ctx, q, completedAt, consumerName, messageID)
	if err != nil {
		return classifyError(err)
	}
	if tag.RowsAffected() == 0 {
		return ports.ErrNotFound
	}
	return nil
}

func scanInboxRecord(row pgx.Row) (ports.InboxRecord, error) {
	var rec ports.InboxRecord
	if err := row.Scan(&rec.ConsumerName, &rec.MessageID, &rec.MessageHash, &rec.ReceivedAt, &rec.CompletedAt); err != nil {
		return ports.InboxRecord{}, err
	}
	return rec, nil
}

package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"backend-challenge-go/internal/ports"
)

type OutboxRepository struct {
	pool *pgxpool.Pool
}

var _ ports.OutboxStore = (*OutboxRepository)(nil)

func NewOutboxRepository(pool *pgxpool.Pool) *OutboxRepository {
	return &OutboxRepository{pool: pool}
}

func (r *OutboxRepository) Enqueue(ctx context.Context, events ...ports.OutboxEvent) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := requireTx(ctx, "OutboxRepository.Enqueue")
	if err != nil {
		return err
	}

	const q = `
		INSERT INTO outbox_events (
			event_id, aggregate_id, event_type, correlation_id, causation_id, payload,
			occurred_at, attempts, next_attempt_at, published_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	batch := &pgx.Batch{}
	for _, e := range events {
		batch.Queue(q, e.EventID, e.AggregateID, e.EventType, e.CorrelationID, e.CausationID, e.Payload,
			e.OccurredAt, e.Attempts, e.NextAttemptAt, e.PublishedAt)
	}

	br := tx.SendBatch(ctx, batch)
	defer br.Close()
	for range events {
		if _, err := br.Exec(); err != nil {
			return classifyError(err)
		}
	}
	return nil
}

func (r *OutboxRepository) LockPendingBatch(ctx context.Context, limit int) ([]ports.OutboxEvent, error) {
	tx, err := requireTx(ctx, "OutboxRepository.LockPendingBatch")
	if err != nil {
		return nil, err
	}
	const q = `
		SELECT event_id, aggregate_id, event_type, correlation_id, causation_id, payload,
			occurred_at, attempts, next_attempt_at, published_at
		FROM outbox_events
		WHERE published_at IS NULL AND next_attempt_at <= now()
		ORDER BY occurred_at
		FOR UPDATE SKIP LOCKED
		LIMIT $1`
	rows, err := tx.Query(ctx, q, limit)
	if err != nil {
		return nil, classifyError(err)
	}
	defer rows.Close()

	var batch []ports.OutboxEvent
	for rows.Next() {
		var e ports.OutboxEvent
		if err := rows.Scan(&e.EventID, &e.AggregateID, &e.EventType, &e.CorrelationID, &e.CausationID, &e.Payload,
			&e.OccurredAt, &e.Attempts, &e.NextAttemptAt, &e.PublishedAt); err != nil {
			return nil, classifyError(err)
		}
		batch = append(batch, e)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyError(err)
	}
	return batch, nil
}

func (r *OutboxRepository) MarkPublished(ctx context.Context, eventID uuid.UUID, publishedAt time.Time) error {
	tx, err := requireTx(ctx, "OutboxRepository.MarkPublished")
	if err != nil {
		return err
	}
	const q = `UPDATE outbox_events SET published_at = $1 WHERE event_id = $2 AND published_at IS NULL`
	if _, err := tx.Exec(ctx, q, publishedAt, eventID); err != nil {
		return classifyError(err)
	}
	return nil
}

func (r *OutboxRepository) RecordFailedAttempt(ctx context.Context, eventID uuid.UUID, nextAttemptAt time.Time) error {
	tx, err := requireTx(ctx, "OutboxRepository.RecordFailedAttempt")
	if err != nil {
		return err
	}
	const q = `UPDATE outbox_events SET attempts = attempts + 1, next_attempt_at = $1 WHERE event_id = $2`
	if _, err := tx.Exec(ctx, q, nextAttemptAt, eventID); err != nil {
		return classifyError(err)
	}
	return nil
}

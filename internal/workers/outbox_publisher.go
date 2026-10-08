package workers

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/platform/metrics"
	"backend-challenge-go/internal/ports"
)

type OutboxPublisher struct {
	uow       ports.UnitOfWork
	store     ports.OutboxStore
	publisher ports.EventPublisher
	cfg       config.OutboxPublisherConfig
	logger    *slog.Logger
	metrics   *metrics.Metrics
	now       func() time.Time

	stopCh  chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func NewOutboxPublisher(
	uow ports.UnitOfWork,
	store ports.OutboxStore,
	publisher ports.EventPublisher,
	cfg config.OutboxPublisherConfig,
	logger *slog.Logger,
	m *metrics.Metrics,
) *OutboxPublisher {
	return &OutboxPublisher{
		uow:       uow,
		store:     store,
		publisher: publisher,
		cfg:       cfg,
		logger:    logger,
		metrics:   m,
		now:       func() time.Time { return time.Now().UTC() },
		stopCh:    make(chan struct{}),
		stopped:   make(chan struct{}),
	}
}

func (p *OutboxPublisher) Run() {
	defer close(p.stopped)

	interval := p.cfg.PollInterval
	for {
		select {
		case <-p.stopCh:
			return
		case <-time.After(interval):
		}

		ctx, cancel := context.WithTimeout(context.Background(), p.batchTimeout())
		published, err := p.runOnce(ctx)
		cancel()
		if err != nil {
			p.logger.Error("outbox_publisher: batch failed", slog.Any("error", err))
		}

		if err != nil || published == 0 {
			interval *= 2
			if interval > p.cfg.MaxPollInterval {
				interval = p.cfg.MaxPollInterval
			}
		} else {
			interval = p.cfg.PollInterval
		}
	}
}

func (p *OutboxPublisher) Stop(ctx context.Context) error {
	p.once.Do(func() { close(p.stopCh) })
	select {
	case <-p.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *OutboxPublisher) batchTimeout() time.Duration {
	if d := p.cfg.PollInterval * 5; d > 30*time.Second {
		return d
	}
	return 30 * time.Second
}

func (p *OutboxPublisher) runOnce(ctx context.Context) (published int, err error) {
	err = p.uow.WithinTx(ctx, func(ctx context.Context) error {
		batch, err := p.store.LockPendingBatch(ctx, p.cfg.BatchSize)
		if err != nil {
			return err
		}
		for _, event := range batch {
			sent, perr := p.publishOne(ctx, event)
			if perr != nil {
				// An unexpected error recording the outcome (not a mere send failure, which
				// publishOne already handles by scheduling a retry) rolls back the whole batch's
				// bookkeeping. The event itself may already have reached SQS; that duplicate-send
				// tradeoff is the accepted, documented behavior of the Outbox pattern
				// (ARCHITECTURE.md #6), absorbed downstream by SQS FIFO's own deduplication window.
				return perr
			}
			if sent {
				published++
			}
		}
		return nil
	})
	return published, err
}

func (p *OutboxPublisher) publishOne(ctx context.Context, event ports.OutboxEvent) (sent bool, err error) {
	sendErr := p.publisher.Publish(ctx, ports.OutboundMessage{
		MessageGroupID:         event.AggregateID.String(),
		MessageDeduplicationID: event.EventID.String(),
		Body:                   event.Payload,
	})
	if sendErr != nil {
		p.logger.Warn("outbox_publisher: publish failed, scheduling retry",
			slog.String("eventId", event.EventID.String()),
			slog.String("eventType", event.EventType),
			slog.Any("error", sendErr),
		)
		if p.metrics != nil {
			p.metrics.Retries.WithLabelValues(metrics.ComponentSQS, "outbox_publish").Inc()
		}
		next := computeNextAttempt(p.cfg.Backoff, event.Attempts, p.now())
		return false, p.store.RecordFailedAttempt(ctx, event.EventID, next)
	}

	now := p.now()
	if p.metrics != nil {
		p.metrics.ObserveOutboxLag(now.Sub(event.OccurredAt))
	}
	if err := p.store.MarkPublished(ctx, event.EventID, now); err != nil {
		return false, err
	}
	return true, nil
}

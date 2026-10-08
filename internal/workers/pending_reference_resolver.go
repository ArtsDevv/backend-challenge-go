package workers

import (
	"context"
	"log/slog"
	"math"
	"math/rand"
	"sync"
	"time"

	wageringapp "backend-challenge-go/internal/application/wagering"
	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/platform/metrics"
	"backend-challenge-go/internal/ports"
)

type PendingReferenceResolver struct {
	uow      ports.UnitOfWork
	txs      ports.TransactionRepository
	resolver *wageringapp.ResolvePendingReferenceUseCase
	cfg      config.PendingReferenceConfig
	logger   *slog.Logger
	metrics  *metrics.Metrics

	stopCh  chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func NewPendingReferenceResolver(
	uow ports.UnitOfWork,
	txs ports.TransactionRepository,
	resolver *wageringapp.ResolvePendingReferenceUseCase,
	cfg config.PendingReferenceConfig,
	logger *slog.Logger,
	m *metrics.Metrics,
) *PendingReferenceResolver {
	return &PendingReferenceResolver{
		uow:      uow,
		txs:      txs,
		resolver: resolver,
		cfg:      cfg,
		logger:   logger,
		metrics:  m,
		stopCh:   make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

func (r *PendingReferenceResolver) Run() {
	defer close(r.stopped)

	interval := r.cfg.PollInterval
	for {
		select {
		case <-r.stopCh:
			return
		case <-time.After(interval):
		}

		ctx, cancel := context.WithTimeout(context.Background(), r.batchTimeout())
		resolved, err := r.runOnce(ctx)
		cancel()
		if err != nil {
			r.logger.Error("pending_reference_resolver: batch failed", slog.Any("error", err))
		}

		if err != nil || resolved == 0 {
			interval *= 2
			if interval > r.cfg.MaxPollInterval {
				interval = r.cfg.MaxPollInterval
			}
		} else {
			interval = r.cfg.PollInterval
		}
	}
}

func (r *PendingReferenceResolver) Stop(ctx context.Context) error {
	r.once.Do(func() { close(r.stopCh) })
	select {
	case <-r.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *PendingReferenceResolver) batchTimeout() time.Duration {
	if d := r.cfg.PollInterval * 5; d > 30*time.Second {
		return d
	}
	return 30 * time.Second
}

func (r *PendingReferenceResolver) runOnce(ctx context.Context) (resolved int, err error) {
	err = r.uow.WithinTx(ctx, func(ctx context.Context) error {
		batch, err := r.txs.LockPendingReferenceBatch(ctx, r.cfg.BatchSize)
		if err != nil {
			return err
		}
		for _, tx := range batch {
			if rerr := r.resolver.Resolve(ctx, tx); rerr != nil {
				return rerr
			}
			resolved++
			if r.metrics != nil {
				r.metrics.TransactionsByStatus.WithLabelValues(string(tx.Status()), string(tx.Kind())).Inc()
			}
		}
		return nil
	})
	return resolved, err
}

func computeNextAttempt(backoff config.BackoffConfig, attempt int, now time.Time) time.Time {
	interval := float64(backoff.BaseInterval) * math.Pow(backoff.Factor, float64(attempt))
	if capped := float64(backoff.MaxInterval); interval > capped {
		interval = capped
	}
	jitterRange := interval * backoff.JitterFraction
	jitter := (rand.Float64()*2 - 1) * jitterRange
	delay := time.Duration(interval + jitter)
	if delay < 0 {
		delay = 0
	}
	return now.Add(delay)
}

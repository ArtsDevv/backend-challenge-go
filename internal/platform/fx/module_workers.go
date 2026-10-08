package fxplatform

import (
	"context"
	"log/slog"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"

	sqsadapter "backend-challenge-go/internal/adapters/sqs"
	wageringapp "backend-challenge-go/internal/application/wagering"
	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/platform/metrics"
	"backend-challenge-go/internal/ports"
	"backend-challenge-go/internal/workers"
)

var WorkersModule = fx.Module("workers",
	fx.Invoke(
		registerSQSConsumer,
		registerOutboxPublisher,
		registerPendingReferenceResolver,
		registerDLQWatcher,
	),
)

func registerSQSConsumer(
	lc fx.Lifecycle,
	client *awssqs.Client,
	cfg config.AWSConfig,
	uow ports.UnitOfWork,
	inbox ports.InboxStore,
	processTransaction *wageringapp.ProcessTransactionUseCase,
	logger *slog.Logger,
	m *metrics.Metrics,
) {
	consumer := sqsadapter.NewConsumer(client, cfg, uow, inbox, processTransaction, logger, m)

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			logger.Info("sqs: consumer starting", slog.String("queue", cfg.WagerTransactionsQueueURL))
			go consumer.Run()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("sqs: consumer stopping")
			return consumer.Stop(ctx)
		},
	})
}

func registerOutboxPublisher(
	lc fx.Lifecycle,
	uow ports.UnitOfWork,
	store ports.OutboxStore,
	publisher ports.EventPublisher,
	cfg config.OutboxPublisherConfig,
	logger *slog.Logger,
	m *metrics.Metrics,
) {
	p := workers.NewOutboxPublisher(uow, store, publisher, cfg, logger, m)

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			logger.Info("outbox_publisher: starting")
			go p.Run()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("outbox_publisher: stopping")
			return p.Stop(ctx)
		},
	})
}

func registerDLQWatcher(
	lc fx.Lifecycle,
	client *awssqs.Client,
	cfg config.AWSConfig,
	logger *slog.Logger,
	m *metrics.Metrics,
) {
	var queues []sqsadapter.DLQQueue
	if cfg.WagerTransactionsDLQURL != "" {
		queues = append(queues, sqsadapter.DLQQueue{URL: cfg.WagerTransactionsDLQURL, Label: metrics.QueueWagerTransactionsDLQ})
	}
	if cfg.WagerEventsDLQURL != "" {
		queues = append(queues, sqsadapter.DLQQueue{URL: cfg.WagerEventsDLQURL, Label: metrics.QueueWagerEventsDLQ})
	}
	watcher := sqsadapter.NewDLQWatcher(client, queues, logger, m)

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			logger.Info("dlq_watcher: starting")
			go watcher.Run()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("dlq_watcher: stopping")
			return watcher.Stop(ctx)
		},
	})
}

func registerPendingReferenceResolver(
	lc fx.Lifecycle,
	uow ports.UnitOfWork,
	txs ports.TransactionRepository,
	resolver *wageringapp.ResolvePendingReferenceUseCase,
	cfg config.PendingReferenceConfig,
	logger *slog.Logger,
	m *metrics.Metrics,
) {
	r := workers.NewPendingReferenceResolver(uow, txs, resolver, cfg, logger, m)

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			logger.Info("pending_reference_resolver: starting")
			go r.Run()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("pending_reference_resolver: stopping")
			return r.Stop(ctx)
		},
	})
}

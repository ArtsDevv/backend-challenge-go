package fxplatform

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"backend-challenge-go/internal/adapters/postgres"
	"backend-challenge-go/internal/platform/metrics"
	"backend-challenge-go/internal/ports"
)

var RepositoriesModule = fx.Module("repositories",
	fx.Provide(
		provideUnitOfWork,
		provideWalletRepository,
		provideTransactionRepository,
		provideLedgerRepository,
		provideInboxStore,
		provideOutboxStore,
	),
)

func provideUnitOfWork(pool *pgxpool.Pool, m *metrics.Metrics) ports.UnitOfWork {
	return postgres.NewTxManager(pool, postgres.WithMetrics(m))
}

func provideWalletRepository(pool *pgxpool.Pool) ports.WalletRepository {
	return postgres.NewWalletRepository(pool)
}

func provideTransactionRepository(pool *pgxpool.Pool) ports.TransactionRepository {
	return postgres.NewTransactionRepository(pool)
}

func provideLedgerRepository(pool *pgxpool.Pool) ports.LedgerRepository {
	return postgres.NewLedgerRepository(pool)
}

func provideInboxStore(pool *pgxpool.Pool) ports.InboxStore {
	return postgres.NewInboxRepository(pool)
}

func provideOutboxStore(pool *pgxpool.Pool) ports.OutboxStore {
	return postgres.NewOutboxRepository(pool)
}

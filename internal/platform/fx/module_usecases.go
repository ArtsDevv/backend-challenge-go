package fxplatform

import (
	"go.uber.org/fx"

	wageringapp "backend-challenge-go/internal/application/wagering"
	"backend-challenge-go/internal/application/wallets"
	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/ports"
)

var UseCasesModule = fx.Module("usecases",
	fx.Provide(
		wallets.NewOpenWalletUseCase,
		provideProcessTransactionUseCase,
		provideResolvePendingReferenceUseCase,
		wageringapp.NewReconciliationUseCase,
	),
)

func provideProcessTransactionUseCase(
	uow ports.UnitOfWork,
	walletRepo ports.WalletRepository,
	txRepo ports.TransactionRepository,
	ledgerRepo ports.LedgerRepository,
	outboxStore ports.OutboxStore,
	pendingRefCfg config.PendingReferenceConfig,
) *wageringapp.ProcessTransactionUseCase {
	return wageringapp.NewProcessTransactionUseCase(uow, walletRepo, txRepo, ledgerRepo, outboxStore, pendingRefCfg.Backoff)
}

func provideResolvePendingReferenceUseCase(
	walletRepo ports.WalletRepository,
	txRepo ports.TransactionRepository,
	ledgerRepo ports.LedgerRepository,
	outboxStore ports.OutboxStore,
	pendingRefCfg config.PendingReferenceConfig,
) *wageringapp.ResolvePendingReferenceUseCase {
	return wageringapp.NewResolvePendingReferenceUseCase(walletRepo, txRepo, ledgerRepo, outboxStore, pendingRefCfg)
}

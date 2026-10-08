package wallets

import (
	"context"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/events"
	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagering"
	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/ports"
)

type OpenWalletCommand struct {
	PlayerID       string
	InitialBalance money.Money
	CorrelationID  uuid.UUID
}

type OpenWalletResult struct {
	WalletID uuid.UUID
	PlayerID string
	Balance  money.Money
	Version  int64
}

type OpenWalletUseCase struct {
	UoW          ports.UnitOfWork
	Wallets      ports.WalletRepository
	Transactions ports.TransactionRepository
	Ledger       ports.LedgerRepository
	Outbox       ports.OutboxStore
	Now          func() time.Time
}

func NewOpenWalletUseCase(
	uow ports.UnitOfWork,
	wallets ports.WalletRepository,
	transactions ports.TransactionRepository,
	ledger ports.LedgerRepository,
	outbox ports.OutboxStore,
) *OpenWalletUseCase {
	return &OpenWalletUseCase{
		UoW:          uow,
		Wallets:      wallets,
		Transactions: transactions,
		Ledger:       ledger,
		Outbox:       outbox,
		Now:          func() time.Time { return time.Now().UTC() },
	}
}

func (uc *OpenWalletUseCase) Execute(ctx context.Context, cmd OpenWalletCommand) (OpenWalletResult, error) {
	correlationID := cmd.CorrelationID
	if correlationID == uuid.Nil {
		correlationID = uuid.New()
	}

	var result OpenWalletResult
	err := uc.UoW.WithinTx(ctx, func(ctx context.Context) error {
		now := uc.Now()
		walletID := uuid.New()

		w, err := wallet.NewWallet(walletID, cmd.PlayerID, cmd.InitialBalance, now)
		if err != nil {
			return err
		}

		if err := uc.Wallets.Insert(ctx, w); err != nil {
			return err
		}

		if cmd.InitialBalance.IsPositive() {
			if err := uc.openWithInitialCredit(ctx, w, cmd, correlationID, now); err != nil {
				return err
			}
		}

		result = OpenWalletResult{
			WalletID: w.ID(),
			PlayerID: w.PlayerID(),
			Balance:  w.Balance(),
			Version:  w.Version(),
		}
		return nil
	})
	if err != nil {
		return OpenWalletResult{}, err
	}
	return result, nil
}

func (uc *OpenWalletUseCase) openWithInitialCredit(ctx context.Context, w *wallet.Wallet, cmd OpenWalletCommand, correlationID uuid.UUID, now time.Time) error {
	openingTxID := uuid.New()

	openingTx, err := wagering.NewOpeningTransaction(wagering.NewOpeningTransactionParams{
		InternalID: openingTxID,
		WalletID:   w.ID(),
		PlayerID:   cmd.PlayerID,
		Money:      w.Balance(),
		Now:        now,
	})
	if err != nil {
		return err
	}
	if err := openingTx.MarkProcessed(w.Balance().MinorUnits(), w.Version(), now); err != nil {
		return err
	}
	if err := uc.Transactions.Insert(ctx, openingTx); err != nil {
		return err
	}

	zero, err := money.Zero(w.Currency())
	if err != nil {
		return err
	}
	entry, err := wallet.NewWalletLedgerEntry(uuid.New(), w.ID(), openingTxID, wallet.DirectionCredit, w.Balance(), zero, w.Balance(), now)
	if err != nil {
		return err
	}
	if err := uc.Ledger.Insert(ctx, entry); err != nil {
		return err
	}

	processedEvent := events.NewWagerTransactionProcessedEnvelope(correlationID, nil, now, events.WagerTransactionProcessedData{
		TransactionID:          openingTx.InternalID(),
		PlayerID:               cmd.PlayerID,
		WalletID:               w.ID(),
		Kind:                   wagering.KindOpening,
		Money:                  w.Balance(),
		ReferenceTransactionID: nil,
		ResultBalanceMinor:     w.Balance().MinorUnits(),
		ResultWalletVersion:    w.Version(),
		ProcessedAt:            now,
	})
	balanceEvent := events.NewWalletBalanceChangedEnvelope(correlationID, &processedEvent.EventID, now, events.WalletBalanceChangedData{
		WalletID:      w.ID(),
		TransactionID: openingTx.InternalID(),
		Direction:     wallet.DirectionCredit,
		Money:         w.Balance(),
		BalanceBefore: 0,
		BalanceAfter:  w.Balance().MinorUnits(),
		WalletVersion: w.Version(),
		ChangedAt:     now,
	})

	outboxEvents := make([]ports.OutboxEvent, 0, 2)
	for _, env := range []events.Envelope{processedEvent, balanceEvent} {
		oe, err := ports.NewOutboxEventFromEnvelope(env)
		if err != nil {
			return err
		}
		outboxEvents = append(outboxEvents, oe)
	}
	return uc.Outbox.Enqueue(ctx, outboxEvents...)
}

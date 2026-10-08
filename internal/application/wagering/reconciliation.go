package wagering

import (
	"context"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/platform/metrics"
	"backend-challenge-go/internal/ports"
)

const reconciliationPageSize = 500

type ReconciliationResult struct {
	WalletID          uuid.UUID
	StoredBalance     money.Money
	CalculatedBalance money.Money
	Difference        money.Money
	Consistent        bool
	CheckedEntries    int
}

type ReconciliationUseCase struct {
	Wallets ports.WalletRepository
	Ledger  ports.LedgerRepository
	Metrics *metrics.Metrics
}

func NewReconciliationUseCase(wallets ports.WalletRepository, ledger ports.LedgerRepository, m *metrics.Metrics) *ReconciliationUseCase {
	return &ReconciliationUseCase{Wallets: wallets, Ledger: ledger, Metrics: m}
}

func (uc *ReconciliationUseCase) Execute(ctx context.Context, walletID uuid.UUID) (ReconciliationResult, error) {
	w, err := uc.Wallets.FindByID(ctx, walletID)
	if err != nil {
		return ReconciliationResult{}, err
	}

	calculated, err := money.Zero(w.Currency())
	if err != nil {
		return ReconciliationResult{}, err
	}

	checked := 0
	cursor := ""
	for {
		entries, next, err := uc.Ledger.ListByWallet(ctx, walletID, cursor, reconciliationPageSize)
		if err != nil {
			return ReconciliationResult{}, err
		}
		for _, entry := range entries {
			switch entry.Direction() {
			case wallet.DirectionCredit:
				calculated, err = calculated.Add(entry.Amount())
			case wallet.DirectionDebit:
				calculated, err = calculated.Sub(entry.Amount())
			}
			if err != nil {
				return ReconciliationResult{}, err
			}
			checked++
		}
		if len(entries) == 0 || next == "" {
			break
		}
		cursor = next
	}

	difference, err := w.Balance().Sub(calculated)
	if err != nil {
		return ReconciliationResult{}, err
	}
	consistent := difference.IsZero()

	if uc.Metrics != nil {
		uc.Metrics.ObserveReconciliationDifference(w.Currency(), difference.MinorUnits(), consistent)
	}

	return ReconciliationResult{
		WalletID:          walletID,
		StoredBalance:     w.Balance(),
		CalculatedBalance: calculated,
		Difference:        difference,
		Consistent:        consistent,
		CheckedEntries:    checked,
	}, nil
}

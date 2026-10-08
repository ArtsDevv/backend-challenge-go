package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	pgadapter "backend-challenge-go/internal/adapters/postgres"
	wageringapp "backend-challenge-go/internal/application/wagering"
	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagering"
)

type workerResult struct {
	Status        string
	FailureCode   string
	Balance       string
	WalletVersion int64
	Err           string
}

func main() {
	os.Exit(run())
}

func run() int {
	databaseURL := flag.String("database-url", "", "")
	providerID := flag.String("provider-id", "", "")
	externalID := flag.String("external-id", "", "")
	idempotencyKey := flag.String("idempotency-key", "", "")
	playerID := flag.String("player-id", "", "")
	walletID := flag.String("wallet-id", "", "")
	roundID := flag.String("round-id", "round-mp", "")
	gameID := flag.String("game-id", "game-mp", "")
	kind := flag.String("kind", "BET", "")
	amount := flag.String("amount", "", "")
	currency := flag.String("currency", "BRL", "")
	flag.Parse()

	if *databaseURL == "" {
		fmt.Fprintln(os.Stderr, "concurrency_worker: -database-url is required")
		return 1
	}

	parsedWalletID, err := uuid.Parse(*walletID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "concurrency_worker: invalid -wallet-id: "+err.Error())
		return 1
	}

	parsedMoney, err := money.NewMoneyFromString(*amount, *currency)
	if err != nil {
		fmt.Fprintln(os.Stderr, "concurrency_worker: invalid -amount/-currency: "+err.Error())
		return 1
	}

	ctx := context.Background()

	pool, err := pgadapter.NewPool(ctx, config.PostgresConfig{
		DatabaseURL:     *databaseURL,
		MaxConns:        5,
		MinConns:        1,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 30 * time.Minute,
		ConnectTimeout:  5 * time.Second,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "concurrency_worker: build pgxpool: "+err.Error())
		return 1
	}
	defer pool.Close()

	uow := pgadapter.NewTxManager(pool)
	walletRepo := pgadapter.NewWalletRepository(pool)
	transactionRepo := pgadapter.NewTransactionRepository(pool)
	ledgerRepo := pgadapter.NewLedgerRepository(pool)
	outboxRepo := pgadapter.NewOutboxRepository(pool)

	processTransactionUC := wageringapp.NewProcessTransactionUseCase(uow, walletRepo, transactionRepo, ledgerRepo, outboxRepo, config.BackoffConfig{
		BaseInterval:   time.Second,
		Factor:         2,
		MaxInterval:    time.Minute,
		JitterFraction: 0.2,
	})

	result, execErr := processTransactionUC.Execute(ctx, wageringapp.ProcessTransactionCommand{
		ProviderID:            *providerID,
		ExternalTransactionID: *externalID,
		IdempotencyKey:        *idempotencyKey,
		PlayerID:              *playerID,
		WalletID:              parsedWalletID,
		RoundID:               *roundID,
		GameID:                *gameID,
		Kind:                  wagering.Kind(*kind),
		Money:                 parsedMoney,
	})

	out := workerResult{}
	if execErr != nil {
		out.Err = execErr.Error()
	} else {
		out.Status = string(result.Status)
		out.Balance = formatMinorUnits(result.Balance.MinorUnits())
		out.WalletVersion = result.WalletVersion
		if result.FailureCode != nil {
			out.FailureCode = string(*result.FailureCode)
		}
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "concurrency_worker: marshal result: "+err.Error())
		return 1
	}

	fmt.Println(string(encoded))
	return 0
}

func formatMinorUnits(minor int64) string {
	negative := minor < 0
	if negative {
		minor = -minor
	}
	sign := ""
	if negative {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%02d", sign, minor/100, minor%100)
}

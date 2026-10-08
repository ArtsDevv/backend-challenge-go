//go:build integration

package integration

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	pgadapter "backend-challenge-go/internal/adapters/postgres"
	wageringapp "backend-challenge-go/internal/application/wagering"
	"backend-challenge-go/internal/application/wallets"
	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagering"
	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/platform/logging"
	"backend-challenge-go/internal/platform/metrics"
	"backend-challenge-go/internal/ports"
)

var (
	pgContainer *tcpostgres.PostgresContainer
	pool        *pgxpool.Pool

	openWalletUC         *wallets.OpenWalletUseCase
	processTransactionUC *wageringapp.ProcessTransactionUseCase
	walletRepo           ports.WalletRepository
	ledgerRepo           ports.LedgerRepository
	transactionRepo      ports.TransactionRepository
	inboxStore           ports.InboxStore
	outboxStore          ports.OutboxStore
	uow                  ports.UnitOfWork

	skipReason string
)

func testLogger() *slog.Logger {
	return logging.NewDefault(slog.LevelWarn)
}

func testMetrics() *metrics.Metrics {
	return metrics.New(prometheus.NewRegistry())
}

func TestMain(m *testing.M) {
	code, err := setupAndRun(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "test/integration: shared setup unavailable, skipping suite:", err)
		skipReason = err.Error()
		code = m.Run()
	}
	os.Exit(code)
}

func setupAndRun(m *testing.M) (int, error) {
	setupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, connStr, err := startPostgres(setupCtx)
	if err != nil {
		return 0, fmt.Errorf("start postgres container: %w", err)
	}
	pgContainer = container
	defer func() { _ = pgContainer.Terminate(context.Background()) }()

	if err := applyMigrations(setupCtx, connStr); err != nil {
		return 0, fmt.Errorf("apply migrations: %w", err)
	}

	p, err := pgadapter.NewPool(setupCtx, config.PostgresConfig{
		DatabaseURL:     connStr,
		MaxConns:        10,
		MinConns:        1,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 30 * time.Minute,
		ConnectTimeout:  5 * time.Second,
	})
	if err != nil {
		return 0, fmt.Errorf("build pgxpool: %w", err)
	}
	pool = p
	defer pool.Close()

	if err := pool.Ping(setupCtx); err != nil {
		return 0, fmt.Errorf("ping postgres: %w", err)
	}

	wireUseCases(pool)

	return m.Run(), nil
}

func startPostgres(ctx context.Context) (*tcpostgres.PostgresContainer, string, error) {
	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("walletdb"),
		tcpostgres.WithUsername("wallet"),
		tcpostgres.WithPassword("wallet"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return nil, "", err
	}

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, "", err
	}
	return container, connStr, nil
}

func applyMigrations(ctx context.Context, databaseURL string) error {
	migrateBin, err := exec.LookPath("migrate")
	if err != nil {
		return fmt.Errorf("the 'migrate' CLI (golang-migrate) is not on PATH (see test/integration/README.md): %w", err)
	}

	migrationsDir, err := migrationsDirPath()
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, migrateBin, "-source", migrationsSourceURL(migrationsDir), "-database", databaseURL, "up")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("migrate up failed: %w\n%s", err, out)
	}
	return nil
}

func migrationsSourceURL(absPath string) string {
	return "file://" + filepath.ToSlash(absPath)
}

func migrationsDirPath() (string, error) {
	abs, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(abs); statErr != nil {
		return "", fmt.Errorf("migrations directory not found at %s: %w", abs, statErr)
	}
	return abs, nil
}

func wireUseCases(pool *pgxpool.Pool) {
	txManager := pgadapter.NewTxManager(pool)
	wRepo := pgadapter.NewWalletRepository(pool)
	txRepo := pgadapter.NewTransactionRepository(pool)
	lRepo := pgadapter.NewLedgerRepository(pool)
	iRepo := pgadapter.NewInboxRepository(pool)
	oRepo := pgadapter.NewOutboxRepository(pool)

	uow = txManager
	walletRepo = wRepo
	ledgerRepo = lRepo
	transactionRepo = txRepo
	inboxStore = iRepo
	outboxStore = oRepo

	openWalletUC = wallets.NewOpenWalletUseCase(txManager, wRepo, txRepo, lRepo, oRepo)
	processTransactionUC = wageringapp.NewProcessTransactionUseCase(txManager, wRepo, txRepo, lRepo, oRepo, config.BackoffConfig{
		BaseInterval:   time.Second,
		Factor:         2,
		MaxInterval:    time.Minute,
		JitterFraction: 0.2,
	})
}

func skipIfUnavailable(t *testing.T) {
	t.Helper()
	if skipReason != "" {
		t.Skip("test/integration: shared setup unavailable: " + skipReason)
	}
}

func mustMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	m, err := money.NewMoneyFromString(amount, currency)
	require.NoError(t, err)
	return m
}

func TestWalletConcurrency_TwoSimultaneousBetsOnlyOneSucceeds(t *testing.T) {
	skipIfUnavailable(t)
	ctx := context.Background()

	playerID := "player-concurrency-" + uuid.NewString()
	openResult, err := openWalletUC.Execute(ctx, wallets.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: mustMoney(t, "100.00", "BRL"),
	})
	require.NoError(t, err)
	walletID := openResult.WalletID
	require.Equal(t, int64(1), openResult.Version)

	providerID := "provider-concurrency-test"
	betAmount := mustMoney(t, "80.00", "BRL")

	cmdA := wageringapp.ProcessTransactionCommand{
		ProviderID:            providerID,
		ExternalTransactionID: "ext-A-" + uuid.NewString(),
		IdempotencyKey:        "idem-A-" + uuid.NewString(),
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  wagering.KindBet,
		Money:                 betAmount,
	}
	cmdB := cmdA
	cmdB.ExternalTransactionID = "ext-B-" + uuid.NewString()
	cmdB.IdempotencyKey = "idem-B-" + uuid.NewString()

	var (
		wg               sync.WaitGroup
		resultA, resultB wageringapp.ProcessTransactionResult
		errA, errB       error
	)
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		resultA, errA = processTransactionUC.Execute(ctx, cmdA)
	}()
	go func() {
		defer wg.Done()
		<-start
		resultB, errB = processTransactionUC.Execute(ctx, cmdB)
	}()
	close(start)
	wg.Wait()

	require.NoError(t, errA, "a business rejection must never surface as a Go error")
	require.NoError(t, errB, "a business rejection must never surface as a Go error")

	var processed, rejected int
	for _, r := range []wageringapp.ProcessTransactionResult{resultA, resultB} {
		switch r.Status {
		case wagering.StatusProcessed:
			processed++
			assert.Equal(t, int64(2000), r.Balance.MinorUnits(), "processed bet must leave exactly 20.00 BRL")
			assert.Equal(t, int64(2), r.WalletVersion)
		case wagering.StatusRejected:
			rejected++
			require.NotNil(t, r.FailureCode)
			assert.Equal(t, wagering.FailureCodeInsufficientBalance, *r.FailureCode)
		default:
			t.Fatalf("unexpected status %s for a BET that is neither processed nor rejected", r.Status)
		}
	}
	require.Equal(t, 1, processed, "exactly one of the two concurrent bets must be processed")
	require.Equal(t, 1, rejected, "exactly one of the two concurrent bets must be rejected for insufficient balance")

	finalWallet, err := walletRepo.FindByID(ctx, walletID)
	require.NoError(t, err)
	assert.Equal(t, int64(2000), finalWallet.Balance().MinorUnits(), "final balance must be exactly 20.00 BRL")
	assert.Equal(t, int64(2), finalWallet.Version(), "version must increment exactly once, never twice")

	entries, _, err := ledgerRepo.ListByWallet(ctx, walletID, "", 50)
	require.NoError(t, err)
	debitCount := 0
	for _, e := range entries {
		if e.Direction() == wallet.DirectionDebit {
			debitCount++
		}
	}
	assert.Equal(t, 1, debitCount, "exactly one debit ledger entry must exist, regardless of the two concurrent attempts")

	replayA, err := processTransactionUC.Execute(ctx, cmdA)
	require.NoError(t, err)
	assert.True(t, replayA.IdempotentReplay)
	assert.Equal(t, resultA.Status, replayA.Status)

	replayB, err := processTransactionUC.Execute(ctx, cmdB)
	require.NoError(t, err)
	assert.True(t, replayB.IdempotentReplay)
	assert.Equal(t, resultB.Status, replayB.Status)

	walletAfterReplay, err := walletRepo.FindByID(ctx, walletID)
	require.NoError(t, err)
	assert.Equal(t, int64(2000), walletAfterReplay.Balance().MinorUnits(), "resends must not change the balance")
	assert.Equal(t, int64(2), walletAfterReplay.Version(), "resends must not bump the version again")

	entriesAfterReplay, _, err := ledgerRepo.ListByWallet(ctx, walletID, "", 50)
	require.NoError(t, err)
	assert.Len(t, entriesAfterReplay, len(entries), "resends must not add any further ledger entries")
}

func TestWalletConcurrency_DifferentWalletsProceedInParallel(t *testing.T) {
	skipIfUnavailable(t)
	ctx := context.Background()

	const walletCount = 5
	results := make([]wageringapp.ProcessTransactionResult, walletCount)
	errs := make([]error, walletCount)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < walletCount; i++ {
		playerID := fmt.Sprintf("player-parallel-%d-%s", i, uuid.NewString())
		openResult, err := openWalletUC.Execute(ctx, wallets.OpenWalletCommand{
			PlayerID:       playerID,
			InitialBalance: mustMoney(t, "50.00", "BRL"),
		})
		require.NoError(t, err)

		cmd := wageringapp.ProcessTransactionCommand{
			ProviderID:            "provider-parallel-test",
			ExternalTransactionID: "ext-parallel-" + uuid.NewString(),
			IdempotencyKey:        "idem-parallel-" + uuid.NewString(),
			PlayerID:              playerID,
			WalletID:              openResult.WalletID,
			RoundID:               "round-1",
			GameID:                "game-1",
			Kind:                  wagering.KindBet,
			Money:                 mustMoney(t, "10.00", "BRL"),
		}

		idx := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[idx], errs[idx] = processTransactionUC.Execute(ctx, cmd)
		}()
	}
	close(start)
	wg.Wait()

	for i := 0; i < walletCount; i++ {
		require.NoError(t, errs[i])
		assert.Equal(t, wagering.StatusProcessed, results[i].Status, "wallet %d must process independently of contention on unrelated wallets", i)
		assert.Equal(t, int64(4000), results[i].Balance.MinorUnits())
		assert.Equal(t, int64(2), results[i].WalletVersion)
	}
}

func TestEndToEnd_HTTPAndSQSWithAuthAndMessaging(t *testing.T) {
	skipIfUnavailable(t)
	runEndToEndHTTPAndSQSScenario(t)
}

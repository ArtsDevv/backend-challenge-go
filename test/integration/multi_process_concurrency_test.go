//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"backend-challenge-go/internal/application/wallets"
	"backend-challenge-go/internal/domain/wallet"
)

type workerResult struct {
	Status        string
	FailureCode   string
	Balance       string
	WalletVersion int64
	Err           string
}

func TestMultiProcessConcurrency_ThreeIndependentProcesses(t *testing.T) {
	skipIfUnavailable(t)
	ctx := context.Background()

	binPath := filepath.Join(t.TempDir(), "concurrency_worker")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}

	var buildOutput bytes.Buffer
	buildCmd := exec.CommandContext(ctx, "go", "build", "-o", binPath, "./testdata/concurrency_worker")
	buildCmd.Stdout = &buildOutput
	buildCmd.Stderr = &buildOutput
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("build concurrency_worker: %v\n%s", err, buildOutput.String())
	}

	databaseURL, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	playerA := "player-mp-a-" + uuid.NewString()
	openA, err := openWalletUC.Execute(ctx, wallets.OpenWalletCommand{
		PlayerID:       playerA,
		InitialBalance: mustMoney(t, "100.00", "BRL"),
	})
	require.NoError(t, err)
	walletAID := openA.WalletID

	playerB := "player-mp-b-" + uuid.NewString()
	openB, err := openWalletUC.Execute(ctx, wallets.OpenWalletCommand{
		PlayerID:       playerB,
		InitialBalance: mustMoney(t, "50.00", "BRL"),
	})
	require.NoError(t, err)
	walletBID := openB.WalletID

	providerID := "provider-multiprocess-test"

	newWorkerCmd := func(playerID string, walletID uuid.UUID, amount string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, binPath,
			"-database-url", databaseURL,
			"-provider-id", providerID,
			"-external-id", "ext-mp-"+uuid.NewString(),
			"-idempotency-key", "idem-mp-"+uuid.NewString(),
			"-player-id", playerID,
			"-wallet-id", walletID.String(),
			"-amount", amount,
		)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		return cmd, &stdout, &stderr
	}

	cmdA1, stdoutA1, stderrA1 := newWorkerCmd(playerA, walletAID, "80.00")
	cmdA2, stdoutA2, stderrA2 := newWorkerCmd(playerA, walletAID, "80.00")
	cmdB, stdoutB, stderrB := newWorkerCmd(playerB, walletBID, "10.00")

	require.NoError(t, cmdA1.Start())
	require.NoError(t, cmdA2.Start())
	require.NoError(t, cmdB.Start())

	if err := cmdA1.Wait(); err != nil {
		t.Fatalf("worker process A1 failed: %v\nstderr: %s", err, stderrA1.String())
	}
	if err := cmdA2.Wait(); err != nil {
		t.Fatalf("worker process A2 failed: %v\nstderr: %s", err, stderrA2.String())
	}
	if err := cmdB.Wait(); err != nil {
		t.Fatalf("worker process B failed: %v\nstderr: %s", err, stderrB.String())
	}

	var resultA1, resultA2, resultB workerResult
	require.NoError(t, json.Unmarshal(stdoutA1.Bytes(), &resultA1))
	require.NoError(t, json.Unmarshal(stdoutA2.Bytes(), &resultA2))
	require.NoError(t, json.Unmarshal(stdoutB.Bytes(), &resultB))

	var processed, rejected int
	for _, r := range []workerResult{resultA1, resultA2} {
		switch r.Status {
		case "PROCESSED":
			processed++
			assert.Equal(t, "20.00", r.Balance)
			assert.Equal(t, int64(2), r.WalletVersion)
		case "REJECTED":
			rejected++
			assert.Equal(t, "INSUFFICIENT_BALANCE", r.FailureCode)
		default:
			t.Fatalf("unexpected status %q for a BET processed by an independent OS process", r.Status)
		}
	}
	require.Equal(t, 1, processed, "exactly one of the two independent-process bets on wallet A must be processed")
	require.Equal(t, 1, rejected, "exactly one of the two independent-process bets on wallet A must be rejected for insufficient balance")

	finalWalletA, err := walletRepo.FindByID(ctx, walletAID)
	require.NoError(t, err)
	assert.Equal(t, int64(2000), finalWalletA.Balance().MinorUnits())
	assert.Equal(t, int64(2), finalWalletA.Version())

	entriesA, _, err := ledgerRepo.ListByWallet(ctx, walletAID, "", 50)
	require.NoError(t, err)
	debitCount := 0
	for _, e := range entriesA {
		if e.Direction() == wallet.DirectionDebit {
			debitCount++
		}
	}
	assert.Equal(t, 1, debitCount)

	assert.Equal(t, "PROCESSED", resultB.Status)
	assert.Equal(t, "40.00", resultB.Balance)
	assert.Equal(t, int64(2), resultB.WalletVersion)
}

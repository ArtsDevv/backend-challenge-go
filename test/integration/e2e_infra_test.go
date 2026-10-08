//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/fx"
	"golang.org/x/oauth2/clientcredentials"

	httpadapter "backend-challenge-go/internal/adapters/http"
	sqsadapter "backend-challenge-go/internal/adapters/sqs"
	wageringapp "backend-challenge-go/internal/application/wagering"
	"backend-challenge-go/internal/application/wallets"
	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagering"
	fxplatform "backend-challenge-go/internal/platform/fx"
	"backend-challenge-go/internal/ports"
	"backend-challenge-go/internal/workers"
)

type alwaysUpPinger struct{}

func (alwaysUpPinger) Ping(context.Context) error { return nil }

type sqsWagerTransactionRequestedData struct {
	ProviderID            string      `json:"providerId"`
	ExternalTransactionID string      `json:"externalTransactionId"`
	IdempotencyKey        string      `json:"idempotencyKey"`
	PlayerID              string      `json:"playerId"`
	WalletID              string      `json:"walletId"`
	RoundID               string      `json:"roundId"`
	GameID                string      `json:"gameId"`
	Kind                  string      `json:"kind"`
	Money                 money.Money `json:"money"`
}

type sqsWagerTransactionRequestedEnvelope struct {
	MessageID  string                           `json:"messageId"`
	Type       string                           `json:"type"`
	OccurredAt time.Time                        `json:"occurredAt"`
	Data       sqsWagerTransactionRequestedData `json:"data"`
}

type outboundEventEnvelope struct {
	EventType string          `json:"EventType"`
	Data      json.RawMessage `json:"Data"`
}

type wagerTransactionProcessedEventData struct {
	ExternalTransactionID string `json:"ExternalTransactionID"`
}

type walletBalanceChangedEventData struct {
	TransactionID string `json:"TransactionID"`
}

func runEndToEndHTTPAndSQSScenario(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	issuerURL := startEphemeralKeycloak(ctx, t)
	providerAToken := clientCredentialsToken(ctx, t, issuerURL, "provider-a", "provider-a-local-dev-secret")
	providerBToken := clientCredentialsToken(ctx, t, issuerURL, "provider-b", "provider-b-local-dev-secret")

	validator, stopAuthApp := buildRealTokenValidator(ctx, t, issuerURL)
	defer stopAuthApp()

	localstackEndpoint := startEphemeralLocalStack(ctx, t)
	setEphemeralAWSTestCredentials(t)

	sqsClient, err := sqsadapter.NewClient(ctx, config.AWSConfig{Region: "us-east-1", Endpoint: localstackEndpoint})
	require.NoError(t, err)

	transactionsQueueURL := createEphemeralFIFOQueue(ctx, t, sqsClient, "wager-transactions.fifo")
	eventsQueueURL := createEphemeralFIFOQueue(ctx, t, sqsClient, "wager-events.fifo")

	server := httptest.NewServer(buildRealRouter(validator))
	defer server.Close()

	playerID := "player-e2e-" + uuid.NewString()
	openResult, err := openWalletUC.Execute(ctx, wallets.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: mustMoney(t, "100.00", "BRL"),
	})
	require.NoError(t, err)
	walletID := openResult.WalletID

	httpExternalTransactionID := "ext-http-" + uuid.NewString()
	httpIdempotencyKey := "idem-http-" + uuid.NewString()
	httpTransactionID := postBetOverHTTP(ctx, t, server.URL, providerAToken, httpIdempotencyKey, httpadapter.ProcessTransactionRequest{
		ProviderID:            "provider-a",
		ExternalTransactionID: httpExternalTransactionID,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-e2e-http",
		GameID:                "game-e2e-http",
		Kind:                  string(wagering.KindBet),
		Money:                 mustMoney(t, "10.00", "BRL"),
	})

	assertMissingAuthorizationIsRejected(t, server.URL)
	assertCrossProviderReadIsMaskedAsNotFound(t, server.URL, providerBToken, httpExternalTransactionID)

	consumer := sqsadapter.NewConsumer(sqsClient, config.AWSConfig{
		Region:                    "us-east-1",
		Endpoint:                  localstackEndpoint,
		WagerTransactionsQueueURL: transactionsQueueURL,
		ConsumerName:              "e2e-consumer-" + uuid.NewString(),
		MaxMessages:               10,
		WaitTimeSeconds:           2,
		VisibilityTimeout:         30,
	}, uow, inboxStore, processTransactionUC, testLogger(), testMetrics())
	go consumer.Run()
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, consumer.Stop(stopCtx))
	}()

	sqsExternalTransactionID := "ext-sqs-" + uuid.NewString()
	sqsIdempotencyKey := "idem-sqs-" + uuid.NewString()
	publishWagerTransactionRequested(ctx, t, sqsClient, transactionsQueueURL, sqsWagerTransactionRequestedData{
		ProviderID:            "provider-a",
		ExternalTransactionID: sqsExternalTransactionID,
		IdempotencyKey:        sqsIdempotencyKey,
		PlayerID:              playerID,
		WalletID:              walletID.String(),
		RoundID:               "round-e2e-sqs",
		GameID:                "game-e2e-sqs",
		Kind:                  string(wagering.KindBet),
		Money:                 mustMoney(t, "5.00", "BRL"),
	})

	sqsTx := waitForTransactionProcessed(ctx, t, "provider-a", sqsIdempotencyKey, 5*time.Second)
	sqsTransactionID := sqsTx.InternalID()

	walletAfterBothBets, err := walletRepo.FindByID(ctx, walletID)
	require.NoError(t, err)
	assert.Equal(t, int64(8500), walletAfterBothBets.Balance().MinorUnits(), "100.00 - 10.00 (HTTP bet) - 5.00 (SQS bet) must leave exactly 85.00 BRL")
	assert.Equal(t, int64(3), walletAfterBothBets.Version(), "opening the wallet plus two processed bets must bump the version exactly three times")

	outboundPublisher := sqsadapter.NewOutboundPublisher(sqsClient, eventsQueueURL)
	outboxCfg := config.OutboxPublisherConfig{
		Backoff: config.BackoffConfig{
			BaseInterval:   20 * time.Millisecond,
			Factor:         2.0,
			MaxInterval:    200 * time.Millisecond,
			JitterFraction: 0,
		},
		PollInterval:    20 * time.Millisecond,
		MaxPollInterval: 100 * time.Millisecond,
		BatchSize:       50,
	}
	outboxPublisher := workers.NewOutboxPublisher(uow, outboxStore, outboundPublisher, outboxCfg, testLogger(), testMetrics())
	go outboxPublisher.Run()
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, outboxPublisher.Stop(stopCtx))
	}()

	waitForOutboundEvents(ctx, t, sqsClient, eventsQueueURL, 5*time.Second,
		map[string]bool{httpExternalTransactionID: true, sqsExternalTransactionID: true},
		map[string]bool{httpTransactionID.String(): true, sqsTransactionID.String(): true},
	)
}

func startEphemeralKeycloak(ctx context.Context, t *testing.T) string {
	t.Helper()

	realmExportPath, err := filepath.Abs(filepath.Join("..", "..", "deployments", "keycloak", "realm-export.json"))
	require.NoError(t, err)

	req := testcontainers.ContainerRequest{
		Image: "quay.io/keycloak/keycloak:25.0",
		Env: map[string]string{
			"KEYCLOAK_ADMIN":          "admin",
			"KEYCLOAK_ADMIN_PASSWORD": "admin",
			"KC_HTTP_ENABLED":         "true",
			"KC_HOSTNAME_STRICT":      "false",
			"KC_HEALTH_ENABLED":       "true",
		},
		Cmd:          []string{"start-dev", "--import-realm"},
		ExposedPorts: []string{"8080/tcp"},
		Files: []testcontainers.ContainerFile{
			{
				HostFilePath:      realmExportPath,
				ContainerFilePath: "/opt/keycloak/data/import/realm-export.json",
				FileMode:          0o644,
			},
		},
		WaitingFor: wait.ForHTTP("/realms/backend-challenge/.well-known/openid-configuration").
			WithPort(nat.Port("8080/tcp")).
			WithStartupTimeout(90 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("test/integration: ephemeral Keycloak container unavailable, skipping real-IdP end-to-end test: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	mappedPort, err := container.MappedPort(ctx, "8080/tcp")
	require.NoError(t, err)

	return fmt.Sprintf("http://%s:%s/realms/backend-challenge", host, mappedPort.Port())
}

func startEphemeralLocalStack(ctx context.Context, t *testing.T) string {
	t.Helper()

	req := testcontainers.ContainerRequest{
		Image: "localstack/localstack:3.7",
		Env: map[string]string{
			"SERVICES":           "sqs",
			"DEFAULT_REGION":     "us-east-1",
			"AWS_DEFAULT_REGION": "us-east-1",
		},
		ExposedPorts: []string{"4566/tcp"},
		WaitingFor: wait.ForHTTP("/_localstack/health").
			WithPort(nat.Port("4566/tcp")).
			WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("test/integration: ephemeral LocalStack container unavailable, skipping real-SQS end-to-end test: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	mappedPort, err := container.MappedPort(ctx, "4566/tcp")
	require.NoError(t, err)

	return fmt.Sprintf("http://%s:%s", host, mappedPort.Port())
}

func setEphemeralAWSTestCredentials(t *testing.T) {
	t.Helper()

	for _, key := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
		previousValue, wasSet := os.LookupEnv(key)
		t.Cleanup(func() {
			if wasSet {
				_ = os.Setenv(key, previousValue)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
	_ = os.Setenv("AWS_ACCESS_KEY_ID", "test")
	_ = os.Setenv("AWS_SECRET_ACCESS_KEY", "test")
}

func clientCredentialsToken(ctx context.Context, t *testing.T, issuerURL, clientID, clientSecret string) string {
	t.Helper()

	cfg := clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     issuerURL + "/protocol/openid-connect/token",
	}
	token, err := cfg.Token(ctx)
	require.NoError(t, err)
	return token.AccessToken
}

func buildRealTokenValidator(ctx context.Context, t *testing.T, issuerURL string) (ports.TokenValidator, func()) {
	t.Helper()

	var validator ports.TokenValidator
	app := fx.New(
		fxplatform.AuthModule,
		fx.Supply(config.OIDCConfig{
			IssuerURL:    issuerURL,
			ClientID:     "wallet-service",
			ClientSecret: "wallet-service-local-dev-secret",
		}),
		fx.Provide(func() *slog.Logger { return testLogger() }),
		fx.Populate(&validator),
		fx.NopLogger,
	)

	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	require.NoError(t, app.Start(startCtx))

	return validator, func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Stop(stopCtx)
	}
}

func buildRealRouter(validator ports.TokenValidator) http.Handler {
	walletHandler := httpadapter.NewWalletHandler(openWalletUC, (*wageringapp.ReconciliationUseCase)(nil), walletRepo, ledgerRepo)
	wageringHandler := httpadapter.NewWageringHandler(processTransactionUC, transactionRepo, testMetrics())
	healthHandler := httpadapter.NewHealthHandler(alwaysUpPinger{}, alwaysUpPinger{})

	return httpadapter.NewRouter(httpadapter.RouterParams{
		Wallet:    walletHandler,
		Wagering:  wageringHandler,
		Health:    healthHandler,
		Validator: validator,
	})
}

func createEphemeralFIFOQueue(ctx context.Context, t *testing.T, client *sqs.Client, name string) string {
	t.Helper()

	out, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(name),
		Attributes: map[string]string{
			"FifoQueue":                 "true",
			"ContentBasedDeduplication": "true",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, out.QueueUrl)
	return *out.QueueUrl
}

func postBetOverHTTP(ctx context.Context, t *testing.T, baseURL, bearerToken, idempotencyKey string, reqBody httpadapter.ProcessTransactionRequest) uuid.UUID {
	t.Helper()

	payload, err := json.Marshal(reqBody)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/wagering/transactions", bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusCreated, resp.StatusCode, "a fresh BET against the real HTTP server with a real provider-a token must be accepted")

	var parsed httpadapter.ProcessTransactionResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&parsed))
	assert.Equal(t, "PROCESSED", parsed.Status)
	require.False(t, parsed.IdempotentReplay)

	return parsed.TransactionID
}

func assertMissingAuthorizationIsRejected(t *testing.T, baseURL string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, baseURL+"/wagering/transactions", bytes.NewReader([]byte("{}")))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "idem-no-auth-"+uuid.NewString())

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a request with no Authorization header must never reach the use case")
}

func assertCrossProviderReadIsMaskedAsNotFound(t *testing.T, baseURL, otherProviderToken, externalTransactionID string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, baseURL+"/providers/provider-a/wagering/transactions/"+externalTransactionID, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+otherProviderToken)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "provider-b must never be able to read provider-a's transaction, masked as not-found")
}

func publishWagerTransactionRequested(ctx context.Context, t *testing.T, client *sqs.Client, queueURL string, data sqsWagerTransactionRequestedData) {
	t.Helper()

	messageID := uuid.NewString()
	envelope := sqsWagerTransactionRequestedEnvelope{
		MessageID:  messageID,
		Type:       "WagerTransactionRequested",
		OccurredAt: time.Now().UTC(),
		Data:       data,
	}
	body, err := json.Marshal(envelope)
	require.NoError(t, err)

	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(queueURL),
		MessageBody:            aws.String(string(body)),
		MessageGroupId:         aws.String(data.WalletID),
		MessageDeduplicationId: aws.String(messageID),
	})
	require.NoError(t, err)
}

func waitForTransactionProcessed(ctx context.Context, t *testing.T, providerID, idempotencyKey string, timeout time.Duration) *wagering.WagerTransaction {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		tx, err := transactionRepo.FindByProviderAndIdempotencyKey(ctx, providerID, idempotencyKey)
		if err == nil && tx.Status() == wagering.StatusProcessed {
			return tx
		}
		if time.Now().After(deadline) {
			if err != nil {
				require.NoError(t, err, "transaction for provider %s / idempotencyKey %s never appeared via SQS", providerID, idempotencyKey)
			}
			t.Fatalf("transaction for provider %s / idempotencyKey %s never reached PROCESSED, last status %s", providerID, idempotencyKey, tx.Status())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitForOutboundEvents(
	ctx context.Context,
	t *testing.T,
	client *sqs.Client,
	queueURL string,
	timeout time.Duration,
	wantProcessedExternalTransactionIDs map[string]bool,
	wantBalanceChangedTransactionIDs map[string]bool,
) {
	t.Helper()

	foundProcessed := make(map[string]bool, len(wantProcessedExternalTransactionIDs))
	foundBalanceChanged := make(map[string]bool, len(wantBalanceChangedTransactionIDs))

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     1,
		})
		require.NoError(t, err)

		for _, msg := range out.Messages {
			var env outboundEventEnvelope
			if err := json.Unmarshal([]byte(aws.ToString(msg.Body)), &env); err == nil {
				switch env.EventType {
				case "WagerTransactionProcessed":
					var data wagerTransactionProcessedEventData
					if err := json.Unmarshal(env.Data, &data); err == nil && wantProcessedExternalTransactionIDs[data.ExternalTransactionID] {
						foundProcessed[data.ExternalTransactionID] = true
					}
				case "WalletBalanceChanged":
					var data walletBalanceChangedEventData
					if err := json.Unmarshal(env.Data, &data); err == nil && wantBalanceChangedTransactionIDs[data.TransactionID] {
						foundBalanceChanged[data.TransactionID] = true
					}
				}
			}
			_, _ = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(queueURL),
				ReceiptHandle: msg.ReceiptHandle,
			})
		}

		if len(foundProcessed) == len(wantProcessedExternalTransactionIDs) && len(foundBalanceChanged) == len(wantBalanceChangedTransactionIDs) {
			break
		}
	}

	for externalTransactionID := range wantProcessedExternalTransactionIDs {
		assert.Truef(t, foundProcessed[externalTransactionID], "expected a WagerTransactionProcessed event for externalTransactionId %s on wager-events.fifo", externalTransactionID)
	}
	for transactionID := range wantBalanceChangedTransactionIDs {
		assert.Truef(t, foundBalanceChanged[transactionID], "expected a WalletBalanceChanged event for transactionId %s on wager-events.fifo", transactionID)
	}
}

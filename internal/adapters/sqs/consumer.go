package sqs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	wageringapp "backend-challenge-go/internal/application/wagering"
	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagering"
	"backend-challenge-go/internal/platform/logging"
	"backend-challenge-go/internal/platform/metrics"
	"backend-challenge-go/internal/ports"
)

const eventTypeWagerTransactionRequested = "WagerTransactionRequested"

var errInboxInFlight = errors.New("sqs: message is already being handled by another in-flight transaction")

type sqsReceiver interface {
	ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, params *sqs.ChangeMessageVisibilityInput, optFns ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

var DefaultRetryBackoff = config.BackoffConfig{
	BaseInterval:   time.Second,
	Factor:         2.0,
	MaxInterval:    30 * time.Second,
	JitterFraction: 0.2,
}

type Consumer struct {
	sqsClient          sqsReceiver
	queueURL           string
	consumerName       string
	maxMessages        int32
	waitTimeSeconds    int32
	visibilityTimeout  int32
	retryBackoff       config.BackoffConfig
	uow                ports.UnitOfWork
	inbox              ports.InboxStore
	processTransaction *wageringapp.ProcessTransactionUseCase
	logger             *slog.Logger
	metrics            *metrics.Metrics
	now                func() time.Time

	serveCtx    context.Context
	cancelServe context.CancelFunc

	workCtx    context.Context
	cancelWork context.CancelFunc

	wg      sync.WaitGroup
	stopped chan struct{}
	once    sync.Once
}
type ConsumerOption func(*Consumer)

func WithRetryBackoff(b config.BackoffConfig) ConsumerOption {
	return func(c *Consumer) { c.retryBackoff = b }
}

func WithClock(now func() time.Time) ConsumerOption {
	return func(c *Consumer) { c.now = now }
}

func NewConsumer(
	client sqsReceiver,
	cfg config.AWSConfig,
	uow ports.UnitOfWork,
	inbox ports.InboxStore,
	processTransaction *wageringapp.ProcessTransactionUseCase,
	logger *slog.Logger,
	m *metrics.Metrics,
	opts ...ConsumerOption,
) *Consumer {
	serveCtx, cancelServe := context.WithCancel(context.Background())
	workCtx, cancelWork := context.WithCancel(context.Background())

	c := &Consumer{
		sqsClient:          client,
		queueURL:           cfg.WagerTransactionsQueueURL,
		consumerName:       cfg.ConsumerName,
		maxMessages:        cfg.MaxMessages,
		waitTimeSeconds:    cfg.WaitTimeSeconds,
		visibilityTimeout:  cfg.VisibilityTimeout,
		retryBackoff:       DefaultRetryBackoff,
		uow:                uow,
		inbox:              inbox,
		processTransaction: processTransaction,
		logger:             logger,
		metrics:            m,
		now:                func() time.Time { return time.Now().UTC() },
		serveCtx:           serveCtx,
		cancelServe:        cancelServe,
		workCtx:            workCtx,
		cancelWork:         cancelWork,
		stopped:            make(chan struct{}),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Consumer) Run() {
	defer close(c.stopped)

	backoff := time.Second
	for {
		if c.serveCtx.Err() != nil {
			c.wg.Wait()
			return
		}

		out, err := c.sqsClient.ReceiveMessage(c.serveCtx, &sqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(c.queueURL),
			MaxNumberOfMessages:         c.maxMessages,
			WaitTimeSeconds:             c.waitTimeSeconds,
			VisibilityTimeout:           c.visibilityTimeout,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
		})
		if err != nil {
			if c.serveCtx.Err() != nil {
				c.wg.Wait()
				return
			}
			c.logger.Error("sqs: receive message failed", slog.Any("error", err))
			select {
			case <-time.After(backoff):
			case <-c.serveCtx.Done():
				c.wg.Wait()
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second

		for _, msg := range out.Messages {
			if c.serveCtx.Err() != nil {
				c.releaseVisibility(msg)
				continue
			}
			c.wg.Add(1)
			go func(m types.Message) {
				defer c.wg.Done()
				c.handleMessage(c.workCtx, m)
			}(msg)
		}
	}
}

func (c *Consumer) Stop(ctx context.Context) error {
	c.once.Do(func() { c.cancelServe() })

	select {
	case <-c.stopped:
		return nil
	case <-ctx.Done():
		c.cancelWork()
		<-c.stopped
		return ctx.Err()
	}
}

func (c *Consumer) releaseVisibility(msg types.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.sqsClient.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(c.queueURL),
		ReceiptHandle:     msg.ReceiptHandle,
		VisibilityTimeout: 0,
	}); err != nil {
		c.logger.Warn("sqs: failed to release message visibility during shutdown", slog.Any("error", err))
	}
}

func (c *Consumer) handleMessage(ctx context.Context, msg types.Message) {
	sqsMessageID := ""
	if msg.MessageId != nil {
		sqsMessageID = *msg.MessageId
	}
	logger := c.logger.With(logging.MessageID(sqsMessageID))

	if msg.Body == nil || msg.ReceiptHandle == nil {
		logger.Error("sqs: received malformed message (missing body or receipt handle)")
		return
	}
	body := []byte(*msg.Body)

	env, err := parseInboundEnvelope(body)
	if err != nil {
		logger.Error("sqs: failed to parse message envelope", slog.Any("error", err))
		return
	}

	logger = logger.With(
		logging.CorrelationID(env.MessageID),
		logging.ProviderID(env.Data.ProviderID),
	)

	cmd, err := commandFromEnvelope(env)
	if err != nil {
		logger.Error("sqs: invalid message payload", slog.Any("error", err))
		return
	}
	logger = logger.With(logging.WalletID(cmd.WalletID.String()))
	ctx = logging.IntoContext(ctx, logger)

	hash := bodyHash(body)

	var (
		result wageringapp.ProcessTransactionResult
		skip   bool
	)

	start := c.now()
	txErr := c.uow.WithinTx(ctx, func(ctx context.Context) error {
		existing, reserved, err := c.inbox.ReserveOrGet(ctx, c.consumerName, env.MessageID, hash)
		if err != nil {
			return err
		}
		if !reserved {
			if existing.Completed() {
				skip = true
				if c.metrics != nil {
					c.metrics.DuplicateRequests.WithLabelValues(metrics.SourceSQS, metrics.ReasonInboxDuplicate).Inc()
				}
				return nil
			}
			return errInboxInFlight
		}

		res, err := c.processTransaction.Execute(ctx, cmd)
		if err != nil {
			return err
		}
		result = res
		if result.IdempotentReplay && c.metrics != nil {
			c.metrics.DuplicateRequests.WithLabelValues(metrics.SourceSQS, metrics.ReasonIdempotentReplay).Inc()
		}
		return c.inbox.MarkCompleted(ctx, c.consumerName, env.MessageID, c.now())
	})

	if c.metrics != nil {
		c.metrics.ObserveProcessingLatency("process_wager_transaction_sqs", c.now().Sub(start))
	}

	if txErr != nil {
		c.handleFailure(logger, msg, txErr)
		return
	}

	if skip {
		logger.Info("sqs: duplicate message acknowledged without reprocessing")
	} else {
		if c.metrics != nil {
			c.metrics.TransactionsByStatus.WithLabelValues(string(result.Status), string(cmd.Kind)).Inc()
		}
		logger.With(logging.TransactionID(result.TransactionID.String())).
			Info("sqs: message processed", slog.String("status", string(result.Status)))
	}
	c.deleteMessage(ctx, msg)
}

func (c *Consumer) handleFailure(logger *slog.Logger, msg types.Message, err error) {
	switch {
	case errors.Is(err, errInboxInFlight):
		logger.Warn("sqs: message already being handled by another in-flight transaction, retrying later")
	case isTransient(err):
		logger.Warn("sqs: transient failure processing message, scheduling retry", slog.Any("error", err))
		if c.metrics != nil {
			c.metrics.Retries.WithLabelValues(metrics.ComponentSQS, "process_transaction").Inc()
		}
		c.retryWithBackoff(msg)
	default:
		logger.Error("sqs: failed to process message", slog.Any("error", err))
	}
}

func isTransient(err error) bool {
	return errors.Is(err, ports.ErrWalletLockTimeout) ||
		errors.Is(err, ports.ErrSerializationFailure) ||
		errors.Is(err, ports.ErrRepositoryUnavailable)
}

func (c *Consumer) retryWithBackoff(msg types.Message) {
	attempt := approximateReceiveCount(msg)
	delay := backoffDuration(c.retryBackoff, attempt)
	visibilitySeconds := int32(delay / time.Second)
	if visibilitySeconds > c.visibilityTimeout {
		visibilitySeconds = c.visibilityTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.sqsClient.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(c.queueURL),
		ReceiptHandle:     msg.ReceiptHandle,
		VisibilityTimeout: visibilitySeconds,
	}); err != nil {
		c.logger.Warn("sqs: failed to shorten visibility timeout for retry", slog.Any("error", err))
	}
}

func (c *Consumer) deleteMessage(ctx context.Context, msg types.Message) {
	delCtx, cancel := context.WithTimeout(detach(ctx), 5*time.Second)
	defer cancel()
	if _, err := c.sqsClient.DeleteMessage(delCtx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.queueURL),
		ReceiptHandle: msg.ReceiptHandle,
	}); err != nil {
		c.logger.Error("sqs: failed to delete processed message", slog.Any("error", err))
	}
}

func detach(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}

func approximateReceiveCount(msg types.Message) int {
	raw, ok := msg.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)]
	if !ok {
		return 1
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

func backoffDuration(b config.BackoffConfig, attempt int) time.Duration {
	interval := float64(b.BaseInterval) * math.Pow(b.Factor, float64(attempt-1))
	if capped := float64(b.MaxInterval); interval > capped {
		interval = capped
	}
	jitterRange := interval * b.JitterFraction
	jitter := (rand.Float64()*2 - 1) * jitterRange
	d := time.Duration(interval + jitter)
	if d < 0 {
		d = 0
	}
	return d
}

func bodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

type inboundEnvelope struct {
	MessageID  string      `json:"messageId"`
	Type       string      `json:"type"`
	OccurredAt time.Time   `json:"occurredAt"`
	Data       inboundData `json:"data"`
}
type inboundData struct {
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	IdempotencyKey                 string      `json:"idempotencyKey"`
	PlayerID                       string      `json:"playerId"`
	WalletID                       string      `json:"walletId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Kind                           string      `json:"kind"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
}

func parseInboundEnvelope(body []byte) (inboundEnvelope, error) {
	var env inboundEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return inboundEnvelope{}, fmt.Errorf("sqs: invalid envelope JSON: %w", err)
	}
	if env.MessageID == "" {
		return inboundEnvelope{}, errors.New("sqs: envelope missing messageId")
	}
	if env.Type != eventTypeWagerTransactionRequested {
		return inboundEnvelope{}, fmt.Errorf("sqs: unsupported envelope type %q", env.Type)
	}
	return env, nil
}

func commandFromEnvelope(env inboundEnvelope) (wageringapp.ProcessTransactionCommand, error) {
	d := env.Data

	walletID, err := uuid.Parse(d.WalletID)
	if err != nil {
		return wageringapp.ProcessTransactionCommand{}, fmt.Errorf("sqs: invalid walletId %q: %w", d.WalletID, err)
	}

	kind := wagering.Kind(strings.ToUpper(strings.TrimSpace(d.Kind)))
	if kind == wagering.KindOpening {
		return wageringapp.ProcessTransactionCommand{}, errors.New("sqs: kind OPENING is not accepted from transport")
	}

	return wageringapp.ProcessTransactionCommand{
		ProviderID:                     d.ProviderID,
		ExternalTransactionID:          d.ExternalTransactionID,
		IdempotencyKey:                 d.IdempotencyKey,
		PlayerID:                       d.PlayerID,
		WalletID:                       walletID,
		RoundID:                        d.RoundID,
		GameID:                         d.GameID,
		Kind:                           kind,
		Money:                          d.Money,
		ReferenceExternalTransactionID: d.ReferenceExternalTransactionID,
	}, nil
}

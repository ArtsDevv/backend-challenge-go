package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/platform/metrics"
	"backend-challenge-go/internal/ports"
)

type fakeUoW struct {
	err error
}

func (f fakeUoW) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	if f.err != nil {
		return f.err
	}
	return fn(ctx)
}

type fakeInbox struct {
	reserved  bool
	completed bool
}

func (f fakeInbox) ReserveOrGet(_ context.Context, consumerName, messageID, hash string) (ports.InboxRecord, bool, error) {
	rec := ports.InboxRecord{ConsumerName: consumerName, MessageID: messageID, MessageHash: hash}
	if f.completed {
		now := time.Now().UTC()
		rec.CompletedAt = &now
	}
	return rec, f.reserved, nil
}

func (f fakeInbox) MarkCompleted(context.Context, string, string, time.Time) error { return nil }

type fakeSQSClient struct {
	deleteCalls           int
	changeVisibilityCalls int
	lastVisibilityTimeout int32
	deleteErr             error
	changeVisibilityErr   error
}

func (f *fakeSQSClient) ReceiveMessage(context.Context, *awssqs.ReceiveMessageInput, ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	return &awssqs.ReceiveMessageOutput{}, nil
}

func (f *fakeSQSClient) DeleteMessage(context.Context, *awssqs.DeleteMessageInput, ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	f.deleteCalls++
	return &awssqs.DeleteMessageOutput{}, f.deleteErr
}

func (f *fakeSQSClient) ChangeMessageVisibility(_ context.Context, params *awssqs.ChangeMessageVisibilityInput, _ ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error) {
	f.changeVisibilityCalls++
	f.lastVisibilityTimeout = params.VisibilityTimeout
	return &awssqs.ChangeMessageVisibilityOutput{}, f.changeVisibilityErr
}

func testConsumerLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func validMessageBody(t *testing.T) string {
	t.Helper()
	amount, err := money.NewMoneyFromString("10.00", "BRL")
	require.NoError(t, err)

	env := inboundEnvelope{
		MessageID:  uuid.NewString(),
		Type:       eventTypeWagerTransactionRequested,
		OccurredAt: time.Now().UTC(),
		Data: inboundData{
			ProviderID:            "provider-a",
			ExternalTransactionID: uuid.NewString(),
			IdempotencyKey:        uuid.NewString(),
			PlayerID:              "player-1",
			WalletID:              uuid.NewString(),
			RoundID:               "round-1",
			GameID:                "game-1",
			Kind:                  "BET",
			Money:                 amount,
		},
	}
	raw, err := json.Marshal(env)
	require.NoError(t, err)
	return string(raw)
}

func testMessage(t *testing.T, body string, receiveCount string) types.Message {
	t.Helper()
	return types.Message{
		MessageId:     aws.String(uuid.NewString()),
		ReceiptHandle: aws.String(uuid.NewString()),
		Body:          aws.String(body),
		Attributes: map[string]string{
			string(types.MessageSystemAttributeNameApproximateReceiveCount): receiveCount,
		},
	}
}

func newTestConsumer(client sqsReceiver, uow ports.UnitOfWork, inbox ports.InboxStore, m *metrics.Metrics) *Consumer {
	return NewConsumer(
		client,
		config.AWSConfig{
			WagerTransactionsQueueURL: "https://example.invalid/queue",
			ConsumerName:              "test-consumer",
			MaxMessages:               10,
			WaitTimeSeconds:           1,
			VisibilityTimeout:         30,
		},
		uow,
		inbox,
		nil,
		testConsumerLogger(),
		m,
	)
}

func TestConsumer_TransientFailureSchedulesRetryInsteadOfDeleting(t *testing.T) {
	client := &fakeSQSClient{}
	m := metrics.New(prometheus.NewRegistry())
	uow := fakeUoW{err: transientRepositoryError()}
	consumer := newTestConsumer(client, uow, fakeInbox{}, m)

	msg := testMessage(t, validMessageBody(t), "1")
	consumer.handleMessage(context.Background(), msg)

	assert.Equal(t, 1, client.changeVisibilityCalls, "a transient failure must shorten the message's visibility to retry soon")
	assert.Equal(t, 0, client.deleteCalls, "a transient failure must never delete the message")
	assert.Equal(t, float64(1), testutil.ToFloat64(m.Retries.WithLabelValues(metrics.ComponentSQS, "process_transaction")))
}

func transientRepositoryError() error {
	return errors.Join(ports.ErrRepositoryUnavailable, errors.New("postgres: connection refused"))
}

func TestConsumer_PermanentFailureLeavesMessageAloneForNaturalRedrive(t *testing.T) {
	client := &fakeSQSClient{}
	m := metrics.New(prometheus.NewRegistry())
	uow := fakeUoW{err: errors.New("boom: a genuine unexpected bug, not a classified sentinel")}
	consumer := newTestConsumer(client, uow, fakeInbox{}, m)

	msg := testMessage(t, validMessageBody(t), "1")
	consumer.handleMessage(context.Background(), msg)

	assert.Equal(t, 0, client.changeVisibilityCalls, "an unclassified permanent failure must not be retried by the consumer itself")
	assert.Equal(t, 0, client.deleteCalls, "an unclassified permanent failure must leave the message for SQS's own redrive policy to eventually move to the DLQ")
}

func TestConsumer_InFlightCollisionDoesNotDeleteOrChangeVisibility(t *testing.T) {
	client := &fakeSQSClient{}
	m := metrics.New(prometheus.NewRegistry())
	uow := fakeUoW{}
	consumer := newTestConsumer(client, uow, fakeInbox{reserved: false, completed: false}, m)

	msg := testMessage(t, validMessageBody(t), "1")
	consumer.handleMessage(context.Background(), msg)

	assert.Equal(t, 0, client.changeVisibilityCalls, "an in-flight collision relies on the natural visibility timeout, not an explicit shortening")
	assert.Equal(t, 0, client.deleteCalls, "an in-flight collision must never delete the message out from under the other in-flight attempt")
}

func TestConsumer_RedeliveryOfAnAlreadyCompletedMessageIsAcknowledgedAndDeleted(t *testing.T) {
	client := &fakeSQSClient{}
	m := metrics.New(prometheus.NewRegistry())
	uow := fakeUoW{}
	consumer := newTestConsumer(client, uow, fakeInbox{reserved: false, completed: true}, m)

	msg := testMessage(t, validMessageBody(t), "2")
	consumer.handleMessage(context.Background(), msg)

	assert.Equal(t, 1, client.deleteCalls,
		"this simulates SQS redelivering a message after the original commit succeeded but before the original DeleteMessage reached SQS "+
			"(e.g. the consumer process was interrupted in between): the inbox already shows it completed, so the message must be "+
			"acknowledged (deleted) again without ever reprocessing the business transaction")
	assert.Equal(t, 0, client.changeVisibilityCalls)
	assert.Equal(t, float64(1), testutil.ToFloat64(m.DuplicateRequests.WithLabelValues(metrics.SourceSQS, metrics.ReasonInboxDuplicate)))
}

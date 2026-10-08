//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	sqsadapter "backend-challenge-go/internal/adapters/sqs"
	"backend-challenge-go/internal/config"
)

func TestDLQWatcher_ObservesMessageAndIncrementsMetricWithoutDeleting(t *testing.T) {
	skipIfUnavailable(t)
	ctx := context.Background()

	endpoint := startEphemeralLocalStack(ctx, t)
	setEphemeralAWSTestCredentials(t)

	client, err := sqsadapter.NewClient(ctx, config.AWSConfig{Region: "us-east-1", Endpoint: endpoint})
	require.NoError(t, err)

	queueURL := createEphemeralFIFOQueue(ctx, t, client, "test-dlq.fifo")

	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(queueURL),
		MessageBody:            aws.String("{\"test\":true}"),
		MessageGroupId:         aws.String(uuid.NewString()),
		MessageDeduplicationId: aws.String(uuid.NewString()),
	})
	require.NoError(t, err)
	sentAt := time.Now()

	m := testMetrics()
	watcher := sqsadapter.NewDLQWatcher(
		client,
		[]sqsadapter.DLQQueue{{URL: queueURL, Label: "test-dlq.fifo"}},
		testLogger(),
		m,
		sqsadapter.WithPollInterval(50*time.Millisecond),
		sqsadapter.WithWaitTimeSeconds(1),
	)

	go watcher.Run()
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, watcher.Stop(stopCtx))
	}()

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(m.DLQMessages.WithLabelValues("test-dlq.fifo")) >= 1
	}, 5*time.Second, 50*time.Millisecond)

	if remaining := 6*time.Second - time.Since(sentAt); remaining > 0 {
		time.Sleep(remaining)
	}

	out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(queueURL),
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     1,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(out.Messages), 1, "the DLQ watcher must never delete the message it observed")
}

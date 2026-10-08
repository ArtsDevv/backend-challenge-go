package sqs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"backend-challenge-go/internal/platform/metrics"
)

const (
	defaultDLQPollInterval    = 30 * time.Second
	defaultDLQWaitTimeSeconds = int32(10)
	dlqVisibilityTimeout      = int32(5)
	dlqMaxMessages            = int32(10)
)

type DLQQueue struct {
	URL   string
	Label string
}

type DLQWatcherOption func(*DLQWatcher)

func WithPollInterval(d time.Duration) DLQWatcherOption {
	return func(w *DLQWatcher) { w.pollInterval = d }
}

func WithWaitTimeSeconds(s int32) DLQWatcherOption {
	return func(w *DLQWatcher) { w.waitTimeSeconds = s }
}

type DLQWatcher struct {
	client          sqsReceiver
	queues          []DLQQueue
	pollInterval    time.Duration
	waitTimeSeconds int32
	logger          *slog.Logger
	metrics         *metrics.Metrics
	stopCh          chan struct{}
	stopped         chan struct{}
	once            sync.Once
}

func NewDLQWatcher(client sqsReceiver, queues []DLQQueue, logger *slog.Logger, m *metrics.Metrics, opts ...DLQWatcherOption) *DLQWatcher {
	w := &DLQWatcher{
		client:          client,
		queues:          queues,
		pollInterval:    defaultDLQPollInterval,
		waitTimeSeconds: defaultDLQWaitTimeSeconds,
		logger:          logger,
		metrics:         m,
		stopCh:          make(chan struct{}),
		stopped:         make(chan struct{}),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

func (w *DLQWatcher) Run() {
	defer close(w.stopped)

	if len(w.queues) == 0 {
		return
	}

	for {
		select {
		case <-w.stopCh:
			return
		case <-time.After(w.pollInterval):
			for _, q := range w.queues {
				w.pollOnce(q)
			}
		}
	}
}

func (w *DLQWatcher) pollOnce(q DLQQueue) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(w.waitTimeSeconds+5)*time.Second)
	defer cancel()

	out, err := w.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(q.URL),
		MaxNumberOfMessages: dlqMaxMessages,
		WaitTimeSeconds:     w.waitTimeSeconds,
		VisibilityTimeout:   dlqVisibilityTimeout,
	})
	if err != nil {
		w.logger.Warn("dlq_watcher: receive message failed", slog.String("queue", q.Label), slog.Any("error", err))
		return
	}

	if len(out.Messages) == 0 {
		return
	}

	if w.metrics != nil {
		w.metrics.DLQMessages.WithLabelValues(q.Label).Add(float64(len(out.Messages)))
	}
	w.logger.Warn("dlq_watcher: messages observed in dead-letter queue", slog.String("queue", q.Label), slog.Int("count", len(out.Messages)))

	for _, msg := range out.Messages {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := w.client.ChangeMessageVisibility(releaseCtx, &sqs.ChangeMessageVisibilityInput{
			QueueUrl:          aws.String(q.URL),
			ReceiptHandle:     msg.ReceiptHandle,
			VisibilityTimeout: 0,
		})
		releaseCancel()
		if err != nil {
			w.logger.Warn("dlq_watcher: failed to release message visibility", slog.String("queue", q.Label), slog.Any("error", err))
		}
	}
}

func (w *DLQWatcher) Stop(ctx context.Context) error {
	w.once.Do(func() { close(w.stopCh) })
	select {
	case <-w.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

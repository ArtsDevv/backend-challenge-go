package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const namespace = "wagering"

const (
	StatusPending          = "PENDING"
	StatusPendingReference = "PENDING_REFERENCE"
	StatusProcessed        = "PROCESSED"
	StatusRejected         = "REJECTED"
	StatusFailed           = "FAILED"
)

const (
	SourceHTTP = "http"
	SourceSQS  = "sqs"
)

const (
	ReasonIdempotentReplay = "idempotent_replay"
	ReasonInboxDuplicate   = "inbox_duplicate"
)

const (
	ConflictLockTimeout          = "lock_timeout"
	ConflictSerializationFailure = "serialization_failure"
	ConflictDeadlockDetected     = "deadlock_detected"
	ConflictVersionMismatch      = "version_mismatch"
)

const (
	ComponentPostgres = "postgres"
	ComponentSQS      = "sqs"
)

const (
	QueueWagerTransactionsDLQ = "wager-transactions-dlq.fifo"
	QueueWagerEventsDLQ       = "wager-events-dlq.fifo"
)

type Metrics struct {
	TransactionsByStatus *prometheus.CounterVec

	DuplicateRequests *prometheus.CounterVec

	Retries *prometheus.CounterVec

	DLQMessages *prometheus.CounterVec

	ConcurrencyConflicts *prometheus.CounterVec

	OutboxLag prometheus.Histogram

	ProcessingLatency *prometheus.HistogramVec

	ReconciliationDifference *prometheus.HistogramVec

	ReconciliationChecks *prometheus.CounterVec // labels: consistent
}

func New(reg prometheus.Registerer) *Metrics {
	factory := promauto.With(reg)

	return &Metrics{
		TransactionsByStatus: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "wager_transactions_total",
			Help:      "Count of wager transactions, partitioned by status and kind.",
		}, []string{"status", "kind"}),

		DuplicateRequests: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "duplicate_requests_total",
			Help:      "Count of requests recognized as idempotent replays or inbox duplicates.",
		}, []string{"source", "reason"}),

		Retries: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "retries_total",
			Help:      "Count of internally retried transient failures (e.g. Postgres or SQS).",
		}, []string{"component", "reason"}),

		DLQMessages: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "dlq_messages_total",
			Help:      "Count of messages delivered to a dead-letter queue.",
		}, []string{"queue"}),

		ConcurrencyConflicts: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "concurrency_conflicts_total",
			Help:      "Count of wallet concurrency conflicts (lock timeouts, serialization failures, etc).",
		}, []string{"component", "reason"}),

		OutboxLag: factory.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "outbox_publish_lag_seconds",
			Help:      "Delay between an outbox event's occurred_at and its successful publish.",
			Buckets:   []float64{.1, .5, 1, 2, 5, 10, 30, 60, 120, 300},
		}),

		ProcessingLatency: factory.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "processing_latency_seconds",
			Help:      "Latency of application use cases, partitioned by operation.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"operation"}),

		ReconciliationDifference: factory.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "reconciliation_difference_minor",
			Help:      "Absolute difference (minor units) between a wallet's stored and recalculated balance.",
			Buckets:   []float64{0, 1, 10, 100, 1000, 10000, 100000},
		}, []string{"currency"}),

		ReconciliationChecks: factory.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "reconciliation_checks_total",
			Help:      "Count of reconciliation checks, partitioned by whether the wallet was found consistent.",
		}, []string{"consistent"}),
	}
}

func (m *Metrics) ObserveProcessingLatency(operation string, d time.Duration) {
	m.ProcessingLatency.WithLabelValues(operation).Observe(d.Seconds())
}

func (m *Metrics) ObserveOutboxLag(d time.Duration) {
	m.OutboxLag.Observe(d.Seconds())
}

func (m *Metrics) ObserveReconciliationDifference(currency string, differenceMinor int64, consistent bool) {
	if differenceMinor < 0 {
		differenceMinor = -differenceMinor
	}
	m.ReconciliationDifference.WithLabelValues(currency).Observe(float64(differenceMinor))
	m.ReconciliationChecks.WithLabelValues(strconv.FormatBool(consistent)).Inc()
}

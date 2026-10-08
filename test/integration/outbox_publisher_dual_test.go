//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/ports"
	"backend-challenge-go/internal/workers"
)

type recordingPublisher struct {
	mu     sync.Mutex
	counts map[string]int
	order  []string
}

func newRecordingPublisher() *recordingPublisher {
	return &recordingPublisher{counts: make(map[string]int)}
}

func (p *recordingPublisher) Publish(ctx context.Context, msg ports.OutboundMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.counts[msg.MessageDeduplicationID]++
	p.order = append(p.order, msg.MessageDeduplicationID)
	return nil
}

func (p *recordingPublisher) totalPublishes() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	total := 0
	for _, count := range p.counts {
		total += count
	}
	return total
}

func (p *recordingPublisher) duplicateDeduplicationIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var duplicates []string
	for id, count := range p.counts {
		if count > 1 {
			duplicates = append(duplicates, id)
		}
	}
	return duplicates
}

func (p *recordingPublisher) countsAmong(ids map[string]bool) map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make(map[string]int, len(ids))
	for id := range ids {
		result[id] = p.counts[id]
	}
	return result
}

func TestOutboxPublisher_TwoConcurrentPublishersNeverDuplicate(t *testing.T) {
	skipIfUnavailable(t)
	ctx := context.Background()

	const eventCount = 24
	outboxEvents := make([]ports.OutboxEvent, 0, eventCount)
	eventIDs := make([]uuid.UUID, 0, eventCount)
	expectedDeduplicationIDs := make(map[string]bool, eventCount)
	occurredAt := time.Now().UTC()
	for i := 0; i < eventCount; i++ {
		eventID := uuid.New()
		outboxEvents = append(outboxEvents, ports.OutboxEvent{
			EventID:       eventID,
			AggregateID:   uuid.New(),
			EventType:     "TestEvent",
			CorrelationID: uuid.New(),
			Payload:       []byte("{}"),
			OccurredAt:    occurredAt,
			NextAttemptAt: occurredAt,
		})
		eventIDs = append(eventIDs, eventID)
		expectedDeduplicationIDs[eventID.String()] = true
	}

	require.NoError(t, uow.WithinTx(ctx, func(ctx context.Context) error {
		return outboxStore.Enqueue(ctx, outboxEvents...)
	}))

	publisher := newRecordingPublisher()
	cfg := config.OutboxPublisherConfig{
		Backoff: config.BackoffConfig{
			BaseInterval:   20 * time.Millisecond,
			Factor:         1.0,
			MaxInterval:    20 * time.Millisecond,
			JitterFraction: 0,
		},
		PollInterval:    20 * time.Millisecond,
		MaxPollInterval: 100 * time.Millisecond,
		BatchSize:       5,
	}

	publisherOne := workers.NewOutboxPublisher(uow, outboxStore, publisher, cfg, testLogger(), testMetrics())
	publisherTwo := workers.NewOutboxPublisher(uow, outboxStore, publisher, cfg, testLogger(), testMetrics())

	go publisherOne.Run()
	go publisherTwo.Run()

	deadline := time.Now().Add(5 * time.Second)
	var publishedCount int
	for time.Now().Before(deadline) {
		row := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL AND event_id = ANY($1)`, eventIDs)
		require.NoError(t, row.Scan(&publishedCount))
		if publishedCount == eventCount {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, publisherOne.Stop(stopCtx))
	require.NoError(t, publisherTwo.Stop(stopCtx))

	if publishedCount != eventCount {
		t.Fatalf("expected all %d outbox events to be published within the deadline, got %d", eventCount, publishedCount)
	}

	duplicates := publisher.duplicateDeduplicationIDs()
	require.Empty(t, duplicates)

	ownCounts := publisher.countsAmong(expectedDeduplicationIDs)
	ownTotal := 0
	for id, count := range ownCounts {
		assert.Equalf(t, 1, count, "expected exactly one publish for event %s, got %d", id, count)
		ownTotal += count
	}
	require.Equal(t, eventCount, ownTotal)
}

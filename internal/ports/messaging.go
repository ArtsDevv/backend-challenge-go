package ports

import "context"

type OutboundMessage struct {
	MessageGroupID         string
	MessageDeduplicationID string
	Body                   []byte
}

type EventPublisher interface {
	Publish(ctx context.Context, msg OutboundMessage) error
}

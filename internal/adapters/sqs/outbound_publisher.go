package sqs

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"backend-challenge-go/internal/ports"
)

type sqsSender interface {
	SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}
type OutboundPublisher struct {
	client   sqsSender
	queueURL string
}

func NewOutboundPublisher(client sqsSender, queueURL string) *OutboundPublisher {
	return &OutboundPublisher{client: client, queueURL: queueURL}
}

var _ ports.EventPublisher = (*OutboundPublisher)(nil)

func (p *OutboundPublisher) Publish(ctx context.Context, msg ports.OutboundMessage) error {
	if msg.MessageGroupID == "" {
		return fmt.Errorf("sqs: outbound message missing MessageGroupID")
	}
	if msg.MessageDeduplicationID == "" {
		return fmt.Errorf("sqs: outbound message missing MessageDeduplicationID")
	}

	_, err := p.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(p.queueURL),
		MessageBody:            aws.String(string(msg.Body)),
		MessageGroupId:         aws.String(msg.MessageGroupID),
		MessageDeduplicationId: aws.String(msg.MessageDeduplicationID),
	})
	if err != nil {
		return fmt.Errorf("sqs: send message to %s: %w", p.queueURL, err)
	}
	return nil
}

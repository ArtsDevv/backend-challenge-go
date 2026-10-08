package fxplatform

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/fx"

	sqsadapter "backend-challenge-go/internal/adapters/sqs"
	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/ports"
)

var SQSModule = fx.Module("sqs",
	fx.Provide(
		newSQSClient,
		provideEventPublisher,
	),
)

func newSQSClient(lc fx.Lifecycle, cfg config.AWSConfig) (*awssqs.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := sqsadapter.NewClient(ctx, cfg)
	if err != nil {
		return nil, err
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			_, err := client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
				QueueUrl:       aws.String(cfg.WagerTransactionsQueueURL),
				AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
			})
			if err != nil {
				return fmt.Errorf("sqs: connectivity check against %s: %w", cfg.WagerTransactionsQueueURL, err)
			}
			return nil
		},
	})

	return client, nil
}

func provideEventPublisher(client *awssqs.Client, cfg config.AWSConfig) ports.EventPublisher {
	return sqsadapter.NewOutboundPublisher(client, cfg.WagerEventsQueueURL)
}

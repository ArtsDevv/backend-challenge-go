package fxplatform

import (
	"go.uber.org/fx"

	"backend-challenge-go/internal/config"
)

var ConfigModule = fx.Module("config",
	fx.Provide(
		provideHTTPConfig,
		providePostgresConfig,
		provideAWSConfig,
		provideOIDCConfig,
		providePendingReferenceConfig,
		provideOutboxPublisherConfig,
	),
)

func provideHTTPConfig(cfg *config.Config) config.HTTPConfig { return cfg.HTTP }

func providePostgresConfig(cfg *config.Config) config.PostgresConfig { return cfg.Postgres }

func provideAWSConfig(cfg *config.Config) config.AWSConfig { return cfg.AWS }

func provideOIDCConfig(cfg *config.Config) config.OIDCConfig { return cfg.OIDC }

func providePendingReferenceConfig(cfg *config.Config) config.PendingReferenceConfig {
	return cfg.Workers.PendingReference
}

func provideOutboxPublisherConfig(cfg *config.Config) config.OutboxPublisherConfig {
	return cfg.Workers.OutboxPublisher
}

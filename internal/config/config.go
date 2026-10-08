package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTP     HTTPConfig
	Postgres PostgresConfig
	AWS      AWSConfig
	OIDC     OIDCConfig
	Workers  WorkersConfig
}

type HTTPConfig struct {
	Port            int
	ShutdownTimeout time.Duration
}

type PostgresConfig struct {
	DatabaseURL     string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
}

type AWSConfig struct {
	Region   string
	Endpoint string

	WagerTransactionsQueueURL string
	WagerTransactionsDLQURL   string
	WagerEventsQueueURL       string
	WagerEventsDLQURL         string

	ConsumerName string

	MaxMessages       int32
	WaitTimeSeconds   int32
	VisibilityTimeout int32
}

type OIDCConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	Audience     string
}

type BackoffConfig struct {
	BaseInterval   time.Duration
	Factor         float64
	MaxInterval    time.Duration
	JitterFraction float64
}

type WorkersConfig struct {
	PendingReference PendingReferenceConfig
	OutboxPublisher  OutboxPublisherConfig
}

type PendingReferenceConfig struct {
	Backoff         BackoffConfig
	MaxAttempts     int
	TTL             time.Duration
	PollInterval    time.Duration
	MaxPollInterval time.Duration
	BatchSize       int
}

type OutboxPublisherConfig struct {
	Backoff         BackoffConfig
	PollInterval    time.Duration
	MaxPollInterval time.Duration
	BatchSize       int
}

func Load() (*Config, error) {
	var errs []error

	cfg := &Config{
		HTTP: HTTPConfig{
			Port:            envInt("HTTP_PORT", 8080, &errs),
			ShutdownTimeout: envDuration("SHUTDOWN_TIMEOUT", 25*time.Second, &errs),
		},
		Postgres: PostgresConfig{
			DatabaseURL:     requireString("DATABASE_URL", &errs),
			MaxConns:        envInt32("DB_MAX_CONNS", 10, &errs),
			MinConns:        envInt32("DB_MIN_CONNS", 2, &errs),
			MaxConnLifetime: envDuration("DB_MAX_CONN_LIFETIME", time.Hour, &errs),
			MaxConnIdleTime: envDuration("DB_MAX_CONN_IDLE_TIME", 30*time.Minute, &errs),
			ConnectTimeout:  envDuration("DB_CONNECT_TIMEOUT", 5*time.Second, &errs),
		},
		AWS: AWSConfig{
			Region:                    envString("AWS_REGION", "us-east-1"),
			Endpoint:                  envString("AWS_SQS_ENDPOINT", ""),
			WagerTransactionsQueueURL: requireString("SQS_WAGER_TRANSACTIONS_QUEUE_URL", &errs),
			WagerTransactionsDLQURL:   envString("SQS_WAGER_TRANSACTIONS_DLQ_URL", ""),
			WagerEventsQueueURL:       requireString("SQS_WAGER_EVENTS_QUEUE_URL", &errs),
			WagerEventsDLQURL:         envString("SQS_WAGER_EVENTS_DLQ_URL", ""),
			ConsumerName:              envString("SQS_CONSUMER_NAME", "wager-transactions-consumer"),
			MaxMessages:               envInt32("SQS_MAX_MESSAGES", 10, &errs),
			WaitTimeSeconds:           envInt32("SQS_WAIT_TIME_SECONDS", 20, &errs),
			VisibilityTimeout:         envInt32("SQS_VISIBILITY_TIMEOUT", 30, &errs),
		},
		OIDC: OIDCConfig{
			IssuerURL:    requireString("OIDC_ISSUER_URL", &errs),
			ClientID:     requireString("OIDC_CLIENT_ID", &errs),
			ClientSecret: requireString("OIDC_CLIENT_SECRET", &errs),
			Audience:     envString("OIDC_AUDIENCE", ""),
		},
		Workers: WorkersConfig{
			PendingReference: PendingReferenceConfig{
				Backoff: BackoffConfig{
					BaseInterval:   envDuration("PENDING_REFERENCE_BACKOFF_BASE", 2*time.Second, &errs),
					Factor:         envFloat("PENDING_REFERENCE_BACKOFF_FACTOR", 2.0, &errs),
					MaxInterval:    envDuration("PENDING_REFERENCE_BACKOFF_MAX_INTERVAL", 5*time.Minute, &errs),
					JitterFraction: envFloat("PENDING_REFERENCE_BACKOFF_JITTER", 0.2, &errs),
				},
				MaxAttempts:     envInt("PENDING_REFERENCE_MAX_ATTEMPTS", 10, &errs),
				TTL:             envDuration("PENDING_REFERENCE_TTL", 30*time.Minute, &errs),
				PollInterval:    envDuration("PENDING_REFERENCE_POLL_INTERVAL", 2*time.Second, &errs),
				MaxPollInterval: envDuration("PENDING_REFERENCE_MAX_POLL_INTERVAL", 10*time.Second, &errs),
				BatchSize:       envInt("PENDING_REFERENCE_BATCH_SIZE", 50, &errs),
			},
			OutboxPublisher: OutboxPublisherConfig{
				Backoff: BackoffConfig{
					BaseInterval:   envDuration("OUTBOX_BACKOFF_BASE", 2*time.Second, &errs),
					Factor:         envFloat("OUTBOX_BACKOFF_FACTOR", 2.0, &errs),
					MaxInterval:    envDuration("OUTBOX_BACKOFF_MAX_INTERVAL", 5*time.Minute, &errs),
					JitterFraction: envFloat("OUTBOX_BACKOFF_JITTER", 0.2, &errs),
				},
				PollInterval:    envDuration("OUTBOX_POLL_INTERVAL", 2*time.Second, &errs),
				MaxPollInterval: envDuration("OUTBOX_MAX_POLL_INTERVAL", 10*time.Second, &errs),
				BatchSize:       envInt("OUTBOX_BATCH_SIZE", 100, &errs),
			},
		},
	}

	validate(cfg, &errs)

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

func validate(cfg *Config, errs *[]error) {
	if cfg.HTTP.Port < 1 || cfg.HTTP.Port > 65535 {
		*errs = append(*errs, fmt.Errorf("HTTP_PORT: must be between 1 and 65535, got %d", cfg.HTTP.Port))
	}
	if cfg.HTTP.ShutdownTimeout <= 0 {
		*errs = append(*errs, errors.New("SHUTDOWN_TIMEOUT: must be positive"))
	}

	if cfg.Postgres.DatabaseURL != "" {
		if u, err := url.Parse(cfg.Postgres.DatabaseURL); err != nil {
			*errs = append(*errs, fmt.Errorf("DATABASE_URL: %w", err))
		} else if u.Scheme != "postgres" && u.Scheme != "postgresql" {
			*errs = append(*errs, fmt.Errorf("DATABASE_URL: scheme must be postgres:// or postgresql://, got %q", u.Scheme))
		}
	}
	if cfg.Postgres.MaxConns < 1 {
		*errs = append(*errs, fmt.Errorf("DB_MAX_CONNS: must be >= 1, got %d", cfg.Postgres.MaxConns))
	}
	if cfg.Postgres.MinConns < 0 {
		*errs = append(*errs, fmt.Errorf("DB_MIN_CONNS: must be >= 0, got %d", cfg.Postgres.MinConns))
	}
	if cfg.Postgres.MinConns > cfg.Postgres.MaxConns {
		*errs = append(*errs, fmt.Errorf("DB_MIN_CONNS (%d) must be <= DB_MAX_CONNS (%d)", cfg.Postgres.MinConns, cfg.Postgres.MaxConns))
	}
	if cfg.Postgres.ConnectTimeout <= 0 {
		*errs = append(*errs, errors.New("DB_CONNECT_TIMEOUT: must be positive"))
	}

	if strings.TrimSpace(cfg.AWS.Region) == "" {
		*errs = append(*errs, errors.New("AWS_REGION: must not be empty"))
	}
	if cfg.AWS.WagerTransactionsQueueURL != "" && cfg.AWS.WagerEventsQueueURL != "" &&
		cfg.AWS.WagerTransactionsQueueURL == cfg.AWS.WagerEventsQueueURL {
		*errs = append(*errs, errors.New("SQS_WAGER_TRANSACTIONS_QUEUE_URL and SQS_WAGER_EVENTS_QUEUE_URL must differ"))
	}
	if cfg.AWS.MaxMessages < 1 || cfg.AWS.MaxMessages > 10 {
		*errs = append(*errs, fmt.Errorf("SQS_MAX_MESSAGES: must be between 1 and 10 (SQS ReceiveMessage limit), got %d", cfg.AWS.MaxMessages))
	}
	if cfg.AWS.WaitTimeSeconds < 0 || cfg.AWS.WaitTimeSeconds > 20 {
		*errs = append(*errs, fmt.Errorf("SQS_WAIT_TIME_SECONDS: must be between 0 and 20 (SQS long-poll limit), got %d", cfg.AWS.WaitTimeSeconds))
	}
	if cfg.AWS.VisibilityTimeout < 1 {
		*errs = append(*errs, fmt.Errorf("SQS_VISIBILITY_TIMEOUT: must be >= 1, got %d", cfg.AWS.VisibilityTimeout))
	}
	if strings.TrimSpace(cfg.AWS.ConsumerName) == "" {
		*errs = append(*errs, errors.New("SQS_CONSUMER_NAME: must not be empty"))
	}

	if cfg.OIDC.IssuerURL != "" {
		if u, err := url.Parse(cfg.OIDC.IssuerURL); err != nil {
			*errs = append(*errs, fmt.Errorf("OIDC_ISSUER_URL: %w", err))
		} else if u.Scheme != "http" && u.Scheme != "https" {
			*errs = append(*errs, fmt.Errorf("OIDC_ISSUER_URL: scheme must be http or https, got %q", u.Scheme))
		}
	}

	validateBackoff("PENDING_REFERENCE_BACKOFF", cfg.Workers.PendingReference.Backoff, errs)
	if cfg.Workers.PendingReference.MaxAttempts < 1 {
		*errs = append(*errs, fmt.Errorf("PENDING_REFERENCE_MAX_ATTEMPTS: must be >= 1, got %d", cfg.Workers.PendingReference.MaxAttempts))
	}
	if cfg.Workers.PendingReference.TTL <= 0 {
		*errs = append(*errs, errors.New("PENDING_REFERENCE_TTL: must be positive"))
	}
	validatePoll("PENDING_REFERENCE", cfg.Workers.PendingReference.PollInterval, cfg.Workers.PendingReference.MaxPollInterval, cfg.Workers.PendingReference.BatchSize, errs)

	validateBackoff("OUTBOX_BACKOFF", cfg.Workers.OutboxPublisher.Backoff, errs)
	validatePoll("OUTBOX", cfg.Workers.OutboxPublisher.PollInterval, cfg.Workers.OutboxPublisher.MaxPollInterval, cfg.Workers.OutboxPublisher.BatchSize, errs)
}

func validateBackoff(prefix string, b BackoffConfig, errs *[]error) {
	if b.BaseInterval <= 0 {
		*errs = append(*errs, fmt.Errorf("%s_BASE: must be positive", prefix))
	}
	if b.Factor <= 1 {
		*errs = append(*errs, fmt.Errorf("%s_FACTOR: must be > 1, got %v", prefix, b.Factor))
	}
	if b.MaxInterval < b.BaseInterval {
		*errs = append(*errs, fmt.Errorf("%s_MAX_INTERVAL must be >= %s_BASE", prefix, prefix))
	}
	if b.JitterFraction < 0 || b.JitterFraction >= 1 {
		*errs = append(*errs, fmt.Errorf("%s_JITTER: must be in [0, 1), got %v", prefix, b.JitterFraction))
	}
}

func validatePoll(prefix string, pollInterval, maxPollInterval time.Duration, batchSize int, errs *[]error) {
	if pollInterval <= 0 {
		*errs = append(*errs, fmt.Errorf("%s_POLL_INTERVAL: must be positive", prefix))
	}
	if maxPollInterval < pollInterval {
		*errs = append(*errs, fmt.Errorf("%s_MAX_POLL_INTERVAL must be >= %s_POLL_INTERVAL", prefix, prefix))
	}
	if batchSize < 1 {
		*errs = append(*errs, fmt.Errorf("%s_BATCH_SIZE: must be >= 1, got %d", prefix, batchSize))
	}
}

func envString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func requireString(key string, errs *[]error) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		*errs = append(*errs, fmt.Errorf("%s is required", key))
	}
	return v
}

func envInt(key string, def int, errs *[]error) int {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: invalid integer %q: %w", key, raw, err))
		return def
	}
	return v
}

func envInt32(key string, def int32, errs *[]error) int32 {
	return int32(envInt(key, int(def), errs))
}

func envFloat(key string, def float64, errs *[]error) float64 {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: invalid float %q: %w", key, raw, err))
		return def
	}
	return v
}

func envDuration(key string, def time.Duration, errs *[]error) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	v, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: invalid duration %q (expected e.g. \"5s\", \"2m\"): %w", key, raw, err))
		return def
	}
	return v
}

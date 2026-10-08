package fxplatform

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"

	"backend-challenge-go/internal/platform/logging"
	"backend-challenge-go/internal/platform/metrics"
)

var LoggingModule = fx.Module("logging",
	fx.Provide(
		newLogger,
		newMetricsRegistry,
		newMetrics,
	),
)

func newLogger() (*slog.Logger, error) {
	level, err := logging.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return nil, fmt.Errorf("logging: %w", err)
	}
	return logging.NewDefault(level), nil
}

func newMetricsRegistry() *prometheus.Registry {
	return prometheus.NewRegistry()
}

func newMetrics(reg *prometheus.Registry) *metrics.Metrics {
	return metrics.New(reg)
}

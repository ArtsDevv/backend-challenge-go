package fxplatform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/fx"

	httpadapter "backend-challenge-go/internal/adapters/http"
	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/ports"
)

var HTTPModule = fx.Module("http",
	fx.Provide(
		httpadapter.NewWalletHandler,
		httpadapter.NewWageringHandler,
		provideHealthHandler,
		provideRouter,
	),
	fx.Invoke(
		registerAPIServer,
		registerMetricsServer,
	),
)

type sqsPinger struct {
	client   *awssqs.Client
	queueURL string
}

func (p *sqsPinger) Ping(ctx context.Context) error {
	_, err := p.client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(p.queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	return err
}

func provideHealthHandler(pool *pgxpool.Pool, client *awssqs.Client, awsCfg config.AWSConfig) *httpadapter.HealthHandler {
	return httpadapter.NewHealthHandler(pool, &sqsPinger{client: client, queueURL: awsCfg.WagerTransactionsQueueURL})
}

func provideRouter(
	wallet *httpadapter.WalletHandler,
	wagering *httpadapter.WageringHandler,
	health *httpadapter.HealthHandler,
	validator ports.TokenValidator,
) chi.Router {
	return httpadapter.NewRouter(httpadapter.RouterParams{
		Wallet:    wallet,
		Wagering:  wagering,
		Health:    health,
		Validator: validator,
	})
}

func registerAPIServer(lc fx.Lifecycle, router chi.Router, cfg config.HTTPConfig, logger *slog.Logger) {
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: router,
	}

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ln, err := net.Listen("tcp", srv.Addr)
			if err != nil {
				return fmt.Errorf("http: listen on %s: %w", srv.Addr, err)
			}
			logger.Info("http: api server starting", slog.String("addr", srv.Addr))
			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("http: api server stopped unexpectedly", slog.Any("error", err))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("http: api server shutting down")
			return srv.Shutdown(ctx)
		},
	})
}

func registerMetricsServer(lc fx.Lifecycle, reg *prometheus.Registry, logger *slog.Logger) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	addr := ":" + metricsPort()
	srv := &http.Server{Addr: addr, Handler: mux}

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("metrics: listen on %s: %w", addr, err)
			}
			logger.Info("metrics: server starting", slog.String("addr", addr))
			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("metrics: server stopped unexpectedly", slog.Any("error", err))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("metrics: server shutting down")
			return srv.Shutdown(ctx)
		},
	})
}

func metricsPort() string {
	if p := os.Getenv("METRICS_PORT"); p != "" {
		return p
	}
	return "9090"
}

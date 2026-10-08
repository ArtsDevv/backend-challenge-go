package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"go.uber.org/fx"

	"backend-challenge-go/internal/config"
	fxplatform "backend-challenge-go/internal/platform/fx"
	"backend-challenge-go/internal/platform/logging"
)

func main() {
	if os.Getenv("APP_ENV") != "production" {
		_ = godotenv.Load()
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	logger := logging.NewDefault(slog.LevelInfo)

	app := fx.New(
		fx.Supply(cfg),
		fx.StopTimeout(cfg.HTTP.ShutdownTimeout),
		fxplatform.Module,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("starting")
	if err := app.Start(ctx); err != nil {
		logger.Error("startup failed", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("started")

	<-ctx.Done()
	logger.Info("shutdown signal received, stopping")

	stopCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()
	if err := app.Stop(stopCtx); err != nil {
		logger.Error("shutdown failed", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("stopped")
}

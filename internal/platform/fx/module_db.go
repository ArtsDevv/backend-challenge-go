package fxplatform

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"backend-challenge-go/internal/adapters/postgres"
	"backend-challenge-go/internal/config"
)

var DBModule = fx.Module("db",
	fx.Provide(newPgxPool),
)

func newPgxPool(lc fx.Lifecycle, cfg config.PostgresConfig) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ConnectTimeout)
	defer cancel()

	pool, err := postgres.NewPool(ctx, cfg)
	if err != nil {
		return nil, err
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
			defer cancel()
			if err := pool.Ping(pingCtx); err != nil {
				return fmt.Errorf("db: ping failed: %w", err)
			}
			return nil
		},
		OnStop: func(context.Context) error {
			pool.Close()
			return nil
		},
	})

	return pool, nil
}

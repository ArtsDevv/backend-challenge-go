package fxplatform

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

var Module = fx.Options(
	ConfigModule,
	LoggingModule,
	DBModule,
	SQSModule,
	AuthModule,
	RepositoriesModule,
	UseCasesModule,
	HTTPModule,
	WorkersModule,
	fx.WithLogger(newFxEventLogger),
)

func newFxEventLogger(logger *slog.Logger) fxevent.Logger {
	return &fxevent.SlogLogger{Logger: logger}
}

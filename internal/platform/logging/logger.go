package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

const (
	FieldCorrelationID = "correlationId"
	FieldMessageID     = "messageId"
	FieldTransactionID = "transactionId"
	FieldWalletID      = "walletId"
	FieldProviderID    = "providerId"
)

func New(w io.Writer, level slog.Level) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
	})
	return slog.New(handler)
}

func NewDefault(level slog.Level) *slog.Logger {
	return New(os.Stdout, level)
}

func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("logging: unknown level %q (want debug, info, warn or error)", s)
	}
}

// --- field attribute helpers -----------------------------------------------

func CorrelationID(v string) slog.Attr { return slog.String(FieldCorrelationID, v) }
func MessageID(v string) slog.Attr     { return slog.String(FieldMessageID, v) }
func TransactionID(v string) slog.Attr { return slog.String(FieldTransactionID, v) }
func WalletID(v string) slog.Attr      { return slog.String(FieldWalletID, v) }
func ProviderID(v string) slog.Attr    { return slog.String(FieldProviderID, v) }

// --- derived-logger helpers -------------------------------------------------

func WithCorrelationID(l *slog.Logger, v string) *slog.Logger { return l.With(CorrelationID(v)) }
func WithMessageID(l *slog.Logger, v string) *slog.Logger     { return l.With(MessageID(v)) }
func WithTransactionID(l *slog.Logger, v string) *slog.Logger { return l.With(TransactionID(v)) }
func WithWalletID(l *slog.Logger, v string) *slog.Logger      { return l.With(WalletID(v)) }
func WithProviderID(l *slog.Logger, v string) *slog.Logger    { return l.With(ProviderID(v)) }

type ctxKey struct{}

func IntoContext(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

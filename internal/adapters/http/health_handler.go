package http

import (
	"context"
	"net/http"
	"time"

	"backend-challenge-go/internal/platform/logging"
)

const defaultReadyTimeout = 2 * time.Second

type Pinger interface {
	Ping(ctx context.Context) error
}

type HealthHandler struct {
	DB           Pinger
	SQS          Pinger
	ReadyTimeout time.Duration
}

func NewHealthHandler(db, sqs Pinger) *HealthHandler {
	return &HealthHandler{DB: db, SQS: sqs, ReadyTimeout: defaultReadyTimeout}
}

func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthStatusResponse{Status: "UP"})
}

func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	timeout := h.ReadyTimeout
	if timeout <= 0 {
		timeout = defaultReadyTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	logger := logging.FromContext(r.Context())
	checks := make(map[string]string, 2)
	ready := true

	if err := h.DB.Ping(ctx); err != nil {
		logger.Warn("readiness check failed", "dependency", "postgres", "error", err.Error())
		checks["postgres"] = "DOWN"
		ready = false
	} else {
		checks["postgres"] = "UP"
	}

	if err := h.SQS.Ping(ctx); err != nil {
		logger.Warn("readiness check failed", "dependency", "sqs", "error", err.Error())
		checks["sqs"] = "DOWN"
		ready = false
	} else {
		checks["sqs"] = "UP"
	}

	status := http.StatusOK
	overall := "UP"
	if !ready {
		status = http.StatusServiceUnavailable
		overall = "DOWN"
	}
	writeJSON(w, status, ReadinessResponse{Status: overall, Checks: checks})
}

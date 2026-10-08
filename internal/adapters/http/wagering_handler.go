package http

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	authmw "backend-challenge-go/internal/adapters/http/middleware"
	wageringapp "backend-challenge-go/internal/application/wagering"
	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagering"
	"backend-challenge-go/internal/domain/wallet"
	"backend-challenge-go/internal/platform/metrics"
	"backend-challenge-go/internal/ports"
)

type WageringHandler struct {
	ProcessTransaction *wageringapp.ProcessTransactionUseCase
	Transactions       ports.TransactionRepository
	Metrics            *metrics.Metrics
}

func NewWageringHandler(processTransaction *wageringapp.ProcessTransactionUseCase, transactions ports.TransactionRepository, m *metrics.Metrics) *WageringHandler {
	return &WageringHandler{ProcessTransaction: processTransaction, Transactions: transactions, Metrics: m}
}

func (h *WageringHandler) Process(w http.ResponseWriter, r *http.Request) {
	claims, ok := authmw.ClaimsFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "missing authentication context")
		return
	}

	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Idempotency-Key header is required")
		return
	}

	var req ProcessTransactionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if req.Money.Currency() == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "money is required")
		return
	}

	if err := authmw.AuthorizeProvider(claims, req.ProviderID); err != nil {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "token identity does not match providerId")
		return
	}

	start := time.Now()
	result, err := h.ProcessTransaction.Execute(r.Context(), wageringapp.ProcessTransactionCommand{
		ProviderID:                     req.ProviderID,
		ExternalTransactionID:          req.ExternalTransactionID,
		IdempotencyKey:                 idempotencyKey,
		PlayerID:                       req.PlayerID,
		WalletID:                       req.WalletID,
		RoundID:                        req.RoundID,
		GameID:                         req.GameID,
		Kind:                           wagering.Kind(req.Kind),
		Money:                          req.Money,
		ReferenceExternalTransactionID: req.ReferenceExternalTransactionID,
		CorrelationID:                  correlationIDFromRequest(r),
	})
	if h.Metrics != nil {
		h.Metrics.ObserveProcessingLatency("process_wager_transaction_http", time.Since(start))
	}
	if err != nil {
		writeMappedError(w, r, err)
		return
	}

	if h.Metrics != nil {
		h.Metrics.TransactionsByStatus.WithLabelValues(string(result.Status), req.Kind).Inc()
		if result.IdempotentReplay {
			h.Metrics.DuplicateRequests.WithLabelValues(metrics.SourceHTTP, metrics.ReasonIdempotentReplay).Inc()
		}
	}

	status := http.StatusCreated
	switch {
	case result.IdempotentReplay:
		status = http.StatusOK
	case result.Status == wagering.StatusPendingReference:
		status = http.StatusAccepted
	}
	writeJSON(w, status, toProcessTransactionResponse(result))
}

func (h *WageringHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	claims, ok := authmw.ClaimsFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "missing authentication context")
		return
	}

	transactionID, err := uuid.Parse(chi.URLParam(r, "transactionID"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "transactionId must be a valid UUID")
		return
	}

	tx, err := h.Transactions.FindByID(r.Context(), transactionID)
	if err != nil {
		writeMappedError(w, r, err)
		return
	}
	if err := authmw.AuthorizeProvider(claims, tx.ProviderID()); err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource not found")
		return
	}

	writeJSON(w, http.StatusOK, toWagerTransactionResponse(tx))
}

func (h *WageringHandler) GetByProviderAndExternalID(w http.ResponseWriter, r *http.Request) {
	claims, ok := authmw.ClaimsFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "missing authentication context")
		return
	}

	providerID := chi.URLParam(r, "providerID")
	externalTransactionID := chi.URLParam(r, "externalTransactionID")

	if err := authmw.AuthorizeProvider(claims, providerID); err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource not found")
		return
	}

	tx, err := h.Transactions.FindByProviderAndExternalTransactionID(r.Context(), providerID, externalTransactionID)
	if err != nil {
		writeMappedError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, toWagerTransactionResponse(tx))
}

func toProcessTransactionResponse(result wageringapp.ProcessTransactionResult) ProcessTransactionResponse {
	resp := ProcessTransactionResponse{
		TransactionID:    result.TransactionID,
		Status:           string(result.Status),
		IdempotentReplay: result.IdempotentReplay,
	}
	if result.Balance.Currency() != "" {
		balance := result.Balance
		version := result.WalletVersion
		resp.Balance = &balance
		resp.WalletVersion = &version
	}
	if result.FailureCode != nil {
		code := string(*result.FailureCode)
		resp.FailureCode = &code
	}
	return resp
}

func toWagerTransactionResponse(tx *wagering.WagerTransaction) WagerTransactionResponse {
	resp := WagerTransactionResponse{
		TransactionID:                  tx.InternalID(),
		ExternalTransactionID:          tx.ExternalTransactionID(),
		ProviderID:                     tx.ProviderID(),
		PlayerID:                       tx.PlayerID(),
		WalletID:                       tx.WalletID(),
		RoundID:                        tx.RoundID(),
		GameID:                         tx.GameID(),
		Kind:                           string(tx.Kind()),
		Money:                          tx.Money(),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
		Status:                         string(tx.Status()),
		CreatedAt:                      tx.CreatedAt(),
		UpdatedAt:                      tx.UpdatedAt(),
		ProcessedAt:                    tx.ProcessedAt(),
		ResultWalletVersion:            tx.ResultWalletVersion(),
	}
	if tx.FailureCode() != nil {
		code := string(*tx.FailureCode())
		resp.FailureCode = &code
	}
	if tx.ResultBalanceMinor() != nil {
		if balance, err := money.FromMinorUnits(*tx.ResultBalanceMinor(), tx.Money().Currency()); err == nil {
			resp.ResultBalance = &balance
		}
	}
	return resp
}

type httpError struct {
	status  int
	code    string
	message string
}

func mapError(err error) httpError {
	switch {
	// --- 400: malformed/invalid payload, rejected before any state change -------------------
	case errors.Is(err, money.ErrEmptyAmount),
		errors.Is(err, money.ErrInvalidFormat),
		errors.Is(err, money.ErrScaleExceeded),
		errors.Is(err, money.ErrNegativeNotAllowed),
		errors.Is(err, money.ErrInvalidCurrency),
		errors.Is(err, money.ErrCurrencyMismatch),
		errors.Is(err, money.ErrOverflow),
		errors.Is(err, wagering.ErrInvalidKindForExternal),
		errors.Is(err, wagering.ErrMissingRequiredField),
		errors.Is(err, wagering.ErrReversalRequiresReference),
		errors.Is(err, wagering.ErrInvalidAmountForKind),
		errors.Is(err, wallet.ErrInvalidWallet),
		errors.Is(err, wallet.ErrInvalidAmount),
		errors.Is(err, wallet.ErrCurrencyMismatch):
		return httpError{http.StatusBadRequest, "INVALID_REQUEST", err.Error()}

	// --- 409: conflicts --------------------------------------------------------------------
	case errors.Is(err, wallet.ErrWalletAlreadyExists):
		return httpError{http.StatusConflict, "WALLET_ALREADY_EXISTS", err.Error()}
	case errors.Is(err, ports.ErrExternalTransactionAlreadyExists):
		return httpError{http.StatusConflict, "EXTERNAL_TRANSACTION_ALREADY_EXISTS", err.Error()}
	case errors.Is(err, ports.ErrIdempotencyKeyConflict):
		return httpError{http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT", err.Error()}

	// --- 404: not found, or a cross-provider lookup masked as not found (default case for
	case errors.Is(err, ports.ErrNotFound):
		return httpError{http.StatusNotFound, "NOT_FOUND", "resource not found"}
	case errors.Is(err, authmw.ErrCrossProviderAccess):
		return httpError{http.StatusNotFound, "NOT_FOUND", "resource not found"}

	// --- 403: scope failure other than cross-provider masking --------------------------------
	case errors.Is(err, authmw.ErrInternalServiceOnly):
		return httpError{http.StatusForbidden, "FORBIDDEN", "operation restricted to the internal service identity"}

	// --- 401 ---------------------------------------------------------------------------------
	case errors.Is(err, ports.ErrInvalidToken):
		return httpError{http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid bearer token"}

	// --- 503: transient infrastructure failure, safe to retry verbatim -----------------------
	case errors.Is(err, ports.ErrWalletLockTimeout):
		return httpError{http.StatusServiceUnavailable, "LOCK_TIMEOUT", "wallet temporarily locked, retry"}
	case errors.Is(err, ports.ErrSerializationFailure):
		return httpError{http.StatusServiceUnavailable, "SERIALIZATION_FAILURE", "transient conflict, retry"}
	case errors.Is(err, ports.ErrRepositoryUnavailable):
		return httpError{http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "dependency temporarily unavailable"}

	default:
		return httpError{http.StatusInternalServerError, "INTERNAL_ERROR", "internal error"}
	}
}

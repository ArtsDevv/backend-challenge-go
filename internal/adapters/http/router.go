package http

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	authmw "backend-challenge-go/internal/adapters/http/middleware"
	"backend-challenge-go/internal/platform/logging"
	"backend-challenge-go/internal/ports"
)

type RouterParams struct {
	Wallet    *WalletHandler
	Wagering  *WageringHandler
	Health    *HealthHandler
	Validator ports.TokenValidator
}

func NewRouter(p RouterParams) chi.Router {
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.Recoverer)

	r.Get("/health/live", p.Health.Live)
	r.Get("/health/ready", p.Health.Ready)

	r.Group(func(business chi.Router) {
		business.Use(authmw.Authenticate(p.Validator))

		business.Route("/wallets", func(wr chi.Router) {
			wr.Use(authmw.RequireInternalService)
			wr.Post("/", p.Wallet.Open)
			wr.Get("/{walletID}", p.Wallet.Get)
			wr.Get("/{walletID}/ledger", p.Wallet.ListLedger)
			wr.Post("/{walletID}/reconciliation", p.Wallet.Reconcile)
		})

		business.Route("/wagering/transactions", func(wr chi.Router) {
			wr.Post("/", p.Wagering.Process)
			wr.Get("/{transactionID}", p.Wagering.GetByID)
		})

		business.Get("/providers/{providerID}/wagering/transactions/{externalTransactionID}", p.Wagering.GetByProviderAndExternalID)
	})

	return r
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	logging.FromContext(r.Context()).Warn("http request rejected", "status", status, "code", code)
	writeJSON(w, status, ErrorResponse{Code: code, Message: message})
}

func writeMappedError(w http.ResponseWriter, r *http.Request, err error) {
	he := mapError(err)
	if he.status == http.StatusInternalServerError {
		logging.FromContext(r.Context()).Error("unmapped error reached writeMappedError", "error", err.Error())
	}
	writeError(w, r, he.status, he.code, he.message)
}

func decodeJSON(r *http.Request, dst interface{}) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func correlationIDFromRequest(r *http.Request) uuid.UUID {
	raw := r.Header.Get("X-Correlation-Id")
	if raw == "" {
		return uuid.Nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil
	}
	return id
}

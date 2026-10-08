package http

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	wageringapp "backend-challenge-go/internal/application/wagering"
	"backend-challenge-go/internal/application/wallets"
	"backend-challenge-go/internal/ports"
)

const (
	defaultLedgerPageSize = 50
	maxLedgerPageSize     = 200
)

type WalletHandler struct {
	OpenWallet     *wallets.OpenWalletUseCase
	Reconciliation *wageringapp.ReconciliationUseCase
	Wallets        ports.WalletRepository
	Ledger         ports.LedgerRepository
}

func NewWalletHandler(
	openWallet *wallets.OpenWalletUseCase,
	reconciliation *wageringapp.ReconciliationUseCase,
	walletRepo ports.WalletRepository,
	ledgerRepo ports.LedgerRepository,
) *WalletHandler {
	return &WalletHandler{
		OpenWallet:     openWallet,
		Reconciliation: reconciliation,
		Wallets:        walletRepo,
		Ledger:         ledgerRepo,
	}
}

func (h *WalletHandler) Open(w http.ResponseWriter, r *http.Request) {
	var req OpenWalletRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if req.PlayerID == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "playerId is required")
		return
	}
	if req.InitialBalance.Currency() == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "initialBalance is required")
		return
	}

	result, err := h.OpenWallet.Execute(r.Context(), wallets.OpenWalletCommand{
		PlayerID:       req.PlayerID,
		InitialBalance: req.InitialBalance,
		CorrelationID:  correlationIDFromRequest(r),
	})
	if err != nil {
		writeMappedError(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, WalletResponse{
		ID:       result.WalletID,
		PlayerID: result.PlayerID,
		Balance:  result.Balance,
		Version:  result.Version,
	})
}

func (h *WalletHandler) Get(w http.ResponseWriter, r *http.Request) {
	walletID, err := uuid.Parse(chi.URLParam(r, "walletID"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "walletId must be a valid UUID")
		return
	}

	wlt, err := h.Wallets.FindByID(r.Context(), walletID)
	if err != nil {
		writeMappedError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, WalletResponse{
		ID:       wlt.ID(),
		PlayerID: wlt.PlayerID(),
		Balance:  wlt.Balance(),
		Version:  wlt.Version(),
	})
}

func (h *WalletHandler) ListLedger(w http.ResponseWriter, r *http.Request) {
	walletID, err := uuid.Parse(chi.URLParam(r, "walletID"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "walletId must be a valid UUID")
		return
	}

	limit := defaultLedgerPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, convErr := strconv.Atoi(raw)
		if convErr != nil || parsed <= 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	if limit > maxLedgerPageSize {
		limit = maxLedgerPageSize
	}
	cursor := r.URL.Query().Get("cursor")

	entries, nextCursor, err := h.Ledger.ListByWallet(r.Context(), walletID, cursor, limit)
	if err != nil {
		writeMappedError(w, r, err)
		return
	}

	resp := LedgerPageResponse{
		Entries:    make([]LedgerEntryResponse, 0, len(entries)),
		NextCursor: nextCursor,
	}
	for _, entry := range entries {
		resp.Entries = append(resp.Entries, LedgerEntryResponse{
			ID:            entry.ID(),
			WalletID:      entry.WalletID(),
			TransactionID: entry.TransactionID(),
			Direction:     string(entry.Direction()),
			Amount:        entry.Amount(),
			BalanceBefore: entry.BalanceBefore(),
			BalanceAfter:  entry.BalanceAfter(),
			CreatedAt:     entry.CreatedAt(),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *WalletHandler) Reconcile(w http.ResponseWriter, r *http.Request) {
	walletID, err := uuid.Parse(chi.URLParam(r, "walletID"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "walletId must be a valid UUID")
		return
	}

	result, err := h.Reconciliation.Execute(r.Context(), walletID)
	if err != nil {
		writeMappedError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, ReconciliationResponse{
		WalletID:          result.WalletID,
		StoredBalance:     result.StoredBalance,
		CalculatedBalance: result.CalculatedBalance,
		Difference:        result.Difference,
		Consistent:        result.Consistent,
		CheckedEntries:    result.CheckedEntries,
	})
}

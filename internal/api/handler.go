package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"koho-assignment/internal/ledger"
)

type store interface {
	OpenAccount(context.Context, int64) (ledger.Account, error)
	Balance(context.Context, string) (int64, error)
	History(context.Context, string) ([]ledger.Transfer, error)
	Transfer(context.Context, ledger.TransferRequest) (ledger.Transfer, error)
}

type handler struct{ store store }

func New(store store) http.Handler {
	h := &handler{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /accounts", h.openAccount)
	mux.HandleFunc("GET /accounts/{id}/balance", h.balance)
	mux.HandleFunc("GET /accounts/{id}/transactions", h.history)
	mux.HandleFunc("POST /transfers", h.transfer)
	return mux
}

func decode(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, ledger.ErrInvalidAmount), errors.Is(err, ledger.ErrInvalidAccount), errors.Is(err, ledger.ErrInvalidIdempotencyKey):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, ledger.ErrAccountNotFound):
		status, code = http.StatusNotFound, "account_not_found"
	case errors.Is(err, ledger.ErrIdempotencyPayloadMismatch):
		status, code = http.StatusConflict, "idempotency_payload_mismatch"
	case errors.Is(err, ledger.ErrInsufficientFunds):
		status, code = http.StatusUnprocessableEntity, "insufficient_funds"
	case errors.Is(err, ledger.ErrBalanceOverflow):
		status, code = http.StatusUnprocessableEntity, "balance_overflow"
	default:
		slog.Error("request failed", "error", err)
	}
	writeJSON(w, status, map[string]string{"error": code})
}

func (h *handler) openAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		InitialBalanceCents *int64 `json:"initial_balance_cents"`
	}
	if err := decode(w, r, &body); err != nil || body.InitialBalanceCents == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	account, err := h.store.OpenAccount(r.Context(), *body.InitialBalanceCents)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, account)
}

func (h *handler) balance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	balance, err := h.store.Balance(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account_id": id, "balance_cents": balance})
}

func (h *handler) history(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	history, err := h.store.History(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account_id": id, "transactions": history})
}

func (h *handler) transfer(w http.ResponseWriter, r *http.Request) {
	var body ledger.TransferRequest
	if err := decode(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	result, err := h.store.Transfer(r.Context(), body)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replay {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

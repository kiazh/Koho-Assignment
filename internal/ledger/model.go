package ledger

import (
	"errors"
	"time"
)

var (
	ErrInvalidAmount              = errors.New("invalid amount")
	ErrInvalidAccount             = errors.New("invalid account")
	ErrAccountNotFound            = errors.New("account not found")
	ErrInsufficientFunds          = errors.New("insufficient funds")
	ErrBalanceOverflow            = errors.New("balance overflow")
	ErrIdempotencyPayloadMismatch = errors.New("idempotency payload mismatch")
	ErrInvalidIdempotencyKey      = errors.New("invalid idempotency key")
)

type Account struct {
	ID                  string    `json:"id"`
	InitialBalanceCents int64     `json:"initial_balance_cents"`
	BalanceCents        int64     `json:"balance_cents"`
	CreatedAt           time.Time `json:"created_at"`
}

type TransferRequest struct {
	FromAccountID  string `json:"from_account_id"`
	ToAccountID    string `json:"to_account_id"`
	AmountCents    int64  `json:"amount_cents"`
	IdempotencyKey string `json:"idempotency_key"`
}

type Transfer struct {
	ID             string    `json:"id"`
	FromAccountID  string    `json:"from_account_id"`
	ToAccountID    string    `json:"to_account_id"`
	AmountCents    int64     `json:"amount_cents"`
	IdempotencyKey string    `json:"idempotency_key"`
	CreatedAt      time.Time `json:"created_at"`
	Replay         bool      `json:"-"`
}

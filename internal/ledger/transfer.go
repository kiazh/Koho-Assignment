package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	beginImmediateAttempts   = 3
	beginImmediateRetryDelay = 100 * time.Millisecond
)

func beginImmediate(ctx context.Context, conn *sql.Conn) error {
	var err error
	for attempt := 0; attempt < beginImmediateAttempts; attempt++ {
		if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err == nil {
			return nil
		}
		if !isSQLiteBusy(err) || attempt == beginImmediateAttempts-1 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * beginImmediateRetryDelay):
		}
	}
	return err
}

func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_BUSY
}

func validateTransfer(request TransferRequest) error {
	if request.AmountCents <= 0 {
		return ErrInvalidAmount
	}
	if request.FromAccountID == "" || request.ToAccountID == "" || request.FromAccountID == request.ToAccountID {
		return ErrInvalidAccount
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" || len(request.IdempotencyKey) > 255 {
		return ErrInvalidIdempotencyKey
	}
	return nil
}

func (s *Store) Transfer(ctx context.Context, request TransferRequest) (Transfer, error) {
	if err := validateTransfer(request); err != nil {
		return Transfer{}, err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Transfer{}, err
	}
	defer conn.Close()
	if err := beginImmediate(ctx, conn); err != nil {
		return Transfer{}, fmt.Errorf("begin transfer: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()

	var existing Transfer
	var existingAt string
	err = conn.QueryRowContext(ctx, `SELECT id, from_account_id, to_account_id, amount_cents, created_at
		FROM transfers WHERE idempotency_key = ?`, request.IdempotencyKey).
		Scan(&existing.ID, &existing.FromAccountID, &existing.ToAccountID, &existing.AmountCents, &existingAt)
	if err == nil {
		if existing.FromAccountID != request.FromAccountID || existing.ToAccountID != request.ToAccountID || existing.AmountCents != request.AmountCents {
			return Transfer{}, ErrIdempotencyPayloadMismatch
		}
		existing.IdempotencyKey = request.IdempotencyKey
		existing.CreatedAt, err = time.Parse(time.RFC3339Nano, existingAt)
		if err != nil {
			return Transfer{}, err
		}
		existing.Replay = true
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return Transfer{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, err
	}

	var sourceBalance, destinationBalance int64
	if err := conn.QueryRowContext(ctx, "SELECT balance_cents FROM accounts WHERE id = ?", request.FromAccountID).Scan(&sourceBalance); errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, ErrAccountNotFound
	} else if err != nil {
		return Transfer{}, err
	}
	if err := conn.QueryRowContext(ctx, "SELECT balance_cents FROM accounts WHERE id = ?", request.ToAccountID).Scan(&destinationBalance); errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, ErrAccountNotFound
	} else if err != nil {
		return Transfer{}, err
	}
	if sourceBalance < request.AmountCents {
		return Transfer{}, ErrInsufficientFunds
	}
	if destinationBalance > math.MaxInt64-request.AmountCents {
		return Transfer{}, ErrBalanceOverflow
	}
	debit, err := conn.ExecContext(ctx, `UPDATE accounts SET balance_cents = balance_cents - ?
		WHERE id = ? AND balance_cents >= ?`, request.AmountCents, request.FromAccountID, request.AmountCents)
	if err != nil {
		return Transfer{}, err
	}
	rows, err := debit.RowsAffected()
	if err != nil {
		return Transfer{}, err
	}
	if rows != 1 {
		return Transfer{}, ErrInsufficientFunds
	}
	credit, err := conn.ExecContext(ctx, "UPDATE accounts SET balance_cents = balance_cents + ? WHERE id = ?", request.AmountCents, request.ToAccountID)
	if err != nil {
		return Transfer{}, err
	}
	rows, err = credit.RowsAffected()
	if err != nil {
		return Transfer{}, err
	}
	if rows != 1 {
		return Transfer{}, fmt.Errorf("receiver credit affected %d rows", rows)
	}
	id, err := newID()
	if err != nil {
		return Transfer{}, err
	}
	created := time.Now().UTC()
	stamp := created.Format(time.RFC3339Nano)
	if _, err := conn.ExecContext(ctx, `INSERT INTO transfers(id, idempotency_key, from_account_id, to_account_id, amount_cents, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, id, request.IdempotencyKey, request.FromAccountID, request.ToAccountID, request.AmountCents, stamp); err != nil {
		return Transfer{}, err
	}
	for _, entry := range []struct {
		accountID string
		amount    int64
	}{{request.FromAccountID, -request.AmountCents}, {request.ToAccountID, request.AmountCents}} {
		entryID, err := newID()
		if err != nil {
			return Transfer{}, err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO ledger_entries(id, transfer_id, account_id, amount_cents, created_at)
			VALUES (?, ?, ?, ?, ?)`, entryID, id, entry.accountID, entry.amount, stamp); err != nil {
			return Transfer{}, err
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Transfer{}, err
	}
	return Transfer{ID: id, FromAccountID: request.FromAccountID, ToAccountID: request.ToAccountID, AmountCents: request.AmountCents, IdempotencyKey: request.IdempotencyKey, CreatedAt: created}, nil
}

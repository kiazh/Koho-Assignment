package ledger

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 10000",
		`CREATE TABLE IF NOT EXISTS accounts (
			id TEXT PRIMARY KEY,
			initial_balance_cents INTEGER NOT NULL CHECK(initial_balance_cents >= 0),
			balance_cents INTEGER NOT NULL CHECK(balance_cents >= 0),
			created_at TEXT NOT NULL
		) STRICT`,
		`CREATE TABLE IF NOT EXISTS transfers (
			id TEXT PRIMARY KEY,
			idempotency_key TEXT NOT NULL UNIQUE,
			from_account_id TEXT NOT NULL REFERENCES accounts(id),
			to_account_id TEXT NOT NULL REFERENCES accounts(id),
			amount_cents INTEGER NOT NULL CHECK(amount_cents > 0),
			created_at TEXT NOT NULL,
			CHECK(from_account_id <> to_account_id)
		) STRICT`,
		`CREATE TABLE IF NOT EXISTS ledger_entries (
			id TEXT PRIMARY KEY,
			transfer_id TEXT NOT NULL REFERENCES transfers(id),
			account_id TEXT NOT NULL REFERENCES accounts(id),
			amount_cents INTEGER NOT NULL CHECK(amount_cents <> 0),
			created_at TEXT NOT NULL,
			UNIQUE(transfer_id, account_id)
		) STRICT`,
		"CREATE INDEX IF NOT EXISTS ledger_entries_account_time ON ledger_entries(account_id, created_at, id)",
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize database: %w", err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func newID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func (s *Store) OpenAccount(ctx context.Context, initialCents int64) (Account, error) {
	if initialCents < 0 {
		return Account{}, ErrInvalidAmount
	}
	id, err := newID()
	if err != nil {
		return Account{}, err
	}
	created := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, "INSERT INTO accounts(id, initial_balance_cents, balance_cents, created_at) VALUES (?, ?, ?, ?)", id, initialCents, initialCents, created.Format(time.RFC3339Nano))
	if err != nil {
		return Account{}, err
	}
	return Account{ID: id, InitialBalanceCents: initialCents, BalanceCents: initialCents, CreatedAt: created}, nil
}

func (s *Store) Balance(ctx context.Context, accountID string) (int64, error) {
	var balance int64
	err := s.db.QueryRowContext(ctx, "SELECT balance_cents FROM accounts WHERE id = ?", accountID).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrAccountNotFound
	}
	return balance, err
}

func (s *Store) History(ctx context.Context, accountID string) ([]Transfer, error) {
	if _, err := s.Balance(ctx, accountID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.from_account_id, t.to_account_id, t.amount_cents, t.idempotency_key, t.created_at
		FROM ledger_entries e JOIN transfers t ON t.id = e.transfer_id
		WHERE e.account_id = ?`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := make([]Transfer, 0)
	for rows.Next() {
		var transfer Transfer
		var created string
		if err := rows.Scan(&transfer.ID, &transfer.FromAccountID, &transfer.ToAccountID, &transfer.AmountCents, &transfer.IdempotencyKey, &created); err != nil {
			return nil, err
		}
		transfer.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		history = append(history, transfer)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(history, func(i, j int) bool {
		if history[i].CreatedAt.Equal(history[j].CreatedAt) {
			return history[i].ID < history[j].ID
		}
		return history[i].CreatedAt.Before(history[j].CreatedAt)
	})
	return history, nil
}

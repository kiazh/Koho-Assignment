package ledger

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestOpenAccountPreservesExactBalance(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	for _, initial := range []int64{0, 10000, math.MaxInt64} {
		account, err := store.OpenAccount(ctx, initial)
		if err != nil {
			t.Fatal(err)
		}
		if account.ID == "" || account.InitialBalanceCents != initial || account.BalanceCents != initial {
			t.Fatalf("unexpected account: %+v", account)
		}
		balance, err := store.Balance(ctx, account.ID)
		if err != nil || balance != initial {
			t.Fatalf("balance = %d, err = %v; want %d", balance, err, initial)
		}
	}
	if _, err := store.OpenAccount(ctx, -1); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("negative opening balance error = %v", err)
	}
}

func TestBalanceUnknownAccount(t *testing.T) {
	store := testStore(t)
	_, err := store.Balance(context.Background(), "missing")
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("error = %v; want ErrAccountNotFound", err)
	}
}

func TestHistoryEmptyAndMissingAccount(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	account, err := store.OpenAccount(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	history, err := store.History(ctx, account.ID)
	if err != nil || len(history) != 0 {
		t.Fatalf("new account history = %+v, err = %v", history, err)
	}
	_, err = store.History(ctx, "missing")
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("missing account history error = %v", err)
	}
}

func TestTransferMovesExactCentsAndWritesTwoEntries(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	from, _ := store.OpenAccount(ctx, 100)
	to, _ := store.OpenAccount(ctx, 10)
	transfer, err := store.Transfer(ctx, TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 25, IdempotencyKey: "first"})
	if err != nil || transfer.ID == "" || transfer.Replay {
		t.Fatalf("transfer = %+v, err = %v", transfer, err)
	}
	fromBalance, _ := store.Balance(ctx, from.ID)
	toBalance, _ := store.Balance(ctx, to.ID)
	if fromBalance != 75 || toBalance != 35 {
		t.Fatalf("balances = %d, %d; want 75, 35", fromBalance, toBalance)
	}
	for _, accountID := range []string{from.ID, to.ID} {
		history, err := store.History(ctx, accountID)
		if err != nil || len(history) != 1 || history[0].ID != transfer.ID {
			t.Fatalf("history for %s = %+v, err = %v", accountID, history, err)
		}
	}
	var count, sum int64
	if err := store.db.QueryRow("SELECT COUNT(*), SUM(amount_cents) FROM ledger_entries WHERE transfer_id = ?", transfer.ID).Scan(&count, &sum); err != nil || count != 2 || sum != 0 {
		t.Fatalf("entries count=%d sum=%d err=%v", count, sum, err)
	}
}

func TestTransferRejectsInvalidRequestsWithoutMutation(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	from, _ := store.OpenAccount(ctx, 100)
	to, _ := store.OpenAccount(ctx, 0)
	base := TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 50, IdempotencyKey: "base"}
	cases := []struct {
		name string
		edit func(*TransferRequest)
		want error
	}{
		{"insufficient", func(r *TransferRequest) { r.AmountCents = 101 }, ErrInsufficientFunds},
		{"zero", func(r *TransferRequest) { r.AmountCents = 0 }, ErrInvalidAmount},
		{"negative", func(r *TransferRequest) { r.AmountCents = -1 }, ErrInvalidAmount},
		{"self", func(r *TransferRequest) { r.ToAccountID = from.ID }, ErrInvalidAccount},
		{"missing source", func(r *TransferRequest) { r.FromAccountID = "missing" }, ErrAccountNotFound},
		{"missing destination", func(r *TransferRequest) { r.ToAccountID = "missing" }, ErrAccountNotFound},
		{"blank key", func(r *TransferRequest) { r.IdempotencyKey = " " }, ErrInvalidIdempotencyKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := base
			tc.edit(&request)
			_, err := store.Transfer(ctx, request)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v; want %v", err, tc.want)
			}
		})
	}
	balance, _ := store.Balance(ctx, from.ID)
	if balance != 100 {
		t.Fatalf("source balance = %d; want 100", balance)
	}
}

func TestTransferRollsBackWhenLedgerInsertFails(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	from, _ := store.OpenAccount(ctx, 100)
	to, _ := store.OpenAccount(ctx, 0)
	_, err := store.db.Exec(`CREATE TRIGGER fail_credit BEFORE INSERT ON ledger_entries WHEN NEW.amount_cents > 0 BEGIN SELECT RAISE(ABORT, 'injected ledger failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Transfer(ctx, TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 40, IdempotencyKey: "rollback"})
	if err == nil {
		t.Fatal("expected injected failure")
	}
	fromBalance, _ := store.Balance(ctx, from.ID)
	toBalance, _ := store.Balance(ctx, to.ID)
	var transfers, entries int
	_ = store.db.QueryRow("SELECT COUNT(*) FROM transfers").Scan(&transfers)
	_ = store.db.QueryRow("SELECT COUNT(*) FROM ledger_entries").Scan(&entries)
	if fromBalance != 100 || toBalance != 0 || transfers != 0 || entries != 0 {
		t.Fatalf("rollback left balances %d/%d, transfers %d, entries %d", fromBalance, toBalance, transfers, entries)
	}
}

func TestTransferRejectsReceiverOverflow(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	from, _ := store.OpenAccount(ctx, 1)
	to, _ := store.OpenAccount(ctx, math.MaxInt64)
	_, err := store.Transfer(ctx, TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 1, IdempotencyKey: "overflow"})
	if !errors.Is(err, ErrBalanceOverflow) {
		t.Fatalf("error = %v; want ErrBalanceOverflow", err)
	}
	fromBalance, _ := store.Balance(ctx, from.ID)
	toBalance, _ := store.Balance(ctx, to.ID)
	if fromBalance != 1 || toBalance != math.MaxInt64 {
		t.Fatalf("balances after rejection = %d/%d", fromBalance, toBalance)
	}
}

func TestTransferIdempotentReplayAndConflict(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	from, _ := store.OpenAccount(ctx, 100)
	to, _ := store.OpenAccount(ctx, 0)
	request := TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 100, IdempotencyKey: "once"}
	first, err := store.Transfer(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Transfer(ctx, request)
	if err != nil || !second.Replay || second.ID != first.ID {
		t.Fatalf("replay = %+v, err = %v, first = %+v", second, err, first)
	}
	request.AmountCents = 99
	_, err = store.Transfer(ctx, request)
	if !errors.Is(err, ErrIdempotencyPayloadMismatch) {
		t.Fatalf("mismatched payload error = %v", err)
	}
	fromBalance, _ := store.Balance(ctx, from.ID)
	toBalance, _ := store.Balance(ctx, to.ID)
	if fromBalance != 0 || toBalance != 100 {
		t.Fatalf("replay changed balances to %d/%d", fromBalance, toBalance)
	}
}

func TestFailedTransferDoesNotReserveIdempotencyKey(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	from, _ := store.OpenAccount(ctx, 0)
	to, _ := store.OpenAccount(ctx, 0)
	donor, _ := store.OpenAccount(ctx, 50)
	request := TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 50, IdempotencyKey: "later-success"}
	if _, err := store.Transfer(ctx, request); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("first attempt error=%v", err)
	}
	if _, err := store.Transfer(ctx, TransferRequest{FromAccountID: donor.ID, ToAccountID: from.ID, AmountCents: 50, IdempotencyKey: "fund-source"}); err != nil {
		t.Fatal(err)
	}
	result, err := store.Transfer(ctx, request)
	if err != nil || result.Replay || result.ID == "" {
		t.Fatalf("retry after funding=%+v err=%v", result, err)
	}
	fromBalance, _ := store.Balance(ctx, from.ID)
	toBalance, _ := store.Balance(ctx, to.ID)
	if fromBalance != 0 || toBalance != 50 {
		t.Fatalf("balances after retry=%d/%d", fromBalance, toBalance)
	}
}

func TestHistoryOrdersFractionalTimesAfterWholeSecond(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	from, _ := store.OpenAccount(ctx, 100)
	to, _ := store.OpenAccount(ctx, 0)
	first, err := store.Transfer(ctx, TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 10, IdempotencyKey: "first-time"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Transfer(ctx, TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 10, IdempotencyKey: "second-time"})
	if err != nil {
		t.Fatal(err)
	}
	third, err := store.Transfer(ctx, TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 10, IdempotencyKey: "third-time"})
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ id, stamp string }{
		{first.ID, "2026-01-01T00:00:00Z"},
		{second.ID, "2026-01-01T00:00:00.12Z"},
		{third.ID, "2026-01-01T00:00:00.123Z"},
	} {
		if _, err := store.db.Exec("UPDATE transfers SET created_at = ? WHERE id = ?", fixture.stamp, fixture.id); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec("UPDATE ledger_entries SET created_at = ? WHERE transfer_id = ?", fixture.stamp, fixture.id); err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.History(ctx, from.ID)
	if err != nil || len(history) != 3 || history[0].ID != first.ID || history[1].ID != second.ID || history[2].ID != third.ID {
		t.Fatalf("history order = %+v, err = %v", history, err)
	}
}

func TestReadersUseSeparateConfiguredConnectionDuringWrite(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	account, err := store.OpenAccount(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer writer.ExecContext(context.Background(), "ROLLBACK")
	if _, err := writer.ExecContext(ctx, "UPDATE accounts SET balance_cents = 99 WHERE id = ?", account.ID); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	reader, err := store.db.Conn(readCtx)
	if err != nil {
		t.Fatalf("reader could not use a separate connection while writer held a transaction: %v", err)
	}
	defer reader.Close()
	var balance, foreignKeys, busyTimeout int
	if err := reader.QueryRowContext(readCtx, "SELECT balance_cents FROM accounts WHERE id = ?", account.ID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if err := reader.QueryRowContext(readCtx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := reader.QueryRowContext(readCtx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if balance != 100 || foreignKeys != 1 || busyTimeout != 10000 {
		t.Fatalf("reader saw balance=%d foreign_keys=%d busy_timeout=%d", balance, foreignKeys, busyTimeout)
	}
}

func TestReplacementConnectionKeepsRequiredPragmas(t *testing.T) {
	store := testStore(t)
	store.db.SetMaxIdleConns(0)
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var foreignKeys, busyTimeout int
	var journalMode string
	for _, check := range []struct {
		query string
		value any
	}{
		{"PRAGMA foreign_keys", &foreignKeys},
		{"PRAGMA busy_timeout", &busyTimeout},
		{"PRAGMA journal_mode", &journalMode},
	} {
		if err := conn.QueryRowContext(context.Background(), check.query).Scan(check.value); err != nil {
			t.Fatal(err)
		}
	}
	if foreignKeys != 1 || busyTimeout != 10000 || journalMode != "wal" {
		t.Fatalf("replacement connection foreign_keys=%d busy_timeout=%d journal_mode=%s", foreignKeys, busyTimeout, journalMode)
	}
}

func TestSQLiteMoneyConstraintsRejectInvalidRows(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	from, err := store.OpenAccount(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	to, err := store.OpenAccount(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := store.Transfer(ctx, TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 1, IdempotencyKey: "constraint-key"})
	if err != nil {
		t.Fatal(err)
	}
	store.db.SetMaxIdleConns(0)
	for _, tc := range []struct {
		name  string
		query string
		args  []any
	}{
		{"negative balance", "INSERT INTO accounts(id, initial_balance_cents, balance_cents, created_at) VALUES ('negative', 0, -1, 'now')", nil},
		{"fractional cents", "INSERT INTO accounts(id, initial_balance_cents, balance_cents, created_at) VALUES ('fractional', 0, 1.5, 'now')", nil},
		{"orphan transfer", "INSERT INTO transfers(id, idempotency_key, from_account_id, to_account_id, amount_cents, created_at) VALUES ('orphan', 'orphan-key', 'missing', ?, 1, 'now')", []any{to.ID}},
		{"duplicate idempotency key", "INSERT INTO transfers(id, idempotency_key, from_account_id, to_account_id, amount_cents, created_at) VALUES ('duplicate', 'constraint-key', ?, ?, 1, 'now')", []any{from.ID, to.ID}},
		{"zero transfer", "INSERT INTO transfers(id, idempotency_key, from_account_id, to_account_id, amount_cents, created_at) VALUES ('zero', 'zero-key', ?, ?, 0, 'now')", []any{from.ID, to.ID}},
		{"self transfer", "INSERT INTO transfers(id, idempotency_key, from_account_id, to_account_id, amount_cents, created_at) VALUES ('self', 'self-key', ?, ?, 1, 'now')", []any{from.ID, from.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.db.ExecContext(ctx, tc.query, tc.args...); err == nil {
				t.Fatal("invalid direct SQL unexpectedly succeeded")
			}
		})
	}
	var transfers int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM transfers").Scan(&transfers); err != nil || transfers != 1 {
		t.Fatalf("transfers=%d want=1 err=%v", transfers, err)
	}
	if valid.ID == "" {
		t.Fatal("valid transfer has no ID")
	}
}

func TestCommittedTransferAndReplaySurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "durable.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	from, err := store.OpenAccount(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	to, err := store.OpenAccount(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	request := TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 75, IdempotencyKey: "durable-key"}
	first, err := store.Transfer(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replay, err := reopened.Transfer(ctx, request)
	if err != nil || !replay.Replay || replay.ID != first.ID {
		t.Fatalf("replay after reopen = %+v, err = %v", replay, err)
	}
	fromBalance, err := reopened.Balance(ctx, from.ID)
	if err != nil {
		t.Fatal(err)
	}
	toBalance, err := reopened.Balance(ctx, to.ID)
	if err != nil {
		t.Fatal(err)
	}
	history, err := reopened.History(ctx, to.ID)
	if err != nil || fromBalance != 25 || toBalance != 75 || len(history) != 1 || history[0].ID != first.ID {
		t.Fatalf("persisted balances=%d/%d history=%+v err=%v", fromBalance, toBalance, history, err)
	}
}

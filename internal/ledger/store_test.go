package ledger

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
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

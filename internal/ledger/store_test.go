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

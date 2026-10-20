package ledger

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestCompetingTransfersCannotDoubleSpend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx := context.Background()
	from, _ := first.OpenAccount(ctx, 100)
	to, _ := first.OpenAccount(ctx, 0)
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for i, store := range []*Store{first, second} {
		go func(i int, store *Store) {
			ready.Done()
			<-start
			_, err := store.Transfer(ctx, TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 100, IdempotencyKey: fmt.Sprintf("race-%d", i)})
			results <- err
		}(i, store)
	}
	ready.Wait()
	close(start)
	successes, insufficient := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrInsufficientFunds):
			insufficient++
		default:
			t.Fatalf("unexpected competing transfer error: %v", err)
		}
	}
	fromBalance, _ := first.Balance(ctx, from.ID)
	toBalance, _ := first.Balance(ctx, to.ID)
	var transfers, entries int
	_ = first.db.QueryRow("SELECT COUNT(*) FROM transfers").Scan(&transfers)
	_ = first.db.QueryRow("SELECT COUNT(*) FROM ledger_entries").Scan(&entries)
	if successes != 1 || insufficient != 1 || fromBalance != 0 || toBalance != 100 || transfers != 1 || entries != 2 {
		t.Fatalf("success=%d insufficient=%d balances=%d/%d transfers=%d entries=%d", successes, insufficient, fromBalance, toBalance, transfers, entries)
	}
}

func TestConcurrentSameKeyMovesMoneyOnce(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	from, _ := store.OpenAccount(ctx, 50000)
	to, _ := store.OpenAccount(ctx, 0)
	request := TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 10000, IdempotencyKey: "retry-storm-key-1"}
	start := make(chan struct{})
	type result struct {
		transfer Transfer
		err      error
	}
	results := make(chan result, 20)
	var ready sync.WaitGroup
	ready.Add(20)
	for range 20 {
		go func() {
			ready.Done()
			<-start
			transfer, err := store.Transfer(ctx, request)
			results <- result{transfer, err}
		}()
	}
	ready.Wait()
	close(start)
	var firstID string
	for range 20 {
		got := <-results
		if got.err != nil || got.transfer.ID == "" {
			t.Fatalf("same-key error=%v transfer=%+v", got.err, got.transfer)
		}
		if firstID == "" {
			firstID = got.transfer.ID
		} else if got.transfer.ID != firstID {
			t.Fatalf("replays returned different transfer IDs: %s and %s", firstID, got.transfer.ID)
		}
	}
	fromBalance, _ := store.Balance(ctx, from.ID)
	toBalance, _ := store.Balance(ctx, to.ID)
	var transfers, entries int
	_ = store.db.QueryRow("SELECT COUNT(*) FROM transfers").Scan(&transfers)
	_ = store.db.QueryRow("SELECT COUNT(*) FROM ledger_entries").Scan(&entries)
	if fromBalance != 40000 || toBalance != 10000 || transfers != 1 || entries != 2 {
		t.Fatalf("balances=%d/%d transfers=%d entries=%d", fromBalance, toBalance, transfers, entries)
	}
}

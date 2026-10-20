package ledger

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
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
	path := filepath.Join(t.TempDir(), "replay.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	otherStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer otherStore.Close()
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
	for i := range 20 {
		go func(i int) {
			ready.Done()
			<-start
			caller := store
			if i%2 == 1 {
				caller = otherStore
			}
			transfer, err := caller.Transfer(ctx, request)
			results <- result{transfer, err}
		}(i)
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

func TestConcurrentConflictingPayloadsKeepOneKeyOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conflict.db")
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
	type result struct {
		amount int64
		err    error
	}
	results := make(chan result, 2)
	for i, store := range []*Store{first, second} {
		amount := int64(40 + 20*i)
		go func(store *Store, amount int64) {
			<-start
			_, err := store.Transfer(ctx, TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: amount, IdempotencyKey: "contested-key"})
			results <- result{amount, err}
		}(store, amount)
	}
	close(start)
	successes, conflicts := 0, 0
	var winningAmount int64
	for range 2 {
		got := <-results
		switch {
		case got.err == nil:
			successes++
			winningAmount = got.amount
		case errors.Is(got.err, ErrIdempotencyPayloadMismatch):
			conflicts++
		default:
			t.Fatalf("unexpected contested-key result: %v", got.err)
		}
	}
	fromBalance, _ := first.Balance(ctx, from.ID)
	toBalance, _ := first.Balance(ctx, to.ID)
	var transfers, entries int
	_ = first.db.QueryRow("SELECT COUNT(*) FROM transfers").Scan(&transfers)
	_ = first.db.QueryRow("SELECT COUNT(*) FROM ledger_entries").Scan(&entries)
	if successes != 1 || conflicts != 1 || fromBalance != 100-winningAmount || toBalance != winningAmount || transfers != 1 || entries != 2 {
		t.Fatalf("success=%d conflict=%d winner=%d balances=%d/%d transfers=%d entries=%d", successes, conflicts, winningAmount, fromBalance, toBalance, transfers, entries)
	}
}

func TestFiftyCompetingTransfersDrainOnlyOnce(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	from, _ := store.OpenAccount(ctx, 10000)
	to, _ := store.OpenAccount(ctx, 0)
	start := make(chan struct{})
	results := make(chan error, 50)
	var ready sync.WaitGroup
	ready.Add(50)
	for i := range 50 {
		go func(i int) {
			ready.Done()
			<-start
			_, err := store.Transfer(ctx, TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 10000, IdempotencyKey: fmt.Sprintf("full-%d", i)})
			results <- err
		}(i)
	}
	ready.Wait()
	close(start)
	successes, insufficient := 0, 0
	for range 50 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrInsufficientFunds):
			insufficient++
		default:
			t.Fatalf("unexpected result: %v", err)
		}
	}
	fromBalance, _ := store.Balance(ctx, from.ID)
	toBalance, _ := store.Balance(ctx, to.ID)
	if successes != 1 || insufficient != 49 || fromBalance != 0 || toBalance != 10000 {
		t.Fatalf("success=%d insufficient=%d balances=%d/%d", successes, insufficient, fromBalance, toBalance)
	}
}

func TestPartialDrainAndBidirectionalTraffic(t *testing.T) {
	t.Run("partial drain", func(t *testing.T) {
		store := testStore(t)
		ctx := context.Background()
		from, _ := store.OpenAccount(ctx, 10000)
		to, _ := store.OpenAccount(ctx, 0)
		results := runConcurrentTransfers(store, 20, func(i int) TransferRequest {
			return TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 1000, IdempotencyKey: fmt.Sprintf("partial-%d", i)}
		})
		successes, insufficient := countResults(t, results)
		fromBalance, _ := store.Balance(ctx, from.ID)
		toBalance, _ := store.Balance(ctx, to.ID)
		if successes != 10 || insufficient != 10 || fromBalance != 0 || toBalance != 10000 {
			t.Fatalf("success=%d insufficient=%d balances=%d/%d", successes, insufficient, fromBalance, toBalance)
		}
	})
	t.Run("bidirectional", func(t *testing.T) {
		store := testStore(t)
		ctx := context.Background()
		a, _ := store.OpenAccount(ctx, 100000)
		b, _ := store.OpenAccount(ctx, 100000)
		results := runConcurrentTransfers(store, 100, func(i int) TransferRequest {
			from, to := a.ID, b.ID
			if i >= 50 {
				from, to = b.ID, a.ID
			}
			return TransferRequest{FromAccountID: from, ToAccountID: to, AmountCents: 100, IdempotencyKey: fmt.Sprintf("bidirectional-%d", i)}
		})
		successes, insufficient := countResults(t, results)
		aBalance, _ := store.Balance(ctx, a.ID)
		bBalance, _ := store.Balance(ctx, b.ID)
		var transfers, entries int
		_ = store.db.QueryRow("SELECT COUNT(*) FROM transfers").Scan(&transfers)
		_ = store.db.QueryRow("SELECT COUNT(*) FROM ledger_entries").Scan(&entries)
		if successes != 100 || insufficient != 0 || aBalance != 100000 || bBalance != 100000 || transfers != 100 || entries != 200 {
			t.Fatalf("success=%d insufficient=%d balances=%d/%d transfers=%d entries=%d", successes, insufficient, aBalance, bBalance, transfers, entries)
		}
	})
}

func runConcurrentTransfers(store *Store, count int, makeRequest func(int) TransferRequest) []error {
	start := make(chan struct{})
	results := make(chan error, count)
	var ready sync.WaitGroup
	ready.Add(count)
	for i := range count {
		go func(i int) {
			ready.Done()
			<-start
			_, err := store.Transfer(context.Background(), makeRequest(i))
			results <- err
		}(i)
	}
	ready.Wait()
	close(start)
	errors := make([]error, 0, count)
	for range count {
		errors = append(errors, <-results)
	}
	return errors
}

func countResults(t *testing.T, results []error) (int, int) {
	t.Helper()
	successes, insufficient := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrInsufficientFunds):
			insufficient++
		default:
			t.Fatalf("unexpected transfer error: %v", err)
		}
	}
	return successes, insufficient
}

func TestSeededConcurrentAuditPreservesEveryCent(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	accounts := make([]Account, 10)
	for i := range accounts {
		account, err := store.OpenAccount(ctx, 10000)
		if err != nil {
			t.Fatal(err)
		}
		accounts[i] = account
	}
	random := rand.New(rand.NewSource(20261020))
	requests := make([]TransferRequest, 500)
	for i := range requests {
		from := random.Intn(len(accounts))
		to := (from + 1 + random.Intn(len(accounts)-1)) % len(accounts)
		requests[i] = TransferRequest{FromAccountID: accounts[from].ID, ToAccountID: accounts[to].ID, AmountCents: int64(1 + random.Intn(15000)), IdempotencyKey: fmt.Sprintf("audit-%d", i)}
	}
	results := runConcurrentTransfers(store, len(requests), func(i int) TransferRequest { return requests[i] })
	successes, insufficient := countResults(t, results)
	if successes == 0 || insufficient == 0 {
		t.Fatalf("audit lacks both accepted and rejected transfers: success=%d insufficient=%d", successes, insufficient)
	}
	var total int64
	for _, account := range accounts {
		balance, err := store.Balance(ctx, account.ID)
		if err != nil || balance < 0 {
			t.Fatalf("account %s balance=%d err=%v", account.ID, balance, err)
		}
		history, err := store.History(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		reconstructed := account.InitialBalanceCents
		for _, transfer := range history {
			if transfer.FromAccountID == account.ID {
				reconstructed -= transfer.AmountCents
			} else {
				reconstructed += transfer.AmountCents
			}
		}
		if reconstructed != balance {
			t.Fatalf("account %s reconstructed=%d balance=%d", account.ID, reconstructed, balance)
		}
		total += balance
	}
	if total != 100000 {
		t.Fatalf("global balance=%d want 100000", total)
	}
	var transferCount, entryCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM transfers").Scan(&transferCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM ledger_entries").Scan(&entryCount); err != nil {
		t.Fatal(err)
	}
	if transferCount != successes || entryCount != 2*successes {
		t.Fatalf("success=%d transfers=%d entries=%d", successes, transferCount, entryCount)
	}
	var unbalanced int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM (
		SELECT transfer_id FROM ledger_entries GROUP BY transfer_id
		HAVING COUNT(*) <> 2 OR SUM(amount_cents) <> 0
	)`).Scan(&unbalanced); err != nil || unbalanced != 0 {
		t.Fatalf("unbalanced transfers=%d err=%v", unbalanced, err)
	}
}

package ledger

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func benchmarkStore(b *testing.B) *Store {
	b.Helper()
	store, err := Open(filepath.Join(b.TempDir(), "benchmark.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	return store
}

func BenchmarkBalanceParallel(b *testing.B) {
	for _, poolSize := range []int{1, 8} {
		b.Run(fmt.Sprintf("pool%d", poolSize), func(b *testing.B) {
			store := benchmarkStore(b)
			store.db.SetMaxOpenConns(poolSize)
			account, err := store.OpenAccount(context.Background(), 10000)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if balance, err := store.Balance(context.Background(), account.ID); err != nil || balance != 10000 {
						b.Errorf("balance=%d err=%v", balance, err)
						return
					}
				}
			})
		})
	}
}

func BenchmarkHistory1000(b *testing.B) {
	store := benchmarkStore(b)
	ctx := context.Background()
	from, err := store.OpenAccount(ctx, 1000)
	if err != nil {
		b.Fatal(err)
	}
	to, err := store.OpenAccount(ctx, 0)
	if err != nil {
		b.Fatal(err)
	}
	for i := range 1000 {
		if _, err := store.Transfer(ctx, TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 1, IdempotencyKey: fmt.Sprintf("bench-%d", i)}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		history, err := store.History(ctx, to.ID)
		if err != nil || len(history) != 1000 {
			b.Fatalf("history length=%d err=%v", len(history), err)
		}
	}
}

func BenchmarkTransferSequential(b *testing.B) {
	store := benchmarkStore(b)
	ctx := context.Background()
	from, err := store.OpenAccount(ctx, int64(b.N))
	if err != nil {
		b.Fatal(err)
	}
	to, err := store.OpenAccount(ctx, 0)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if _, err := store.Transfer(ctx, TransferRequest{FromAccountID: from.ID, ToAccountID: to.ID, AmountCents: 1, IdempotencyKey: fmt.Sprintf("bench-%d", i)}); err != nil {
			b.Fatal(err)
		}
	}
}

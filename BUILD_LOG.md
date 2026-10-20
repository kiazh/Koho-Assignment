# KOHO Move the Money — Build Log

## Goal and approach

Build the four account/transfer operations in Go and SQLite, then prove the money rules against a real database. AI accelerated the schema, HTTP handlers, fault-injection test, and concurrency harness. I worked in small verified commits and reviewed the generated logic.

## Slice record

| Commit | What changed; evidence |
| --- | --- |
| `3f9e5b5` | Exact-cent accounts and schema; opening-balance tests failed before implementation, then passed. |
| `c627f7c` | Balance/history reads; missing-account and empty-history tests passed. |
| `1e220f6` | Atomic transfer; an injected ledger failure rolled back both balances and all records. |
| `693f4bc` | Replay/conflict and synchronized races; two competing transfers produced one success and one insufficient-funds result. Twenty same-key callers moved money once. |
| `828c502` | Strict HTTP JSON and error codes; API tests and server build passed. |
| `e5a86a4` | 50-way, partial-drain, bidirectional, and seeded 500-attempt audits; ten focused race-enabled runs passed. |
| `b92f86d` | README, stack decisions, and log; manual API smoke showed 201 creation, 200 replay, 7500/2500 balances, and one history record. |
| `0a1da62` | Review fixes; history-order and active-request shutdown tests each failed before the fix and passed after it. |

Every code slice passed `go vet ./...` and `go test -race -count=1 ./...` before its commit. A fresh clone passed those commands after the review fixes.

## AI mistakes and rejected ideas

The strongest AI-generated code mistake was sorting variable-width `RFC3339Nano` timestamps as text: `...00.123Z` sorts before `...00.12Z`, reversing time. Fresh review caught it. `TestHistoryOrdersFractionalTimesAfterWholeSecond` reproduced the wrong order; sorting parsed instants fixed it. Review also caught shutdown returning before an active request drained; a blocked-request test verified that fix.

I also chose the newest SQLite driver without checking its Go floor. `go mod tidy -go=1.22.0` exposed a transitive Go 1.26 requirement. Pinning `modernc.org/sqlite v1.34.5` restored the Go 1.22 target.

I rejected the planning documents' `SELECT ... FOR UPDATE` suggestion: SQLite has no such row-lock syntax. `BEGIN IMMEDIATE` plus conditional debit fits SQLite. I cut UI, Docker/PostgreSQL setup, and floating-point money to focus on correctness.

## Risks and fifth hour

SQLite permits one writer at a time. History has no pagination, and successful idempotency keys are retained indefinitely. I would spend a fifth hour on bounded history pagination and an operator-facing balance/ledger reconciliation command. The API has no auth because the assignment excludes it; it is bound to localhost for demonstration.

## Follow-up audit

After the initial push, I added repeatable benchmarks before changing performance code. On this machine the one-connection store handled parallel balance reads in about 24–25 µs/op; an eight-connection benchmark variant took about 5.9–7.0 µs/op. A 1,000-row history read took about 3.7 ms, and a sequential transfer about 0.89 ms. Those numbers are local measurements, not universal throughput claims.

The bottleneck was `SetMaxOpenConns(1)`: a held write connection blocked reads despite WAL. A new test failed with `context deadline exceeded`; with eight pooled connections and per-connection foreign-key/busy-timeout PRAGMAs it passed and saw the last committed balance. Ten shuffled race-suite runs passed after the change. I kept SQLite's one-writer transaction discipline; the transfer benchmark did not materially change.

I then strengthened the adversarial proof: same-key callers now use two store handles, different payloads race for one key, a failed key succeeds after funding, 20 HTTP requests compete for one balance, and the 500-attempt audit counts transfer/entry rows against successes. These checks passed repeated race-enabled runs and found no further money mutation defect. I did not add a generic retry loop because the current busy timeout and these tests already cover the observed contention.

Two external reports reviewed an older Repomix snapshot without running Go. I checked their claims against the current branch. The per-connection PRAGMA gap was already fixed above; the audit gap was real. I now reconcile each stored balance with signed entries and require each transfer's debit and credit to match its accounts and exact amount. Deliberately corrupted 39-cent and missing-entry fixtures are detected. The 100-transfer race test now has a bounded deadline; three local race-enabled runs completed in 0.49–0.58 seconds. This is a measurement, not a portable timing guarantee.

I also forced SQLite to replace its physical connection and read back WAL, foreign keys, and the busy timeout through the actual Go driver. Direct SQL probes then confirmed negative balances, fractional stored cents, orphan transfers, duplicate keys, zero transfers, and self-transfers are rejected. Ten race-enabled runs passed. The reports' missing-`go.sum` claim came from the export: `go.sum` is tracked in this repository.

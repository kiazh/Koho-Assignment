# KOHO Move the Money — Full Code Review

> **Historical external review.** This report examined an older repository export and did not run the Go suite. Some findings were fixed or disproved in later commits; see the [implementation journal](IMPLEMENTATION_JOURNAL.md) for current evidence.

- **Reviewed artifact:** `repomix-output-kiazh-Koho-Assignment.xml`
- **Review date:** 20 October 2026
- **Scope:** Every file in the packed snapshot, with line-level review of the Go ledger, HTTP API, server, and tests.
- **Method:** Static review. The suite was **not executed**. This environment has no Go toolchain, the pack has no `go.sum`, and module dependencies are not vendored. Claims that tests pass come only from `BUILD_LOG.md`, not from a fresh run.

---

## 1. Verdict

The core money path is the right SQLite design for this assignment. Integer cents, `BEGIN IMMEDIATE`, a conditional debit, `CHECK (balance_cents >= 0)`, a unique idempotency key, and a deferred rollback are all in place. The four invariants hold on the paths the tests actually exercise, assuming the build log is accurate.

The real gaps are not a naive read-then-write race. They are:

1. A commit whose outcome is uncertain is reported as a hard failure, with no idempotency-key lookup.
2. `foreign_keys` and `busy_timeout` are set once on a pooled connection instead of on every connection.
3. Several claimed database backstops are untested, so a regression would still leave `go test` green.
4. One shutdown test can flake, and `go.sum` is missing from this snapshot.

None of those are a proven double-spend in the happy path. The first one can become a double-charge if a client retries a timed-out or 500 response with a new key.

---

## 2. What was reviewed

| Path | Lines | Role |
| --- | ---: | --- |
| `cmd/server/main.go` | 73 | Process entry, listen, graceful shutdown |
| `cmd/server/main_test.go` | 71 | Shutdown-drain test |
| `internal/api/handler.go` | 127 | Routes, strict JSON, status mapping |
| `internal/api/handler_test.go` | 108 | HTTP contract tests |
| `internal/ledger/model.go` | 40 | Types and sentinel errors |
| `internal/ledger/store.go` | 133 | SQLite open, schema, account reads/writes |
| `internal/ledger/transfer.go` | 123 | Validation and atomic transfer |
| `internal/ledger/store_test.go` | 226 | Persistence, validation, rollback, replay |
| `internal/ledger/concurrency_test.go` | 277 | Contention and conservation audit |
| `go.mod` | 17 | Go 1.22.0, `modernc.org/sqlite v1.34.5` |
| `README.md`, `BUILD_LOG.md`, `TECH_STACK_DECISIONS.md` | — | Contract and claims |
| Planning docs (`plan.md`, agents notes, definition of done, assignment text) | — | Compared against the code, not treated as bugs when the code deliberately rejected them |

`go.sum` is not in the directory listing of the pack. `.gitignore` only ignores `*.db`, `*.db-shm`, `*.db-wal`, and `.superpowers/`.

---

## 3. Invariants and how the code enforces them

### 3.1 No negative balance

- `OpenAccount` rejects `initialCents < 0` with `ErrInvalidAmount` before SQL.
- Schema: `CHECK (initial_balance_cents >= 0)` and `CHECK (balance_cents >= 0)` on a `STRICT` table.
- `Transfer` reads the source balance under `BEGIN IMMEDIATE`, rejects `sourceBalance < amount`, then runs:

```sql
UPDATE accounts
SET balance_cents = balance_cents - ?
WHERE id = ? AND balance_cents >= ?
```

- `RowsAffected() != 1` maps to `ErrInsufficientFunds`.
- Zero is allowed. The check is `>= 0`, and a debit of the exact balance lands on 0.

This is safe against the classic lost-update race **as long as the write lock is actually held before the read**. `BEGIN IMMEDIATE` does that. `SetMaxOpenConns(1)` also serializes writers inside one `Store`. The two-handle test is what actually proves the file lock.

### 3.2 Atomic transfer

One `sql.Conn` runs `BEGIN IMMEDIATE`, then debit, credit, transfer insert, and two ledger inserts, then `COMMIT`. Every error return relies on:

```go
defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
```

Using `context.Background()` is correct. A canceled request context must not block rollback.

`TestTransferRollsBackWhenLedgerInsertFails` installs a trigger that aborts the positive ledger insert after the debit. `RAISE(ABORT, ...)` undoes only the current statement; the Go rollback is what undoes the debit, the transfer row, and the negative entry. That is the right test, and the code matches it.

`COMMIT` then a deferred `ROLLBACK` does not undo a successful commit. SQLite returns "no transaction is active", and that error is ignored.

### 3.3 Idempotency

- Empty or whitespace-only keys, and keys longer than 255 bytes, are rejected before a transaction.
- The key is inserted only in the successful transaction, so a rejected attempt does not reserve it.
- `idempotency_key TEXT NOT NULL UNIQUE` is the durable backstop.
- Replay compares `from_account_id`, `to_account_id`, and `amount_cents`. Mismatch returns `ErrIdempotencyPayloadMismatch` before any balance check, which is the right priority.
- Replay returns the original id even if the source can no longer afford the amount. `Replay` is `json:"-"`; the HTTP layer signals it with 200 vs 201.
- Same-key concurrency is serialized by `BEGIN IMMEDIATE`, so the second caller sees the committed row instead of racing the insert.

### 3.4 Exact integer cents

- Domain fields are `int64`.
- JSON decodes straight into `int64` or `*int64`. `encoding/json` rejects `10.5`, `1e2`, numeric strings, and values outside `int64`.
- `openAccount` uses `*int64`, so a missing field is 400 and `0` is a valid opening balance.
- Transfer amount uses a plain `int64`. Missing and zero both become `ErrInvalidAmount`, which is correct because a zero transfer is invalid.
- Receiver overflow is checked in Go before the credit:

```go
if destinationBalance > math.MaxInt64-request.AmountCents {
    return Transfer{}, ErrBalanceOverflow
}
```

Boundary check:

| Destination | Amount | Result |
| --- | ---: | --- |
| `MaxInt64` | 1 | overflow (`MaxInt64 > MaxInt64-1`) |
| `MaxInt64 - amount` | `amount` | allowed, result is `MaxInt64` |
| `MaxInt64 - amount + 1` | `amount` | overflow |

`-request.AmountCents` cannot overflow: `AmountCents <= 0` is rejected first, and `MinInt64` is negative. Negating `MaxInt64` fits in `int64`.

If that Go check were wrong, SQLite integer overflow becomes a REAL, and a `STRICT` INTEGER column should reject the update. That would roll back. It is a backstop, not the primary check, and it is not tested.

---

## 4. Findings

Severity is impact on money correctness or on whether a reviewer can trust the proof, not style.

### 4.1 High — uncertain commit is reported as failure

**Where:** `internal/ledger/transfer.go`, both `COMMIT` sites; `internal/api/handler.go` `writeError`.

**What happens:**

```go
if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
    return Transfer{}, err
}
```

`writeError` maps anything that is not a known sentinel to HTTP 500 `internal_error`. The body does not say "retry this key".

**Why it matters:** `TECH_STACK_DECISIONS.md` already states the contract: a commit error can leave the client unsure, and the client should retry the same key. The code never does the follow-up read the plan required:

> If commit's outcome is uncertain, look up the idempotency key before reporting a retryable error. Never claim failure after a commit that may have succeeded.

Two concrete windows:

1. The request context is canceled while `COMMIT` is in flight. `ExecContext` can return `context.Canceled` even if SQLite already committed.
2. `WriteTimeout` is 15 seconds. The deadline covers handler work, not just the response bytes. Callers queue on the single pooled connection. If the debit commits and the response write then fails, the client sees a transport error, not 201.

If that client generates a **new** idempotency key, the transfer runs again. Reusing the same key is safe. The API does not force or even hint at that.

`SQLITE_BUSY` after the busy timeout, and a unique-constraint error if the write lock were ever skipped, fall into the same 500 bucket. Busy **before** the debit is safe, because nothing was committed. An error **after** a commit that might have succeeded is not.

**Fix:**

1. On `COMMIT` error, query `transfers` by `idempotency_key` on a fresh read.
2. If the row exists and the three fields match, return that transfer with `Replay: true`.
3. If it does not exist, return a distinct retryable error.
4. Map that error to something other than a generic 500, or document the 500 body as "outcome unknown, replay this key".
5. Do the same lookup for driver errors that can surface after the insert statements have been sent.

Do not treat 500 or a dropped connection as permission to mint a new key.

### 4.2 Medium — pragmas are not bound to every connection

**Where:** `internal/ledger/store.go` `Open`.

```go
db, err := sql.Open("sqlite", path)
db.SetMaxOpenConns(1)
// then Exec:
//   PRAGMA journal_mode = WAL
//   PRAGMA foreign_keys = ON
//   PRAGMA busy_timeout = 10000
```

`journal_mode=WAL` is stored in the database file. `foreign_keys` and `busy_timeout` are connection-local. SQLite leaves foreign keys off unless every connection turns them on.

`modernc.org/sqlite` does not reset pragmas when a connection returns to the pool, so the long-lived single connection keeps the settings. `database/sql` can still discard that connection after a failed statement and dial a new one, even when `MaxOpenConns` is 1. The replacement will not have foreign keys or a 10-second busy handler.

**Impact:**

- Lost `busy_timeout`: the second writer gets `SQLITE_BUSY` immediately, which becomes 500. That is availability, not a silent double-spend, because `BEGIN IMMEDIATE` fails before the debit.
- Lost `foreign_keys`: a transfer row can reference a missing account if the Go existence check is ever removed or bypassed. Today the Go check still runs first.

**Fix:** pass a DSN that applies the pragmas on connect, then read them back:

```text
file:ledger.db?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)
```

`modernc.org/sqlite` honors `_pragma=`, not the `mattn/go-sqlite3` `_busy_timeout` / `_fk` query keys. A bare filesystem path will not do this.

Add a test that opens a second `Conn` after the first has been used and asserts:

- `PRAGMA foreign_keys` is `1`
- `PRAGMA busy_timeout` is `10000`
- `PRAGMA journal_mode` is `wal`

No current test reads those values. Deleting the `PRAGMA` lines would not fail CI.

### 4.3 Medium — database backstops are claimed and untested

The README says a `CHECK` prevents negative balances even if application validation regresses, `STRICT` blocks non-integers, and the unique key is the durable idempotency backstop. The tests never poke those constraints directly.

| Claim | What the tests actually hit |
| --- | --- |
| Negative opening balance rejected | Go `if initialCents < 0`, expecting `ErrInvalidAmount`. A removed `CHECK` still passes. |
| Negative balance impossible under concurrency | Application debit plus the race tests. A removed `CHECK` still passes if the Go debit stays. |
| `STRICT` rejects non-integers | Nothing. |
| Foreign keys on | Nothing. |
| Exactly two opposite ledger rows | Application inserts plus the audit query. No schema constraint. Documented, but easy to regress. |
| Unique idempotency key | Application lookup under the write lock. A removed `UNIQUE` might still pass the single-connection tests. |
| `busy_timeout` | Nothing reads it. A flake would show up only as an unexpected error in the two-handle test. |

`TestTransferRollsBackWhenLedgerInsertFails` is the one fault-injection test, and it is good. It does not replace constraint tests.

**Add, at least:**

- Raw `INSERT` of `balance_cents = -1` must fail.
- Raw `INSERT` of a non-integer into a `STRICT` money column must fail.
- Raw `INSERT` of a transfer whose `from_account_id` does not exist must fail **on a newly checked-out connection**.
- Two successful inserts of the same `idempotency_key` must fail.
- `PRAGMA` read-back as above.

### 4.4 Medium — shutdown test can fail for the wrong reason

**Where:** `cmd/server/main_test.go` `TestShutdownWaitsForActiveRequest`.

`net.Listen` succeeds, then two goroutines start: `serve` and `http.Get`. `http.Get` does not retry. If it runs before `Serve` reaches `Accept`, the dial gets connection refused, the handler never closes `started`, and the test fails with "request did not start" after 5 seconds.

That is a scheduler race, more likely on a loaded CI runner. It does not mean shutdown is broken.

The 100-millisecond branch only checks that the server has **not** returned yet. A slow machine can pass that branch even if shutdown is late to start. It is a weak assertion, not a false failure, when the handler is still blocked on `release`.

**Production `serve`:** the wait itself looks correct. `Shutdown` is called from `ctx.Done()`, and `serve` does not return until `shutdownDone` receives, unless `Serve` failed with something other than `http.ErrServerClosed`. The build log says an earlier version returned before the active request finished. This version waits. I did not re-run the test.

**Fix the test:** retry `connection refused` for a short window, or send the request only after a successful dial. On early `t.Fatal`, also `close(release)` so the handler goroutine is not stuck until the process exits.

### 4.5 Low — process exit skips a clean database close

**Where:** `cmd/server/main.go`.

`os.Exit(1)` on open, listen, or serve failure does not run `defer store.Close()` or `defer stop()`. The process is about to die, so the file descriptor is reclaimed. What you skip is a clean SQLite checkpoint. The next open in WAL mode should recover. Still worth returning an exit code from `main` instead of `os.Exit`, so `Close` runs on the listen-failure path.

On shutdown timeout, `serve` returns `Shutdown`'s error and `main` exits without `server.Close()`. Process death drops the socket. An in-flight transaction should roll back when its connection closes. Money stays consistent; the client sees a dropped connection and is back in finding 4.1 if it changes the idempotency key.

### 4.6 Low — credit update does not check `RowsAffected`

**Where:** `internal/ledger/transfer.go`, the receiver `UPDATE`.

The debit checks `RowsAffected() == 1`. The credit does not. There is no delete API, both accounts were just selected, and the write lock is held, so this cannot miss today. If it ever updated 0 rows and the function kept going, the sender would be debited, the ledger would say the receiver was credited, and the stored balance would not move. The transaction would still roll back **only if** that path returned an error.

Check `RowsAffected` on the credit and return an error. Same for the transfer and ledger inserts if you want the invariant to survive a later edit.

### 4.7 Low — oversized body can desync keep-alive

**Where:** `internal/api/handler.go` `decode`.

```go
r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
```

One mebibyte is a limit, but it is not small, and every decode error including `http.MaxBytesError` becomes 400 `invalid_request`. `MaxBytesReader` does not set 413 by itself. If the client sent more than the limit, unread bytes can sit on a keep-alive connection and corrupt the next request. Close the connection on that error, and cap the body at a few kilobytes. The largest legitimate body here is a couple of hundred bytes.

### 4.8 Low — account creation is not idempotent

`POST /accounts` always inserts a new funded account. A client timeout after the insert, followed by a retry, mints money. The assignment does not require idempotent open. It is the other retry trap beside transfers, and it should be one sentence in the README next to the transfer-key rule so a demo client does not "retry until it works".

### 4.9 Low — history ordering footgun

`History` loads rows and sorts parsed instants, then `id`. That is the correct fix for variable-width `RFC3339Nano` text. String order is wrong: `2026-01-01T00:00:00.123Z` sorts before `2026-01-01T00:00:00.12Z`.

The index is still:

```sql
CREATE INDEX IF NOT EXISTS ledger_entries_account_time
ON ledger_entries(account_id, created_at, id)
```

`created_at` there is text. A later "optimization" to `ORDER BY t.created_at, t.id` would restore the bug `TestHistoryOrdersFractionalTimesAfterWholeSecond` was written to kill. Keep the Go sort, or store a fixed-width UTC timestamp.

Tie-break for equal timestamps is random hex `id`, not insertion order. That matches the written plan (`created_at`, then `id`). It is not causal order if two transfers share a nanosecond. Rare, and acceptable if you say so.

### 4.10 Info — HTTP and JSON nits

- Duplicate JSON keys are accepted by `encoding/json`; the last value wins. `DisallowUnknownFields` does not reject duplicates. Low risk for this demo.
- `writeJSON` ignores the encode error after `WriteHeader`. A transfer struct will not fail to encode. An account-create retry after a partial write can still double-open (finding 4.8).
- Canceled requests and driver errors are logged as `request failed` and returned as `internal_error`. Safe before commit. Noisy, and it hides the retry contract.
- `int64` JSON numbers are exact for Go clients. A JavaScript client loses precision above `2^53` (9007199254740992 cents). Opening balances up to `math.MaxInt64` are allowed. Fine for the assignment; unsafe to call a general JSON API without a warning.
- Idempotency keys are trimmed only to reject whitespace-only values. `" key"` and `"key"` are different. That is reasonable for an opaque key. Worth one line in the README so callers do not trim on one side only.
- Empty account ids are `ErrInvalidAccount` (400), not 404. Matches the README.
- Self-transfer is rejected in Go and by `CHECK (from_account_id <> to_account_id)`.
- `Replay` is omitted from JSON. Clients must use the status code. The README says this. Good.

### 4.11 Info — server behavior that is fine

- Default bind is `127.0.0.1:8080`. `ADDR` can override that. The README says not to expose it. There is no auth, and anyone who can reach it can open a funded account. That is the assignment, not a defect, as long as the demo stays on localhost.
- `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, and `IdleTimeout` are set. Good against a slow header. The write timeout is part of finding 4.1, not a reason to remove it.
- Method-specific Go 1.22 routes are correct: `POST /accounts`, `GET /accounts/{id}/balance`, `GET /accounts/{id}/transactions`, `POST /transfers`.
- Unknown fields, trailing JSON, and a second JSON value are rejected. The handler tests cover the account-open cases.
- SQL uses placeholders. No string-built queries in the production path.
- IDs are 16 bytes from `crypto/rand`, hex-encoded. Collision handling is absent and not worth a retry loop at this size. Say that once if someone asks.
- `SetMaxOpenConns(1)` plus a short `BEGIN IMMEDIATE` transaction is a deliberate throughput ceiling. The tech-stack note is honest. Do not "fix" it with a bigger pool unless every new connection sets the pragmas and every transfer still takes the write lock before reading balances.

### 4.12 Info — planning docs vs code

These are not defects. The build log says they were rejected on purpose:

- `SELECT ... FOR UPDATE` is invalid SQLite. Sorting account ids does not create row locks. `BEGIN IMMEDIATE` is the right lock.
- Floating-point and decimal libraries were rejected. `int64` cents is the right call.
- Docker Postgres was rejected so `go test -race ./...` needs no extra services.
- Agents notes mention `busy_timeout` of 5000 ms. The code uses 10000 ms. Stricter, not wrong.
- The "core ledger under 250 lines" target is missed if you count `model.go` + `store.go` + `transfer.go` (about 296 lines including imports and blanks). Not a functional issue.

---

## 5. Request path, step by step

### 5.1 Open account

1. Body limit, `DisallowUnknownFields`, then a second decode that must be `io.EOF`.
2. `initial_balance_cents` must be present (`*int64 != nil`).
3. Negative values die in `OpenAccount` before insert.
4. One `INSERT` of `id`, `initial_balance_cents`, `balance_cents`, `created_at`. Single statement, so it does not need an explicit transaction.
5. 201 with the account. 400 on bad JSON or a negative amount.

Not idempotent. See finding 4.8.

### 5.2 Transfer

1. Validate amount `> 0`, distinct non-empty account ids, key non-blank and `<= 255` bytes.
2. `db.Conn(ctx)` pins the pooled connection. With `MaxOpenConns(1)`, other work waits. That wait uses the request context, so a cancel before `BEGIN` moves no money.
3. `BEGIN IMMEDIATE` takes the reserved lock before any read.
4. Look up `idempotency_key`. Match means `COMMIT` the empty transaction and return `Replay`. Mismatch returns 409 and the deferred rollback runs.
5. Load both balances. Missing account is 404. Insufficient funds is 422. Overflow is 422.
6. Conditional debit. Credit. Insert `transfers`. Insert two `ledger_entries` (`-amount`, `+amount`) with `UNIQUE(transfer_id, account_id)`.
7. `COMMIT`. Any error before this point rolls back, including the injected credit-insert failure.
8. HTTP 201, or 200 if `Replay` was set.

The credit is checked in Go, not with a conditional `UPDATE`. Under the reserved lock those are equivalent. The README's "checked receiver credit" means that Go check, not a `WHERE` clause.

### 5.3 Balance and history

- `Balance` is one statement. It sees the latest commit, never a half-applied transfer.
- `History` first calls `Balance` so a missing account is 404, then joins `ledger_entries` to `transfers`. The balance value is discarded. A transfer can commit between the two statements; the response is only the history query, so the JSON stays internally consistent.
- Empty history is a non-nil slice, so JSON is `[]`, not `null`.
- Sort is in Go by parsed time, then id. See finding 4.9.

---

## 6. Test map

### 6.1 What is actually proved, if the suite passes

| Test | Proves |
| --- | --- |
| `TestOpenAccountPreservesExactBalance` | 0, 10000, and `MaxInt64` round-trip; negative open is `ErrInvalidAmount` |
| `TestBalanceUnknownAccount` | Missing balance is `ErrAccountNotFound` |
| `TestHistoryEmptyAndMissingAccount` | New account history is empty; missing account is not found |
| `TestTransferMovesExactCentsAndWritesTwoEntries` | 100→75 and 10→35 on a 25-cent move; both histories contain the id; two entries sum to 0 |
| `TestTransferRejectsInvalidRequestsWithoutMutation` | Insufficient, zero, negative, self, missing source, missing destination, blank key; source stays at 100 |
| `TestTransferRollsBackWhenLedgerInsertFails` | Debit does not stick if the credit entry insert aborts |
| `TestTransferRejectsReceiverOverflow` | `MaxInt64` receiver plus 1 is `ErrBalanceOverflow`; balances unchanged |
| `TestTransferIdempotentReplayAndConflict` | Same key returns the same id with `Replay`; changed amount conflicts; balances move once |
| `TestHistoryOrdersFractionalTimesAfterWholeSecond` | `.12Z` sorts before `.123Z` after a text-timestamp rewrite |
| `TestCompetingTransfersCannotDoubleSpend` | Two `Store`s, one file, 100 cents, two keys: 1 success, 1 insufficient, balances 0 and 100, 1 transfer, 2 entries |
| `TestConcurrentSameKeyMovesMoneyOnce` | 20 callers, one key: one id, money moves once |
| `TestFiftyCompetingTransfersDrainOnlyOnce` | 50 callers for the full balance: 1 success, 49 insufficient |
| `TestPartialDrainAndBidirectionalTraffic` | 10 of 20 partial debits succeed; 100 bidirectional transfers conserve both balances and write 200 entries |
| `TestSeededConcurrentAuditPreservesEveryCent` | Seed `20261020`, 10 accounts, 500 random transfers: no negative balance, per-account reconstruction, global sum 100000, every transfer has 2 entries summing to 0 |
| `TestAccountAndTransferRoutes` | 201 create, 200 replay, same id, balance 60, history contains the id |
| `TestStrictMoneyJSONAndErrorStatuses` | 400 on fraction, exponent, string, negative, overflow, unknown field, trailing JSON; 422 insufficient; 404 missing; 409 conflict |
| `TestShutdownWaitsForActiveRequest` | Shutdown does not return while a handler is blocked, **when the dial wins the race** |

### 6.2 What those tests do not prove

- HTTP 422 `balance_overflow`.
- HTTP 400 on a missing `initial_balance_cents`, JSON `null`, zero transfer amount, self-transfer, or a 256-byte key.
- Error **code** in the JSON body, not just the status.
- Replay after the source is drained, through HTTP.
- Mismatch on destination id only.
- A failed key can be reused successfully later. The code does this; no test reserves a failed key and then succeeds with it.
- `CHECK`, `STRICT`, foreign keys, `UNIQUE`, or pragma values, by attacking the schema.
- Cross-connection locking for the 50-way, partial, bidirectional, and audit tests. Those share one `Store` and therefore one connection. They prove the in-process queue plus the conditional debit. Only the two-handle test proves the SQLite lock.
- That `go test -race` is clean on this snapshot. Not run.

### 6.3 Test-quality nits

- Several tests discard `OpenAccount` and `Balance` errors (`from, _ := ...`). A setup failure still fails later, but the message is worse.
- `countResults` is used as `_, _ = countResults(...)` in the audit. Unexpected errors still `Fatal`. Success and insufficient counts are ignored on purpose; conservation is the assertion. That is fine.
- Concurrency helpers ignore balance-query errors and then assert the integer. A query error looks like a wrong balance. Acceptable, noisy.
- The shutdown test leaks the blocked handler if it fatals before `close(release)`.

---

## 7. Snapshot and build issues

- No `go.sum` in the pack. Go 1.22 will refuse or rewrite the module on a clean `go test` until sums exist. If the real git repo has `go.sum`, this is only a pack gap. If git also lacks it, commit it.
- `BUILD_LOG.md` says every code slice passed `go vet ./...` and `go test -race -count=1 ./...`, and that a fresh clone passed after the review fixes. Treat that as an unverified claim until you run it.
- Suggested gate, from the repo root, with Go 1.22 or newer:

```sh
go vet ./...
go test -race -count=1 ./...
go test -race -count=10 -run 'TestCompetingTransfersCannotDoubleSpend' ./internal/ledger
```

PowerShell needs double quotes around the `-run` pattern, as the README says.

---

## 8. What not to "fix"

- Do not switch money to `float64` or a decimal library.
- Do not add `SELECT ... FOR UPDATE`. SQLite will reject it.
- Do not raise `MaxOpenConns` above 1 unless the DSN sets pragmas on every connection and every transfer still starts with `BEGIN IMMEDIATE` before reading balances. A deferred `BEGIN` plus read-then-write is the double-spend the tests are there to catch.
- Do not sort `created_at` as text.
- Do not reserve idempotency keys on failed attempts. A rejected key must stay reusable.
- Do not return 409 for insufficient funds. Key mismatch is checked first; that is correct.
- Do not treat a replay as a new debit just because the source is now empty.

---

## 9. Fix order

If you only change three things before someone else reads this:

1. **After a doubtful `COMMIT`, read the idempotency key and return the stored transfer when it is there.** Map a genuinely missing outcome to a retryable error, not a bare 500. One sentence in the README: transport errors and 500s must replay the same key.
2. **Move `busy_timeout`, `foreign_keys`, and `journal_mode` into the SQLite DSN**, and add a test that reads them from a new connection.
3. **Add four negative SQL tests:** negative balance insert, non-integer into a money column, orphan transfer with foreign keys on, duplicate idempotency key. Retry the shutdown dial so that test cannot fail closed.

Then, if there is time: check credit `RowsAffected`, shrink the body limit and close the connection when it trips, and stop using `os.Exit` on the listen-failure path.

---

## 10. File-level notes

### `internal/ledger/model.go`

Sentinel errors are plain `errors.New` values, so `errors.Is` works. `Transfer.Replay` is correctly hidden from JSON. No functional issue.

### `internal/ledger/store.go`

Schema matches the invariants you can enforce in SQLite without triggers: non-negative balances, positive transfer amounts, no self-transfer, unique key, unique `(transfer_id, account_id)`, non-zero entry amounts. Nothing in the schema says a transfer has exactly two entries or that those entries sum to zero. The code and the audit query do. That split is documented and acceptable.

`OpenAccount` stores `time.RFC3339Nano` in UTC. `History` parses with the same layout. That round-trip works, including timestamps with no fractional seconds. Do not scan these columns as `time.Time` through the driver unless you have checked the driver's time format.

`History` has no `LIMIT`. The README already says history is unpaginated. That is a fifth-hour item, not a current defect.

### `internal/ledger/transfer.go`

Validation, lock, idempotency, conditional debit, overflow check, and rollback structure are right. The defects are the unguarded credit update (low) and both `COMMIT` error returns (high).

Replay takes `BEGIN IMMEDIATE` and commits a transaction that did not write. Correct, and it holds the write lock longer than a read needs. Not worth changing unless you are chasing throughput.

### `internal/api/handler.go`

Status mapping matches the README:

| Error | Status | Code |
| --- | ---: | --- |
| `ErrInvalidAmount`, `ErrInvalidAccount`, `ErrInvalidIdempotencyKey` | 400 | `invalid_request` |
| `ErrAccountNotFound` | 404 | `account_not_found` |
| `ErrIdempotencyPayloadMismatch` | 409 | `idempotency_payload_mismatch` |
| `ErrInsufficientFunds` | 422 | `insufficient_funds` |
| `ErrBalanceOverflow` | 422 | `balance_overflow` |
| anything else | 500 | `internal_error` |

Internal error text is not returned to the client. Good.

### `cmd/server/main.go`

Signal handling via `signal.NotifyContext` for interrupt and `SIGTERM`, then `Shutdown` with a 10-second cap, is the right shape. `serve` waits for that shutdown on the `ErrServerClosed` path. Findings are the `os.Exit` skips and the lack of a forced `Close` after a shutdown timeout.

### Tests

Coverage of the success path, the obvious rejects, rollback, replay, overflow, and the two-handle race is better than a typical four-hour pass. The holes are the constraint backstops, the HTTP overflow status, and the shutdown dial race. Section 6 is the full map.

---

## 11. Bottom line

Ship the design. Do not let a reviewer walk away thinking the race is unhandled: `BEGIN IMMEDIATE` plus the conditional debit is the answer, and the two-store test is the proof.

Do not ship the impression that a 500 is a clean failure. For a ledger, "I don't know if the commit landed" has to be answered by reading the idempotency key, not by asking the client to guess. That is the one change that protects money. The pragma DSN and the constraint tests are what protect the claim that the database still saves you if the Go checks regress.

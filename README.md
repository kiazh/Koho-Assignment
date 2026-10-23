# Move the Money

A concurrency-safe, double-entry financial ledger and HTTP JSON API built in **Go 1.22+** and **SQLite (`modernc.org/sqlite` in WAL mode)**.

It implements the four required operations—opening an account with a starting balance, transferring money between accounts, reading an account's current balance, and reading an account's transaction history—while enforcing and proving the four money invariants under concurrent contention:

1. **Balances never go negative:** Not under any sequence of requests, including simultaneous requests competing for the exact same funds across separate database connections.
2. **Transfers are all-or-nothing:** Every transfer atomically debits the sender, credits the receiver, records the transfer, and writes two zero-sum ledger entries (`-amount_cents` and `+amount_cents`) inside a single ACID write transaction. Any failure rolls back the entire transaction.
3. **At-most-once execution (Idempotency):** Network retries and double-clicks submitting the same `idempotency_key` and payload execute the transfer once and replay the original transfer record (`200 OK`) without a second debit. Reusing a key with different parameters returns `409 Conflict`.
4. **Exact amounts (`int64` cents):** All balances and transfer amounts are signed 64-bit integer cents (`int64`) across JSON, Go, and SQLite `STRICT` tables. Floating-point numbers never enter the system.

For the full account of my workflow, architectural choices, where AI tools helped, where they were confidently wrong, and how I debugged them, see the **[build log](docs/BUILD_LOG.md)**. The [technical decisions](docs/TECH_STACK_DECISIONS.md) summary records the stack choices and their alternatives.


| Test or group | Evidence |
| --- | --- |
| `TestCompetingTransfersCannotDoubleSpend` | Two independent store callers cannot both spend the same 100 cents. |
| `TestConcurrentSameKeyMovesMoneyOnce` | Twenty concurrent retries produce one payment with a consistent ID. |
| `TestConcurrentConflictingPayloadsKeepOneKeyOwner` | Two different amounts cannot both claim the same key. |
| `TestFiftyCompetingTransfersDrainOnlyOnce` | Fifty requests for the full balance produce one success. |
| `TestPartialDrainAndBidirectionalTraffic` | Partial drains and opposite-direction transfers conserve money. |
| `TestSeededConcurrentAuditPreservesEveryCent` | After 500 concurrent attempts across ten accounts, balances, histories, entries, counts, and totals reconcile. |
| `TestLedgerAuditDetectsMalformedPairs` | The audit notices wrong paired amounts or missing entries, even if a pair still adds to zero. |
| `TestTransferRollsBack...` | Mid-operation failures restore balances and leave no partial records. |
| `TestCommittedTransferAndReplaySurviveReopen` | Balances, history, and idempotency persist after closing and reopening SQLite. |
| `TestTransferRejectsReceiverOverflow` | Receiver overflow does not mutate either account. |
| `TestReadersUseSeparateConfiguredConnectionDuringWrite` | Another connection sees the committed balance, not the uncommitted write. |
| `TestSQLiteMoneyConstraintsRejectInvalidRows` | Important database checks work even with direct SQL inputs. |
| `TestConcurrentHTTPTransfersCannotDoubleSpend` | Twenty HTTP requests competing for 100 cents yield one creation and 19 rejections. |

---

## Repository Layout

```text
.
├── cmd/server/
│   ├── main.go                 # HTTP server entrypoint, timeouts & graceful shutdown
│   └── main_test.go            # In-flight request drain verification on shutdown
├── internal/
│   ├── api/
│   │   ├── handler.go          # Go 1.22 HTTP routes, strict JSON decoder & error mapping
│   │   └── handler_test.go     # Boundary validation, overflow, replay & 20-way HTTP race tests
│   └── ledger/
│       ├── model.go            # Domain types (Account, Transfer, TransferRequest) & sentinel errors
│       ├── store.go            # SQLite WAL setup, per-conn DSN pragmas, STRICT schema, reads
│       ├── transfer.go         # Atomic BEGIN IMMEDIATE transfer engine & busy-lock retry
│       ├── store_test.go       # Rollback triggers, raw SQL constraint probes, PRAGMA & reopen tests
│       ├── concurrency_test.go # Cross-store double-spend barrier proof, retry storms & 500-tx audit
│       └── benchmark_test.go   # Parallel read, sequential transfer & 1000-row history benchmarks
├── docs/
│   ├── BUILD_LOG.md            # Engineering workflow, AI catches, rejected ideas & verification
│   ├── TECH_STACK_DECISIONS.md # Stack choices, alternatives & limits
│   └── _historical/
│       ├── codex_repository_instructions_agents_md.md
│       ├── koho_definition_of_done_and_execution_spec.md
│       └── plan.md
└── README.md                   # Runbook, API reference, architecture & scope trade-offs
```

---

## How to Run the Service

Because this uses Go's standard library and the pure-Go `modernc.org/sqlite` driver, it runs from a clean checkout with **zero external services, no Docker, no PostgreSQL setup, and no C compiler (`cgo`) required**.

Prerequisite: **Go 1.22+** installed.

```sh
go mod download
go run ./cmd/server
```

By default, the server listens on `127.0.0.1:8080` and persists data to `ledger.db` in the working directory. You can override either with environment variables (`ADDR` and `LEDGER_DB`).

*Note for Windows:* Port `8080` is sometimes reserved by system services on Windows. If `127.0.0.1:8080` is unavailable, bind to any free localhost port:

```powershell
$env:ADDR = '127.0.0.1:18178'
$env:LEDGER_DB = 'ledger.db'
go run ./cmd/server
```

---

## How to Run the Tests

Run static analysis and the full race-enabled test suite from the repository root:

```sh
go vet ./...
go test -race -count=1 ./...
```

### Running the Core Rule 1 Concurrency Proof

The assignment states: *"At least one of your tests should demonstrate that rule 1 survives two transfers competing for the same money at the same time. How you construct that test is up to you, and it is the single thing we will look at most closely."*

Run `TestCompetingTransfersCannotDoubleSpend` 10 consecutive times with the Go race detector enabled:

```sh
# macOS / Linux
go test -race -count=10 -v -run '^TestCompetingTransfersCannotDoubleSpend$' ./internal/ledger

# Windows PowerShell
go test -race -count=10 -v -run "^TestCompetingTransfersCannotDoubleSpend$" ./internal/ledger
```

**What this test does (`internal/ledger/concurrency_test.go`):**

1. Opens **two independent `Store` instances** (`first` and `second`, each with its own connection pool) against the **same SQLite file** (`shared.db`).
2. Funds Account A with `100` cents ($1.00) and Account B with `0` cents.
3. Spawns two goroutines—one on each `Store`—and holds both at a `sync.WaitGroup` + `chan struct{}` start barrier (`<-start`).
4. Closes `start` so both goroutines simultaneously attempt to transfer the full `100` cents from Account A to Account B with distinct idempotency keys (`race-0` and `race-1`).
5. Asserts that **exactly 1 succeeds**, **exactly 1 fails with `ErrInsufficientFunds`**, final balances are `0` and `100`, `transfers` has `1` row, and `ledger_entries` has `2` rows.

### Other Targeted Proof Tests

```powershell
# 20 concurrent callers submitting the exact same idempotency_key across two Store handles
go test -race -v -run "^TestConcurrentSameKeyMovesMoneyOnce$" ./internal/ledger

# Fault injection: SQLite triggers aborting mid-transaction or ignoring receiver credit -> full rollback
go test -race -v -run "^TestTransferRollsBack" ./internal/ledger

# 500 seeded concurrent transfers across 10 accounts verifying global conservation & entry reconstruction
go test -race -v -run "^TestSeededConcurrentAuditPreservesEveryCent$" ./internal/ledger

# 20 simultaneous HTTP POST /transfers requests competing for the same 100 cents
go test -race -v -run "^TestConcurrentHTTPTransfersCannotDoubleSpend$" ./internal/api
```

---

## Try the HTTP API

With the server running in one terminal (`$env:ADDR = '127.0.0.1:18178'`), run this end-to-end sequence in a second PowerShell terminal (or equivalent `curl` commands):

```powershell
$base = 'http://127.0.0.1:18178'

# 1. Open two accounts ($100.00 and $0.00)
$a = Invoke-RestMethod -Method Post -Uri "$base/accounts" -ContentType application/json -Body '{"initial_balance_cents":10000}'
$b = Invoke-RestMethod -Method Post -Uri "$base/accounts" -ContentType application/json -Body '{"initial_balance_cents":0}'

# 2. Send 2500 cents ($25.00) from A to B
$key = [guid]::NewGuid().ToString('N')
$body = @{ from_account_id = $a.id; to_account_id = $b.id; amount_cents = 2500; idempotency_key = $key } | ConvertTo-Json -Compress
$first = Invoke-RestMethod -Method Post -Uri "$base/transfers" -ContentType application/json -Body $body

# 3. Replay the exact same transfer request (simulating a retry / double-click)
$replay = Invoke-RestMethod -Method Post -Uri "$base/transfers" -ContentType application/json -Body $body

# 4. Verify balances and transaction history
[pscustomobject]@{
    SameTransferID   = ($first.id -eq $replay.id)                                                    # True
    SenderBalance    = (Invoke-RestMethod "$base/accounts/$($a.id)/balance").balance_cents            # 7500
    ReceiverBalance  = (Invoke-RestMethod "$base/accounts/$($b.id)/balance").balance_cents            # 2500
    ReceiverHistory  = (Invoke-RestMethod "$base/accounts/$($b.id)/transactions").transactions.Count  # 1
}
```

### API Contract Summary

| Endpoint | Success Status | Error Statuses & Codes |
| :--- | :--- | :--- |
| `POST /accounts`<br>`{"initial_balance_cents": 10000}` | `201 Created` | `400 invalid_request` (negative, decimal/exponent/string, missing field, unknown field, trailing JSON, or overflow) |
| `GET /accounts/{id}/balance` | `200 OK` | `404 account_not_found` |
| `GET /accounts/{id}/transactions` | `200 OK` | `404 account_not_found` |
| `POST /transfers`<br>`{"from_account_id": "...", "to_account_id": "...", "amount_cents": 2500, "idempotency_key": "..."}` | `201 Created` (new)<br>`200 OK` (idempotent replay) | `400 invalid_request` (`amount_cents <= 0`, self-transfer, blank/`>255`-byte key, malformed JSON)<br>`404 account_not_found`<br>`409 idempotency_payload_mismatch` (same key, different parameters)<br>`422 insufficient_funds`<br>`422 balance_overflow` (credit would exceed `math.MaxInt64`) |

---

## Architecture & Why the Money Rules Hold

### 1. Why `BEGIN IMMEDIATE` Instead of `SELECT ... FOR UPDATE`

In PostgreSQL, preventing concurrent double-spends requires locking the two account rows in canonical ID order (`SELECT ... FROM accounts WHERE id IN ($1, $2) ORDER BY id ASC FOR UPDATE`). SQLite does not have row-level locks and rejects `FOR UPDATE` syntax. Furthermore, Go's default `db.BeginTx()` issues `BEGIN DEFERRED` in SQLite, which does not acquire a lock until the first write statement—allowing two concurrent requests to read the same balance simultaneously before either writes.

To close that race window completely, `Store.Transfer` (`internal/ledger/transfer.go`) pins a dedicated connection (`s.db.Conn(ctx)`) and executes `BEGIN IMMEDIATE` (with a bounded 3-attempt context-aware retry if `SQLITE_BUSY` occurs before the transaction begins). This acquires SQLite's reserved writer lock **before** reading the idempotency key or either account balance.

### 2. Defense-in-Depth on Every Invariant

- **Idempotency Checked Before Balances:** Inside the `BEGIN IMMEDIATE` transaction, `transfers` is queried by `idempotency_key` *before* checking the sender's balance. If Account A had 100 cents, transferred 100 cents (leaving 0), and the client retries that request, it returns the original transfer (`200 OK`) rather than failing with `422 insufficient_funds`. Failed attempts roll back and never reserve an idempotency key.
- **Triple Non-Negative Guard:** Sender affordability is enforced in Go (`sourceBalance < request.AmountCents`), in the atomic SQL mutation (`UPDATE accounts SET balance_cents = balance_cents - ? WHERE id = ? AND balance_cents >= ?` with `RowsAffected() == 1`), and by the SQLite `STRICT` table schema (`CHECK(balance_cents >= 0)`).
- **Checked Receiver Credit & Overflow Guard:** Before crediting the destination account, Go checks `destinationBalance > math.MaxInt64 - request.AmountCents` (`ErrBalanceOverflow`). The credit `UPDATE` also verifies `RowsAffected() == 1` so a skipped update can never leave a one-sided debit.
- **Zero-Sum Double-Entry Audit Trail:** Opening funds are stored on `accounts.initial_balance_cents`. Every transfer inserts one `transfers` row and two signed `ledger_entries` rows (`-amount_cents` for sender, `+amount_cents` for receiver). At all times, `balance_cents(a) = initial_balance_cents(a) + SUM(ledger_entries.amount_cents WHERE account_id = a)`.

- **Connection Pool & WAL Read Concurrency:** File-backed stores configure an 8-connection pool (`SetMaxOpenConns(8)`) with `_pragma=busy_timeout(10000)` and `_pragma=foreign_keys(1)` embedded in the DSN so every pooled replacement connection inherits foreign-key enforcement and lock timeouts while WAL readers proceed alongside an active writer.

---

## What I Chose Not to Build (To Respect the 4-Hour Cap)

- **No User Interface (UI):** Exposed strictly via the HTTP JSON API.
- **No Authentication, Authorization, or KYC:** Explicitly out of scope per the assignment brief. Because any caller can open a funded account, the server binds to `127.0.0.1` by default and must not be exposed publicly.
- **No Multi-Currency or FX Conversion:** Single currency represented in `int64` minor units (cents).
- **No External Containers (Docker / Postgres / Redis / Kafka):** All ACID and idempotency guarantees live inside a single local SQLite transactional boundary so reviewers can run the project in seconds.
- **No Idempotency Key on `POST /accounts`:** Transfers are strictly idempotent, whereas account creation always inserts a new account. If an account-creation call times out, the caller should verify before retrying.

---

## What I Would Do Next (Hour 5 & Production Evolution)

1. **Resolve the Uncertain-`COMMIT` Window in Code:** If `COMMIT` returns an I/O or context error after SQLite has already persisted the transaction, the server currently returns `500 internal_error`. Retrying with the *same* `idempotency_key` is safe (and documented), but if a client mints a *new* key on `500`, it risks double-sending. My first Hour 5 change is a 10-line post-commit recovery check: when `COMMIT` errors, query `transfers` by `idempotency_key` on a fresh read connection and return `200 OK` if the transfer landed.
2. **Cursor-Based History Pagination:** `GET /accounts/{id}/transactions` currently loads and sorts all transfers for an account. I would store fixed-width UTC timestamps (e.g., `YYYY-MM-DDTHH:MM:SS.nnnnnnnnnZ` or integer epoch nanoseconds) so SQLite can execute indexed cursor pagination (`LIMIT` + `(created_at, id)` cursor) directly in SQL.
3. **Operator Reconciliation Command & Idempotent Account Creation:** Add a CLI subcommand that runs the global conservation and per-account `initial_balance_cents + SUM(ledger_entries) == balance_cents` audit on demand, and add an optional `idempotency_key` to `POST /accounts`.
4. **PostgreSQL Row-Locking Migration for Multi-Node Scale:** When scaling beyond one node or SQLite's single-writer throughput ceiling, implement the `store` interface against PostgreSQL using canonical ascending lock ordering (`SELECT ... FROM accounts WHERE id IN ($1, $2) ORDER BY id ASC FOR UPDATE`) to allow parallel transfers across disjoint account pairs without AB–BA deadlocks.

# Build Log — Move the Money

## The short version

I built a small Go 1.22 and SQLite HTTP service that opens accounts, moves integer cents between them, and reads balances and history. The four money rules are enforced in the transaction and at the database boundary, and the main race test opens two separate `Store` handles on the same file to prove that two 100-cent transfers cannot both spend the same 100 cents.

AI wrote most of the Go. I do not pretend otherwise. My job was to decide what the system had to prove, set up the test gates, review the output, and debug the failures. The catches that mattered were a PostgreSQL `FOR UPDATE` pattern that does not exist in SQLite, text sorting of `RFC3339Nano` timestamps, a receiver credit that could silently affect zero rows, connection-local PRAGMAs being dropped by the pool, and a `SQLITE_BUSY` starvation bug found by the final 500-request race audit. The sharpest remaining gap is an uncertain `COMMIT`, which is documented but not fully solved in code.

The brief asks for roughly one page. I kept this short version at the top and the detailed record below. The extra detail is there so the claims can be checked against the commits and tests instead of taken on trust.

Supporting records: [technical decisions](TECH_STACK_DECISIONS.md), [README](../README.md), and the original setup notes in [`_historical/`](_historical/).

## What I set out to do

The assignment asks for four operations and four rules that must never break:

1. A balance can never go negative, including when two requests compete for the same money.
2. A transfer is all or nothing.
3. The same transfer submitted twice is applied once.
4. Amounts are exact; no cent may disappear.

The brief also says the two-transfer race is the thing it will look at most closely, and it asks for an honest account of where AI was wrong. So I treated the race proof as a product requirement, not an optional test. I chose Go with the standard library and the pure-Go `modernc.org/sqlite` driver so a reviewer can run everything from a clean checkout without Docker, Postgres, or a C compiler. Amounts are `int64` cents in JSON, Go, and SQLite, and every successful transfer writes one negative and one positive ledger entry inside the same transaction.

I deliberately cut everything that did not strengthen those four rules: no UI, no authentication, no multi-currency, no external services. The trade-offs are in the README and in the decision log. The short version is that this is a local correctness exercise, not a production banking service.

## How I worked

I used AI as the implementer, not as the authority. The workflow that produced this repo was:

1. **Research first.** I usually start with Perplexity Deep Research because it is good at surfacing known patterns for ledgers, double-entry accounting, and database locking. I treat the output as a map, not as truth. Retrieved answers often mix PostgreSQL and SQLite syntax, or recommend a package version that does not fit the Go version I pinned.
2. **Define done before asking for code.** I gave the coding harness the original brief, a definition of done, and explicit gates: race detection, static analysis, HTTP boundary tests, rollback tests, a two-connection race proof, and an honest issue log. That setup matters more than a broad "build this" prompt.
3. **Work in small slices.** Even when the setup looked strong enough to generate the whole repo at once, I did not do that. Each commit had one job: account storage, reads, transfers, the race proof, HTTP, then hardening. The commit trail below is the real sequence.
4. **Get a fresh-context review.** I sent the finished code to reviewers that had not watched me make the decisions. My broader workflow can compare Argon, Claude, and GPT reviews, but agreement between models is not proof. A review finding only earns a code change when I can reproduce it with the real toolchain or a failing test.
5. **Run it myself.** I started the server, exercised the HTTP routes, tested the Windows port fallback, and checked the restart behavior. The tests are the main evidence, but I did not treat a green unit suite as a substitute for using the service.

AI output is not deterministic. Long sessions accumulate stale assumptions, and an agent asked to grade its own work will often write tests that confirm the implementation instead of challenging it. Small commits and fresh-context reviews are how I try to keep those failure modes visible.

### Commit trail

| Commits | What changed |
| --- | --- |
| `3f9e5b5` → `c627f7c` | Exact-cent account storage, then balance and history reads. |
| `1e220f6` → `693f4bc` | Atomic `BEGIN IMMEDIATE` transfer, zero-sum entries, rollback proof, idempotency, and the two-Store race test. |
| `828c502` → `e5a86a4` | Strict HTTP routes and the first 500-request conservation audit. |
| `b92f86d` → `0a1da62` | Submission docs, then the timestamp-order and graceful-shutdown fixes found in review. |
| `f1be2d5` → `7cb288c` | Benchmarks, the connection-pool/WAL fix, and more cross-Store and HTTP contention tests. |
| `9f9a568` → `6228d0b` | Exact ledger-pair audit, malformed-fixture test, raw SQL constraints, and connection-replacement checks. |
| `48c64fa` → `bb8e82c` | Receiver-credit row-count check, retry/router contract docs, final verification, and restart smoke test. |
| `cb4e4b4` → `a2cf2d6` | Documentation split, presentation runbook, historical-input labels, decision log, and HTTP boundary tests. |
| `b848b9a` → `8f0fc6f` | `SQLITE_BUSY` begin-only retry, lock-release test, and the build-log record of that catch. |
| `92bf2df` | README replaced with the submission-facing overview. |

## Where AI genuinely helped

- **The strict JSON boundary.** The agent produced the `json.NewDecoder` setup with `DisallowUnknownFields()`, a second decode that requires `io.EOF`, and direct `int64` decoding. That rejects `10.5`, `1e2`, `"100"`, unknown fields, trailing JSON, and `int64` overflow at the HTTP boundary. I decided which cases were errors; the agent wrote the boilerplate quickly.
- **The concurrency harnesses.** Start-gun channels, `sync.WaitGroup` barriers, result collection, and the SQL reconciliation queries are mechanical work. AI saved real time there. I still read the assertions and decided what a pass meant.
- **Fault injection and benchmarks.** The agent helped wire up SQLite `BEFORE INSERT` and `BEFORE UPDATE` triggers, and the benchmark file that compares read concurrency and transfer throughput.
- **Rapid comparison.** It let me try different pool sizes, driver versions, and transaction shapes without hand-writing every variation. That is legitimate acceleration, and I am not going to pretend I typed it all myself.

## Where the tools were wrong, and how I caught it

### 1. `SELECT ... FOR UPDATE` does not exist in SQLite

My research and spec generation both suggested locking the two account rows in ascending ID order with `SELECT ... FOR UPDATE`. That is the right PostgreSQL habit: lock both rows in a canonical order, then read balances, then update. In SQLite it is not valid. SQLite has no row-level locks and rejects `FOR UPDATE` syntax.

There is a second trap underneath it. Go's default `db.BeginTx()` produces a deferred transaction in SQLite, so it does not take a write lock until the first write. Two requests can both read the same starting balance before either writes. The fix was to pin a dedicated `*sql.Conn` and issue `BEGIN IMMEDIATE` before reading the idempotency key or either balance. That takes SQLite's writer lock first.

I proved it with `TestCompetingTransfersCannotDoubleSpend`: two separate `Store` instances on the same database file, a start barrier, and two 100-cent transfers against a 100-cent balance. Exactly one succeeds, one gets `ErrInsufficientFunds`, the balances end at 0 and 100, and there is one transfer with two ledger entries.

### 2. Sorting `RFC3339Nano` timestamps as text

The first version of `History` sorted the stored timestamp strings directly. It looked clean and passed the early tests. It is wrong because `time.RFC3339Nano` drops trailing zeros in the fractional seconds, so the string width varies. In text order, `2026-01-01T00:00:00.12Z` sorts after `2026-01-01T00:00:00.123Z`, even though 120 ms is earlier than 123 ms.

I caught it in a fresh-context review and reproduced it with `TestHistoryOrdersFractionalTimesAfterWholeSecond`, which writes `00Z`, `.12Z`, and `.123Z` fixtures. The fix is to parse the timestamps into `time.Time` values, sort by the instant, and use the transfer ID as a deterministic tie-breaker.

### 3. A receiver credit that could silently affect zero rows

The sender debit checked `RowsAffected() == 1`. The receiver credit below it did not. If a trigger or a future schema change caused the credit to affect zero rows without returning an SQL error, the code would debit the sender, write the transfer and ledger rows, leave the receiver unchanged, and commit. That breaks rule 2, and nothing in the old code would have complained.

I reproduced it with a `BEFORE UPDATE ... RAISE(IGNORE)` trigger in `TestTransferRollsBackWhenReceiverUpdateIsIgnored`. The old code committed a one-sided debit. The fix checks that the credit changed exactly one row and rolls back otherwise.

### 4. Connection-local PRAGMAs and a one-connection pool

`Open` originally used `SetMaxOpenConns(1)` and ran `PRAGMA foreign_keys = ON` and `PRAGMA busy_timeout = 10000` once with `db.Exec()`. That has two problems.

First, one connection means a writer blocks readers before WAL can help. WAL is only useful if a reader can get a different connection. Second, `foreign_keys` and `busy_timeout` are connection-local in SQLite. When `database/sql` opens a replacement connection, those settings silently reset. The database can end up with foreign keys disabled on the connection that happens to serve the next write.

I found the read blocking with a held-writer test and benchmarked the difference: parallel balance reads were roughly 24–25 µs/op with one connection and about 6–7 µs/op with eight on this machine. The fix moves `_pragma=busy_timeout(10000)` and `_pragma=foreign_keys(1)` into the DSN so every new connection gets them, and uses eight connections for file-backed stores. `TestReadersUseSeparateConfiguredConnectionDuringWrite` and `TestReplacementConnectionKeepsRequiredPragmas` check both behaviors.

### 5. `SQLITE_BUSY` under the 500-request audit

The final full race gate failed once in `TestSeededConcurrentAuditPreservesEveryCent` after about 13 seconds with `begin transfer: database is locked (5) (SQLITE_BUSY)`. It reproduced in two of three focused runs. I raised the busy timeout from 10 seconds to 15 seconds and it still failed once in five, so a longer blind wait was not a fix.

An agent suggested wrapping the whole `Transfer` call in a retry loop. I rejected that. A failure after `COMMIT` may mean the transaction already landed; retrying the whole transfer can double-apply it. The `SQLITE_BUSY` was happening at `BEGIN IMMEDIATE`, before any transaction state existed, so a retry there is safe. I kept the 10-second timeout and added a bounded, context-aware retry around `BEGIN IMMEDIATE` only: three attempts, a short backoff, and a typed `*sqlite.Error` check for code 5.

`TestBeginImmediateRetriesBusyAndSucceedsWhenLockClears` holds the write lock on one connection, forces a 1 ms busy timeout on another, proves the helper retries and classifies the error, then releases the lock and proves the next begin succeeds. The 500-request audit passed ten consecutive race-enabled runs after the fix. No money moved in the failing case: the transaction never started.

### 6. A zero-sum audit that could miss wrong amounts

An early audit only checked that each transfer had two ledger entries summing to zero. That is not enough. A 40-cent transfer corrupted to `-39` and `+39` still sums to zero, and a missing pair can hide in an aggregate. I wrote `TestLedgerAuditDetectsMalformedPairs`, corrupted fixtures directly, and tightened the query to check the exact account, sign, and amount on both sides. The account-level audit also reconstructs each balance from opening funds plus signed entries.

### 7. Smaller catches and false leads

- **Go version floor.** A newer `modernc.org/sqlite` pulled a transitive requirement on a newer Go version. `go mod tidy -go=1.22.0` exposed it, and I pinned `v1.34.5`.
- **A missing `go.sum` that was not missing.** One external review read a packed export that omitted `go.sum` and concluded the repo was incomplete. The file was tracked; I checked the actual checkout.
- **A pool claim that was already fixed.** The same review said replacement connections still dropped PRAGMAs. By the time I checked, the DSN fix was in place, so I verified the real connection behavior instead of changing code in response to a stale report.
- **The Windows port.** Port 8080 was reserved on this machine. The live demo ran on `127.0.0.1:18178`, and the README documents the override.
- **The documentation flattening.** Early AI drafts of this log kept turning the debugging trail into a confident summary. The facts were often right; the specificity was wrong. I rewrote the log against the actual commits and test output. That is the one catch that was about directing the tool rather than fixing the code, and it is the one I would want to talk through in the interview.

## Design choices and rejected alternatives

| Area | What I chose | What I rejected, and why | Evidence in the repo |
| --- | --- | --- | --- |
| **Interface** | Standard-library HTTP JSON API with Go 1.22 routes | A CLI or library is harder to demonstrate live status codes such as `201` versus `200` replay, `409`, and `422`. A frontend adds no value to the concurrency proof. | `internal/api/handler.go`, `TestAccountAndTransferRoutes` |
| **Runtime and driver** | Go 1.22+ standard library plus pure-Go `modernc.org/sqlite v1.34.5` | Node/TypeScript loses integer precision above 2^53 unless every boundary uses strings or `BigInt`. `mattn/go-sqlite3` needs cgo and a C toolchain on the reviewer's machine. | `go.mod`, `go test -race ./...` |
| **Money type** | Signed `int64` cents across JSON, Go, and SQLite `STRICT` tables | `float64` introduces rounding drift. A decimal library adds a dependency and a conversion layer when integer cents map directly to SQL `INTEGER` and `CHECK` constraints. | `TestStrictMoneyJSONAndErrorStatuses`, `TestTransferRejectsReceiverOverflow` |
| **Database** | One local SQLite file in WAL mode with eight pooled connections | An in-memory map plus mutex loses durability and only proves Go locking. Docker plus PostgreSQL adds setup friction to a four-hour local exercise. | `internal/ledger/store.go`, `TestCommittedTransferAndReplaySurviveReopen` |
| **Ledger schema** | `accounts` with opening and current balance, `transfers`, and two signed `ledger_entries` per transfer | A balance column alone has no audit trail. Summing entries on every balance read turns an O(1) lookup into an O(N) scan. | `TestTransferMovesExactCentsAndWritesTwoEntries`, `TestSeededConcurrentAuditPreservesEveryCent` |
| **Locking and concurrency** | `BEGIN IMMEDIATE` on a pinned connection, a three-attempt busy retry, and a conditional debit | `SELECT ... FOR UPDATE` is invalid SQLite. A whole-transfer retry is unsafe after an ambiguous commit. | `internal/ledger/transfer.go`, `TestCompetingTransfersCannotDoubleSpend` |
| **Idempotency** | `UNIQUE(idempotency_key)` checked inside the transaction before balances, with direct comparison of source, destination, and amount | A SHA-256 payload hash adds complexity for three fields. Reserving a key on a failed transfer prevents a user from retrying after topping up. | `TestConcurrentSameKeyMovesMoneyOnce`, `TestFailedTransferDoesNotReserveIdempotencyKey` |
| **History ordering** | Parse timestamps to instants, sort by time, then ID | Sorting `RFC3339Nano` strings is wrong because the fractional width varies. | `TestHistoryOrdersFractionalTimesAfterWholeSecond` |
| **Server lifecycle** | Explicit 5s/10s/15s/60s timeouts and `signal.NotifyContext` with graceful shutdown | The default server has no useful timeouts and can abort in-flight requests on `SIGINT`. | `cmd/server/main.go`, `TestShutdownWaitsForActiveRequest` |

## What I shipped that I am not comfortable with

1. **The uncertain-`COMMIT` window.** If the final `COMMIT` returns an error, or the HTTP write timeout fires after SQLite has committed, the API returns `500 internal_error`. A failed `COMMIT` call can still mean the transaction landed. If a client reacts by generating a new idempotency key, the money can move twice. The README tells callers to retry `500`s and timeouts with the same key and payload, but the safer fix is in code: when `COMMIT` errors, look up the idempotency key on a fresh connection and return the stored transfer if it exists. I did not implement that here. This is the sharpest edge in the submission.
2. **What the storm tests actually prove.** The two-store tests genuinely exercise SQLite's file lock across separate connection pools. The larger storms (50-way drain, 100-way bidirectional, and the 500-request audit) share a single `Store`, so they prove in-process pool behavior, the begin retry, and the conditional debit more than they prove cross-process locking. I do not want the test count to imply more than that.
3. **Account opening is not idempotent.** `POST /accounts` always inserts. A timeout followed by a retry creates a second funded account. The brief does not require idempotent opening, but it is the other retry trap beside transfers.
4. **Unbounded history, permanent keys, and one writer.** `History` loads and sorts every matching transfer in memory. Successful idempotency keys never expire. SQLite serializes writes. These are acceptable for a local demo, not for a production service.
5. **The busy retry is bounded, not a guarantee.** If all three `BEGIN IMMEDIATE` attempts fail, the request can still return `500` or hit the HTTP write timeout before any money moves. A production version should return a distinct retryable error and use a request-level deadline.

With a fifth hour I would close the post-commit lookup first, then add cursor pagination over fixed-width timestamps, an operator reconciliation command, and idempotent account creation. For multiple service nodes or higher write throughput, I would move the store to PostgreSQL and lock both account rows in ascending ID order with `SELECT ... FOR UPDATE`.

## How I verified it

The final gate was:

- `gofmt`, `go vet ./...`, and `go build ./...` clean.
- The full race suite passed with `go test -race -count=1 ./...`.
- Five shuffled race-suite runs passed.
- The suite passed at CPU counts 1, 4, and 12.
- The 500-request audit passed ten consecutive race-enabled runs after the busy retry.
- The focused double-spend and HTTP race tests each passed 20 consecutive race-enabled runs.
- The shutdown test passed 50 repetitions.
- Coverage was 75.8% overall: 90.5% in the API and 79.6% in the ledger. The remaining server percentage is mostly process wiring.

The final transfer smoke test used a compiled server, stopped and reopened the process, and confirmed the same transfer ID, balances of 60/40 cents, and one history row. Balance reads improved from about 24–25 µs/op with one connection to 5.9–7.0 µs/op with eight; 100 concurrent bidirectional transfers finished in about 0.49–0.58 seconds in three local race-enabled runs. Those are this machine's numbers, not a throughput promise.

One thing I would change next time is when I ran the hardest gate. The 500-request race audit found the last real bug after I thought the work was done. I would run that audit earlier and repeatedly, not just at the end.

# Decision log

This is the detailed architectural record behind **Move the Money**. The [build log](BUILD_LOG.md) explains how I worked with AI and where it was wrong; the [implementation journal](IMPLEMENTATION_JOURNAL.md) records the issue trail and commit map; this file answers a narrower question: **why is the code shaped this way, and what else did I consider?**

I wrote one record per consequential choice. Each record states the decision, the alternatives, the evidence that it holds, and when I would revisit it. The records are deliberately more detailed than the one-page build log because the brief asks for the reasoning, not just the outcome.

## How to read a record

Every record uses the same template:

```markdown
### DNN — Short title

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** short hashes
**Context:** the forces at play.
**Options considered:**
- Option A — what it buys and what it costs.
- Option B — what it buys and what it costs.
**Decision:** what shipped.
**Why:** the deciding reason.
**Evidence:** the code, test, or measurement that makes the claim checkable.
**Consequences:** what this choice costs or constrains.
**Revisit when:** the condition that would justify changing it.
```

Status is `Accepted`, `Superseded`, or `Revisit`. A superseded record stays in place and points to its replacement; I do not rewrite history to make an earlier choice look better.

## Index

| ID | Decision | Status |
| --- | --- | --- |
| [D01](#d01--http-json-api-as-the-deliverable) | HTTP JSON API as the deliverable | Accepted |
| [D02](#d02--go-122-standard-library-and-pure-go-sqlite) | Go 1.22, standard library, pure-Go SQLite | Accepted |
| [D03](#d03--int64-cents-everywhere) | `int64` cents everywhere | Accepted |
| [D04](#d04--one-local-sqlite-file-in-wal-mode) | One local SQLite file in WAL mode | Accepted |
| [D05](#d05--balance-cache-plus-double-entry-ledger) | Balance cache plus double-entry ledger | Accepted |
| [D06](#d06--server-generated-account-ids-and-single-insert-opening) | Server-generated account IDs, single-insert opening | Accepted |
| [D07](#d07--database-constraints-as-backstops) | Database constraints as backstops | Accepted |
| [D08](#d08--begin-immediate-instead-of-row-locks-or-a-mutex) | `BEGIN IMMEDIATE` instead of row locks or a mutex | Accepted |
| [D09](#d09--transfer-order-and-checked-writes) | Transfer order and checked writes | Accepted |
| [D10](#d10--idempotency-by-unique-key-and-direct-field-comparison) | Idempotency by unique key and direct field comparison | Accepted |
| [D11](#d11--history-is-parsed-and-sorted-in-go) | History is parsed and sorted in Go | Accepted |
| [D12](#d12--eight-connections-with-pragmas-on-every-connection) | Eight connections with PRAGMAs on every connection | Accepted |
| [D13](#d13--strict-json-and-explicit-http-status-mapping) | Strict JSON and explicit HTTP status mapping | Accepted |
| [D14](#d14--server-timeouts-signals-and-graceful-shutdown) | Server timeouts, signals, and graceful shutdown | Accepted |
| [D15](#d15--test-the-risky-path-at-more-than-one-layer) | Test the risky path at more than one layer | Accepted |
| [D16](#d16--deliberate-scope-cuts) | Deliberate scope cuts | Accepted |
| [D17](#d17--known-risks-and-deferred-work) | Known risks and deferred work | Revisit |
| [D18](#d18--rejected-alternatives-register) | Rejected alternatives register | Reference |

---

## D01 — HTTP JSON API as the deliverable

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `828c502`, `b92f86d`

**Context:** The brief accepts an HTTP API, a CLI, or a well-tested library. It requires four operations and no UI. The submission also needs to make the four money rules easy to demonstrate in an interview.

**Options considered:**
- **HTTP JSON API** — directly demoable with `Invoke-RestMethod` or `curl`; status codes make replay (`200`) versus a new transfer (`201`) visible; the interviewer can inspect each rule at the boundary.
- **CLI** — compact to build and easy to script, but the transaction state and idempotency outcome are less visible in a demo.
- **Library only** — strongest unit-test story, weakest demo story; the reviewer has to write a caller before they can use it.
- **API plus UI** — the brief explicitly says no UI is required; a UI would consume the four-hour budget without strengthening the money rules.

**Decision:** Build a small HTTP JSON API with four routes and no UI.

**Why:** It is the shortest path from a clean checkout to a visible demonstration of all four rules. The status code is part of the contract: a new transfer is `201`, an identical replay is `200`, an insufficient transfer is `422`, and a key reuse with different fields is `409`.

**Evidence:** The routes are registered in [`internal/api/handler.go`](../internal/api/handler.go); [`handler_test.go`](../internal/api/handler_test.go) exercises success, replay, strict JSON, and error statuses; the [README](../README.md) contains a copy-paste demo. The HTTP-level race test sends 20 real requests through `httptest.NewServer`.

**Consequences:** The API needs a strict JSON boundary, explicit status mapping, and a body limit. There is no CLI convenience layer. Authentication and a real funding policy are absent by design, so the service must stay on localhost.

**Revisit when:** An operator needs scheduled reconciliation or scripted account creation; a CLI over the same ledger package would then be a thin addition, not a rewrite.

---

## D02 — Go 1.22, standard library, and pure-Go SQLite

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `3f9e5b5`, `f1be2d5`

**Context:** The service is small, concurrent, and money-sensitive. The toolchain has to support exact integers, race detection, a standard HTTP server, and a zero-install review experience.

**Options considered:**
- **Go with `net/http` and `database/sql`** — one static binary, strong `int64` support, first-class race detector, no framework needed for four routes.
- **Node/TypeScript** — excellent JSON ergonomics, but JavaScript numbers lose integer precision above `2^53`; the API would need strings or `BigInt` conventions to keep cents exact.
- **Python** — fast iteration, but packaging and concurrency ergonomics are weaker for a small self-contained demo.
- **Go with a cgo SQLite driver** — mature, but requires a C compiler on the reviewer's machine and complicates Windows setup.
- **Go with `modernc.org/sqlite`** — pure Go, no C compiler, works with `go test -race` and `go run` from a clean checkout.

**Decision:** Go 1.22+ with the standard library and `modernc.org/sqlite v1.34.5`.

**Why:** Go gives exact `int64` arithmetic, a race detector, and a standard HTTP server with method-aware routing in 1.22. The pure-Go driver removes the only external build dependency. The module declares `go 1.22.0`; the driver is pinned to a release compatible with that floor rather than letting a newer transitive requirement raise it silently.

**Evidence:** [`go.mod`](../go.mod) pins the module and driver; the full suite passes under `go test -race`; the [build log](BUILD_LOG.md) records the `go mod tidy -go=1.22.0` catch that exposed a newer driver's Go requirement.

**Consequences:** The project deliberately avoids frameworks and ORMs. A reviewer needs Go 1.22+ but no database service. The dependency is pinned, so upgrades are explicit.

**Revisit when:** The team standardizes on another runtime, or the driver's Go floor needs to move for a security or performance reason.

---

## D03 — `int64` cents everywhere

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `3f9e5b5`, `828c502`

**Context:** Rule 4 says amounts are exact. A cent lost anywhere is a failure. The representation has to be identical in JSON, Go, and SQLite so there is no conversion boundary where precision can leak.

**Options considered:**
- **`float64`** — rejected. Binary floating point cannot represent most decimal cents exactly; `0.1 + 0.2` is the canonical warning.
- **A decimal library** — exact, but adds a dependency and a second representation that must be converted at the JSON and SQL boundaries.
- **String decimals** — exact if parsed carefully, but invites inconsistent parsing and makes arithmetic awkward.
- **`int64` minor units** — exact for addition, subtraction, and comparison; maps directly to JSON integers and SQLite `INTEGER`; overflow can be checked explicitly.

**Decision:** Every amount is a signed 64-bit integer number of cents: JSON, Go structs, SQL columns, and ledger entries.

**Why:** Integer cents eliminate rounding by construction. The same value survives JSON decoding, Go arithmetic, and SQLite storage. `encoding/json` rejects fractional, exponent, numeric-string, and out-of-range values when decoding directly into `int64`, so the boundary enforces the representation instead of trusting the caller.

**Evidence:** `Account`, `TransferRequest`, and `Transfer` use `int64` in [`model.go`](../internal/ledger/model.go). The schema uses `INTEGER` columns with `CHECK` constraints in [`store.go`](../internal/ledger/store.go). `TestStrictMoneyJSONAndErrorStatuses` rejects `10.5`, `1e2`, `"100"`, `-1`, and `9223372036854775808`. `TestOpenAccountPreservesExactBalance` round-trips `0`, `10000`, and `math.MaxInt64`. `TestTransferRejectsReceiverOverflow` checks the credit ceiling.

**Consequences:** One currency, no fractional cents, and a maximum balance of `math.MaxInt64` cents. A JavaScript client loses precision above `2^53` unless it treats amounts as strings; that is documented as a boundary limitation, not hidden. Multi-currency would need a currency code and a minor-unit definition.

**Revisit when:** The service supports multiple currencies or sub-cent pricing; even then, the storage should remain integer minor units, not floating point.

---

## D04 — One local SQLite file in WAL mode

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `3f9e5b5`, `e501778`

**Context:** The brief accepts any storage and values a clean, runnable submission. The concurrency proof needs real cross-connection behavior, not an in-memory stand-in that accidentally shares one connection.

**Options considered:**
- **In-memory map plus a mutex** — fastest to write, but durability is lost and the concurrency proof would only test one process's lock, not a database's.
- **PostgreSQL in Docker** — strong row-locking story, but adds a service, credentials, and startup friction to a four-operation exercise.
- **SQLite file with WAL** — zero services, ACID transactions, a real file lock, and readers that can proceed while a writer holds its transaction.
- **SQLite `:memory:`** — convenient for tests, but a process-private database can split across connections; it would weaken the race proof.

**Decision:** One local SQLite database file in WAL mode, opened by the server; tests use file-backed temporary databases.

**Why:** SQLite gives ACID semantics and a real single-writer lock without external infrastructure. WAL lets readers continue during a write, which matters for balance and history requests. A file-backed database lets the decisive test open two independent `Store` handles against the same file, which is the strongest available proof in this stack.

**Evidence:** [`store.go`](../internal/ledger/store.go) opens the file, sets WAL, and creates the schema. `TestCompetingTransfersCannotDoubleSpend` uses two stores on one file. `TestCommittedTransferAndReplaySurviveReopen` closes and reopens the database and verifies that the transfer and replay survive. The [README](../README.md) documents `LEDGER_DB` and the default `ledger.db`.

**Consequences:** SQLite admits one writer at a time. Write throughput is bounded by that design, and horizontal scaling would require a different store. The local file is ignored by Git (`.gitignore` covers `*.db`, `*.db-wal`, and `*.db-shm`).

**Revisit when:** Multiple service nodes, higher write throughput, or managed backups become requirements; PostgreSQL with explicit row locking is the next step.

---

## D05 — Balance cache plus double-entry ledger

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `3f9e5b5`, `1e220f6`, `9f9a568`

**Context:** The service needs fast balance reads, an auditable history, and a way to prove that every stored balance agrees with the money that moved.

**Options considered:**
- **Balance only** — one `accounts.balance_cents` column; simple, but no immutable audit trail and no way to reconstruct how the balance was reached.
- **Ledger entries only** — derive the balance by summing entries on every read; correct but increasingly expensive, and opening funds still need a representation.
- **Balance cache plus signed entries** — store the current balance for reads, and write one negative sender entry plus one positive receiver entry per transfer; the opening balance is stored separately for reconciliation.
- **Full event sourcing** — more general, but far more machinery than four operations need.

**Decision:** Store `initial_balance_cents` and `balance_cents` on each account, plus a `transfers` row and exactly two signed `ledger_entries` per successful transfer.

**Why:** The cached balance makes reads O(1), while the signed entries make the history and audit possible. The opening balance is not a transfer, so it is recorded explicitly rather than invented as a funding transaction. The audit equation is:

```text
initial_balance_cents
  + incoming transfer amounts
  - outgoing transfer amounts
  = current balance_cents
```

**Evidence:** The schema is in [`store.go`](../internal/ledger/store.go). `TestTransferMovesExactCentsAndWritesTwoEntries` asserts two entries summing to zero. `TestSeededConcurrentAuditPreservesEveryCent` reconstructs every account from signed entries and checks global conservation. `TestLedgerAuditDetectsMalformedPairs` corrupts a pair to prove the audit can fail.

**Consequences:** The balance and the entries are denormalized, so they must change in the same transaction; the transfer code does that. The schema does not itself enforce “exactly two entries per transfer” — the transaction and audit tests do. A direct SQL writer could still corrupt the pair, which is documented rather than hidden.

**Revisit when:** An event-sourced or append-only accounting model becomes a requirement, or reconciliation moves to a separate service.

---

## D06 — Server-generated account IDs and single-insert opening

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `3f9e5b5`, `828c502`

**Context:** The brief requires opening an account with a starting balance. It does not require caller-supplied IDs or idempotent account creation.

**Options considered:**
- **Client-supplied account IDs** — useful for import or migration, but introduces a duplicate-ID path the brief never asks for.
- **Server-generated random IDs** — no collision handling needed at this scale; the caller cannot accidentally choose an existing ID.
- **Sequential integers** — compact and readable, but leak account volume and invite guessing; not useful for a demo ledger.

**Decision:** Generate 16 random bytes with `crypto/rand`, hex-encode them, and insert the account in one statement with `initial_balance_cents = balance_cents`.

**Why:** Random IDs remove the duplicate-ID branch and are unguessable enough for a local demo. A single insert is atomic without an explicit transaction. A negative opening balance is rejected in Go before SQL, and the database also has a `CHECK`.

**Evidence:** `newID` and `OpenAccount` are in [`store.go`](../internal/ledger/store.go). `TestOpenAccountPreservesExactBalance` covers zero, a normal balance, `math.MaxInt64`, and a negative rejection. `TestAccountAndTransferRoutes` covers the HTTP `201`.

**Consequences:** Account opening is **not idempotent**. A timeout followed by a retry can create a second funded account. The brief does not require idempotent opening, but the README and [build log](BUILD_LOG.md) call it out as a retry trap.

**Revisit when:** Account creation becomes a production onboarding flow; add a client-supplied idempotency key and a unique constraint on it.

---

## D07 — Database constraints as backstops

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `3f9e5b5`, `6228d0b`

**Context:** Application validation can regress. The database should reject the most dangerous states even if a future change forgets a Go check.

**Options considered:**
- **Application checks only** — easier to evolve, but a missing check silently permits negative or non-integer money.
- **Database constraints only** — strong, but poor error messages and awkward business-rule expression.
- **Both, with the database as a backstop** — clear domain errors in Go, plus hard stops in SQLite.
- **Triggers for the two-entry invariant** — enforceable, but more moving parts and harder to reason about during a live change.

**Decision:** Use `STRICT` tables plus these constraints:

- `accounts.initial_balance_cents >= 0` and `accounts.balance_cents >= 0`.
- `transfers.amount_cents > 0` and `from_account_id <> to_account_id`.
- `transfers.idempotency_key` is `UNIQUE`.
- `transfers.from_account_id` and `to_account_id` reference `accounts(id)`.
- `ledger_entries.amount_cents <> 0` and `UNIQUE(transfer_id, account_id)`.
- An index on `ledger_entries(account_id, created_at, id)`.

**Why:** These are the invariants SQLite can state directly. They survive a Go regression and produce immediate failures. The “exactly two entries, one negative and one positive, matching the transfer” rule is enforced by the transaction code and the audit query rather than a trigger, because the pair spans three tables and the test can express the rule more clearly than the schema.

**Evidence:** The schema is in [`store.go`](../internal/ledger/store.go). `TestSQLiteMoneyConstraintsRejectInvalidRows` attempts negative, fractional, orphaned, duplicate-key, zero, and self-transfer rows directly. `TestReplacementConnectionKeepsRequiredPragmas` proves foreign keys and the busy timeout survive a pool replacement. `TestLedgerAuditDetectsMalformedPairs` proves the audit notices a corrupted pair.

**Consequences:** A direct SQL writer can still insert an unbalanced pair because the two-entry rule is not a trigger. That is a deliberate boundary: the service owns the transaction, and the audit test detects drift. Schema migrations are not present; `CREATE TABLE IF NOT EXISTS` is enough for a fresh demo database.

**Revisit when:** Multiple writers or external tools touch the database; then add triggers, a stronger ledger model, or a migration tool.

---

## D08 — `BEGIN IMMEDIATE` instead of row locks or a mutex

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `693f4bc`, `e501778`, `7cb288c`

**Context:** Two transfers can try to spend the same balance at the same moment. The affordability decision must be serialized before either transaction reads a balance.

**Options considered:**
- **Read-then-write inside a deferred transaction** — the classic double-spend. Two readers can both see 100 cents and both debit; rejected.
- **`SELECT ... FOR UPDATE` with sorted account IDs** — correct in PostgreSQL, but SQLite has no row locks and rejects the syntax. Sorting IDs does not create a lock; rejected.
- **A process-local mutex** — serializes one process, but two `Store` handles or two processes bypass it. It would make the in-process tests pass and the real file race fail; rejected as the primary mechanism.
- **`BEGIN IMMEDIATE` on a dedicated connection** — takes SQLite's reserved write lock before any idempotency or balance read, across connections and processes.
- **PostgreSQL row locks** — the right future choice for multiple nodes, but not available without adding a service.

**Decision:** Every transfer obtains a dedicated `*sql.Conn`, executes `BEGIN IMMEDIATE`, and only then reads the idempotency key and balances.

**Why:** The write lock is acquired before the read that decides affordability, so two competing transfers cannot both observe the same balance. The lock is held by the database file, not by a Go object, so the two-store test is meaningful. A deferred rollback runs on every error path using `context.Background()` so a canceled request context cannot prevent cleanup.

**Evidence:** The transaction is in [`transfer.go`](../internal/ledger/transfer.go). `TestCompetingTransfersCannotDoubleSpend` opens **two separate `Store` handles on one file** and releases two 100-cent transfers through one start gate; it requires one success, one insufficient-funds error, balances of 0 and 100, one transfer, and two entries. `TestConcurrentSameKeyMovesMoneyOnce` covers 20 concurrent callers. `TestFiftyCompetingTransfersDrainOnlyOnce` covers 50-way contention.

**Consequences:** SQLite allows one writer at a time, so write throughput is serialized. Contention waits on `busy_timeout` rather than failing immediately. The design is correct for one node and intentionally not a distributed lock.

**Revisit when:** More than one service node or higher write throughput is required; move to PostgreSQL and lock both account rows in ascending ID order before the balance read.

---

## D09 — Transfer order and checked writes

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `1e220f6`, `48c64fa`

**Context:** A transfer touches two balances, one transfer row, and two ledger entries. Any partial application would break rule 2. The order of checks also determines replay behavior.

**Options considered:**
- **Check balances first, then idempotency** — a replay after the source has been drained would incorrectly return insufficient funds instead of the original transfer; rejected.
- **Unconditional debit after an application check** — relies on the earlier read staying true; the conditional debit is a second guard and gives a clear `RowsAffected` signal.
- **Unchecked credit** — can silently affect zero rows under a trigger or future schema change, leaving the sender debited and the receiver unchanged; rejected after a fault-injection test exposed it.
- **Checked credit plus explicit overflow guard** — each mutation proves it changed exactly one row, and the receiver cannot overflow `int64`.

**Decision:** Run the transfer in this order:

1. Validate amount, distinct non-empty account IDs, and a non-blank key of at most 255 bytes.
2. Obtain a dedicated connection and `BEGIN IMMEDIATE`.
3. Look up the idempotency key. A matching payload returns the existing transfer; a different payload returns `ErrIdempotencyPayloadMismatch`.
4. Read both account balances; a missing account is `ErrAccountNotFound`.
5. Reject insufficient funds and receiver overflow (`destinationBalance > math.MaxInt64 - amount`).
6. Debit with `WHERE id = ? AND balance_cents >= ?`; require one affected row.
7. Credit the receiver; require one affected row.
8. Insert the transfer and the two signed ledger entries.
9. `COMMIT`; any earlier error triggers the deferred rollback.

**Why:** The idempotency check comes before the balance check so a replay is stable even after the source is empty. The conditional debit makes the affordability rule true at the moment of mutation, not just at read time. The checked credit closes the silent-skip gap. The overflow guard keeps the credit inside `int64` before SQLite sees it.

**Evidence:** [`transfer.go`](../internal/ledger/transfer.go). `TestTransferRollsBackWhenLedgerInsertFails` aborts the positive entry insert and requires every balance and row to be unchanged. `TestTransferRollsBackWhenReceiverUpdateIsIgnored` uses `RAISE(IGNORE)` on the credit and requires a rollback. `TestTransferRejectsReceiverOverflow` checks the `MaxInt64` boundary. `TestFailedTransferDoesNotReserveIdempotencyKey` proves a failed key can be reused later.

**Consequences:** The write lock is held across the key lookup and both balance reads. The transaction is intentionally short, but it is still a serialized critical section. A generic retry loop was rejected because no observed `SQLITE_BUSY` failure required one; `busy_timeout` handles normal contention.

**Revisit when:** A new mutation is added to the transfer path; it must join the same transaction and have its affected-row count checked.

---

## D10 — Idempotency by unique key and direct field comparison

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `693f4bc`, `7cb288c`

**Context:** Rule 3 says the same transfer submitted twice is applied once. Retries can be sequential or concurrent, and a caller can reuse a key with a different payload by mistake.

**Options considered:**
- **A separate idempotency table** — flexible for storing responses and expiry, but another table and another lifecycle to keep in sync with the transfer.
- **A payload hash** — compact, but adds a hashing decision and obscures which field differed; the payload is only three short fields.
- **Direct comparison of source, destination, and amount** — simple, inspectable, and sufficient for the current request shape.
- **Reserve the key on every attempt** — makes failed attempts block later retries; rejected because a rejected transfer must not own the key.
- **Reserve only on success** — the unique constraint and the transaction make the key durable exactly when money moves.

**Decision:** `transfers.idempotency_key` is `UNIQUE`. A successful transaction inserts the transfer and reserves the key. A replay with the same source, destination, and amount returns the original transfer with `Replay: true` (HTTP `200`). A different payload returns `409 idempotency_payload_mismatch`. Blank keys and keys longer than 255 bytes are rejected. Failed attempts do not reserve the key.

**Why:** Comparing the three stored fields is easier to review than a hash and gives a precise error. The unique constraint is the durable backstop. Keeping the key lookup inside the write transaction means concurrent same-key callers serialize and all see the committed row.

**Evidence:** The logic is in [`transfer.go`](../internal/ledger/transfer.go). `TestTransferIdempotentReplayAndConflict` covers sequential replay and mismatch. `TestConcurrentSameKeyMovesMoneyOnce` sends 20 concurrent callers and requires one transfer ID and one movement. `TestConcurrentConflictingPayloadsKeepOneKeyOwner` covers two different payloads racing for one key. `TestFailedTransferDoesNotReserveIdempotencyKey` covers reuse after a failure. `TestCommittedTransferAndReplaySurviveReopen` proves the key survives a restart.

**Consequences:** Keys are retained for the life of the database; there is no expiry or cleanup. The comparison covers only the three request fields; adding a field to the transfer contract would require updating the comparison. The **uncertain-commit** case remains: a `COMMIT` error returns a generic `500`, and the client must retry with the same key rather than mint a new one. That is the sharpest known edge and is recorded in [D17](#d17--known-risks-and-deferred-work).

**Revisit when:** Idempotency keys need retention limits, a response body must be cached, or the request gains fields that must participate in the match.

---

## D11 — History is parsed and sorted in Go

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `0a1da62`, `9f9a568`

**Context:** History must show every transfer affecting an account in a stable order. Timestamps are stored as RFC3339Nano text, and fractional seconds have variable width.

**Options considered:**
- **Sort the timestamp strings directly** — looks natural and is wrong: `2026-01-01T00:00:00.12Z` sorts after `2026-01-01T00:00:00.123Z` as text even though it is earlier in time.
- **`ORDER BY created_at` in SQL** — the column is text, so the same variable-width bug returns; rejected unless the format is changed.
- **Fixed-width UTC timestamps** — a good alternative, but it changes the storage format and requires a migration decision.
- **Parse to `time.Time` and sort instants in Go** — correct for the existing format, with the transfer ID as a tie-break.

**Decision:** Query the transfers for the account, parse each `created_at` with `time.RFC3339Nano`, sort by the parsed instant and then by transfer ID, and return an empty slice (not `null`) for a new account. Check that the account exists first so a missing account is `404`, not an empty history.

**Why:** Parsing removes the text-width trap without changing the stored format. The ID tie-break makes equal timestamps deterministic. The existence check distinguishes “no transfers yet” from “no such account.”

**Evidence:** `History` is in [`store.go`](../internal/ledger/store.go). `TestHistoryOrdersFractionalTimesAfterWholeSecond` rewrites timestamps to `...00Z`, `...00.12Z`, and `...00.123Z` and requires the correct order. `TestHistoryEmptyAndMissingAccount` covers the empty and missing cases. `TestAccountAndTransferRoutes` checks the HTTP history contains the transfer.

**Consequences:** History loads all matching rows and sorts in memory; there is no pagination. Equal timestamps fall back to a random hex ID, which is deterministic but not insertion order. The index on `(account_id, created_at, id)` helps the lookup but not the Go sort.

**Revisit when:** History grows large enough to need pagination or a fixed-width timestamp column; then move ordering into SQL with a format that sorts correctly.

---

## D12 — Eight connections with PRAGMAs on every connection

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `f1be2d5`, `e501778`, `6228d0b`

**Context:** WAL should let readers proceed during a write, but a single pooled connection serializes reads behind the writer. `foreign_keys` and `busy_timeout` are connection-local in SQLite, so a new pooled connection must receive them too.

**Options considered:**
- **One connection** — simple and easy to reason about, but a held write blocks every read and makes the WAL benefit invisible.
- **Eight file-backed connections** — allows WAL readers to use other connections while one writer holds the transaction.
- **More connections** — no evidence that a larger pool helps this workload; it adds contention and tuning without a measured need.
- **Per-connection PRAGMAs via the DSN** — the only reliable way to configure connections that `database/sql` dials later.

**Decision:** File-backed stores use `SetMaxOpenConns(8)`; `:memory:` stays on one connection so it remains one database. The DSN includes `_pragma=busy_timeout(10000)` and `_pragma=foreign_keys(1)`, and startup sets `PRAGMA journal_mode = WAL`. The busy timeout is 10 seconds (the planning notes suggested 5; 10 is a deliberate local tolerance).

**Why:** WAL is only useful if reads can get a different connection. `database/sql` can replace a connection after a failure, so setting PRAGMAs once on the pool is not enough; the DSN applies them on every new connection. `modernc.org/sqlite` honors `_pragma=`, not the `mattn/go-sqlite3` `_fk`/`_busy_timeout` keys.

**Evidence:** `Open` is in [`store.go`](../internal/ledger/store.go). `TestReadersUseSeparateConfiguredConnectionDuringWrite` holds a write on one connection and reads on another, checking `foreign_keys = 1` and `busy_timeout = 10000`. `TestReplacementConnectionKeepsRequiredPragmas` forces a replacement connection and checks WAL, foreign keys, and the timeout. The [build log](BUILD_LOG.md) records the observed balance-read benchmark change from roughly 24–25 µs/op with one connection to about 6–7 µs/op with eight; those are local measurements, not a throughput promise.

**Consequences:** Writes still serialize through SQLite's single writer. Eight is a local default, not a capacity plan. The pool makes reads more available, not writes faster.

**Revisit when:** Measurements show the pool is the bottleneck, or the store moves to a database with real row-level concurrency.

---

## D13 — Strict JSON and explicit HTTP status mapping

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `828c502`, `da46486`

**Context:** The API boundary is where imprecise money or ambiguous failures can enter. A caller also needs to distinguish a new transfer from a replay and a bad payload from a missing account.

**Options considered:**
- **Permissive decoding** — accepts decimals and strings, then converts; invites precision bugs and hides contract mistakes.
- **Framework binding with validation tags** — convenient, but another dependency and less explicit control over `int64` and trailing JSON.
- **Strict `encoding/json` into `int64`** — rejects imprecise representations at the boundary and keeps the contract small.
- **Always return `200` with an error body** — easier for some clients, but obscures replay and failure in a demo; HTTP statuses are part of the contract.

**Decision:** Decode with `DisallowUnknownFields`, require a single JSON value followed by `io.EOF`, cap the body at 1 MiB, and decode money directly into `int64`. Account opening uses `*int64` so a missing field is distinguishable from zero; transfer amount uses `int64` so missing and zero both fail validation.

Status mapping:

| Domain error | HTTP | JSON `error` |
| --- | ---: | --- |
| `ErrInvalidAmount`, `ErrInvalidAccount`, `ErrInvalidIdempotencyKey` | `400` | `invalid_request` |
| `ErrAccountNotFound` | `404` | `account_not_found` |
| `ErrIdempotencyPayloadMismatch` | `409` | `idempotency_payload_mismatch` |
| `ErrInsufficientFunds` | `422` | `insufficient_funds` |
| `ErrBalanceOverflow` | `422` | `balance_overflow` |
| anything else | `500` | `internal_error` |

A new transfer returns `201`; a replay returns `200`. Unknown routes and wrong methods are the Go router's plain-text `404`/`405`, not the JSON error shape.

**Why:** Direct `int64` decoding lets the standard library reject fractions, exponents, numeric strings, and overflow before the ledger sees them. Explicit status mapping makes the retry and conflict rules visible. Keeping internal error text out of the response avoids leaking storage details.

**Evidence:** [`handler.go`](../internal/api/handler.go) and [`handler_test.go`](../internal/api/handler_test.go). The tests cover strict money JSON, unknown fields, trailing JSON, insufficient funds, missing accounts, payload conflicts, and receiver overflow (`TestHTTPBoundaryRejectionsAndReceiverOverflow`). `TestRouterBoundaryResponses` checks the plain-text router behavior.

**Consequences:** Duplicate JSON keys are accepted by `encoding/json` with last-value-wins; this is a known boundary gap. The 1 MiB cap is generous for a body that should be a few hundred bytes. JavaScript clients above `2^53` cents need string handling. There is no authentication on any route.

**Revisit when:** The API is exposed beyond localhost; then add auth, a smaller body cap, duplicate-key rejection, and a standard problem-details error format.

---

## D14 — Server timeouts, signals, and graceful shutdown

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `0a1da62`

**Context:** A money-moving server should not hang on a slow client, and a shutdown should not close the database while a request is still using it.

**Options considered:**
- **Default `http.Server` with no timeouts** — simple, but vulnerable to slow headers and idle connections.
- **Immediate exit on SIGINT** — fast, but can cut off an in-flight transfer and make the client's retry decision ambiguous.
- **Timeouts plus `Shutdown` with a deadline** — bounds slow clients and lets active requests finish.
- **A framework's lifecycle wrapper** — unnecessary for one server and four routes.

**Decision:** Use `http.Server` with `ReadHeaderTimeout: 5s`, `ReadTimeout: 10s`, `WriteTimeout: 15s`, and `IdleTimeout: 60s`; listen for `os.Interrupt` and `SIGTERM` through `signal.NotifyContext`; call `Shutdown` with a 10-second deadline; and make `serve` wait for the shutdown result before returning.

**Why:** The timeouts bound the resources a slow or stalled client can hold. `Shutdown` stops accepting new connections and waits for active handlers. Waiting for the result prevents the store from closing underneath a request that is still finishing.

**Evidence:** [`main.go`](../cmd/server/main.go). `TestShutdownWaitsForActiveRequest` blocks a handler, cancels the context, verifies the server has not returned, releases the handler, and then requires a clean return.

**Consequences:** `os.Exit(1)` on a startup failure skips deferred `store.Close()` and the signal stop; the process is terminating, so this is a minor clean-checkpoint issue rather than a money bug. On a shutdown timeout, the server returns without a forced `Close`. The 15-second write timeout also means a very slow response can surface as a transport error, which feeds the uncertain-commit discussion in D17.

**Revisit when:** The service is deployed behind a real load balancer; then add forced close on timeout, structured access logs, and health/readiness endpoints.

---

## D15 — Test the risky path at more than one layer

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `693f4bc`, `e5a86a4`, `7cb288c`, `9f9a568`, `6228d0b`

**Context:** The brief says the two-transfer race is the single thing it will scrutinize. Happy-path unit tests would not prove the lock, the rollback, or the ledger reconciliation.

**Options considered:**
- **Happy-path tests only** — fast, but proves almost nothing about the invariants.
- **A timing-based test with sleeps** — flaky and can pass without real contention; rejected.
- **Start-gated goroutines with a barrier** — deterministic release of competing callers.
- **A single store with many goroutines** — tests the in-process queue and conditional debit, but not the cross-connection file lock.
- **Two stores on one file plus a barrier** — the decisive test for rule 1 under real SQLite locking.
- **Fault injection and raw SQL probes** — prove rollback and the database backstops, which normal API tests cannot reach.

**Decision:** Use layered tests:

- **Store tests** for exact balances, missing accounts, empty history, replay, conflict, overflow, rollback, durability, and raw constraints.
- **Concurrency tests** with a start gate for two-store, 50-way, 20-way partial, 100-way bidirectional, and 20 same-key contention.
- **Fault injection** with SQLite triggers for an aborted ledger insert and an ignored receiver credit.
- **A seeded 500-attempt audit** that reconstructs each account from signed entries and checks global conservation.
- **HTTP tests** for status codes, strict JSON, receiver overflow, and 20 concurrent requests through a real `httptest` server.
- **A shutdown test** that blocks a handler and verifies the drain.
- **Benchmarks** for balance reads, history, and sequential transfers.

**Why:** Each layer answers a different question. The two-store test proves the database lock; the storm tests prove the in-process queue and conditional debit under higher contention; the fault-injection tests prove rollback; the audit proves the accounting equation; the HTTP tests prove the boundary. No single green test is treated as proof of the whole system.

**Evidence:** [`concurrency_test.go`](../internal/ledger/concurrency_test.go), [`store_test.go`](../internal/ledger/store_test.go), [`handler_test.go`](../internal/api/handler_test.go), and [`main_test.go`](../cmd/server/main_test.go). The decisive test is `TestCompetingTransfersCannotDoubleSpend`; it opens two `Store` handles on one file and asserts one success, one insufficient-funds result, balances `0/100`, one transfer, and two entries. The full suite passes under `go test -race -count=1 ./...`; the focused race test passed 10 consecutive runs during final verification.

**Consequences:** The larger storm tests mostly share one `Store` and therefore one connection; they do **not** prove cross-connection locking as strongly as the two-store test. The [build log](BUILD_LOG.md) says this explicitly rather than letting the test count imply more than it should. There is no CI configuration in the repository; the gates are run locally.

**Revisit when:** The project gets CI; then run the race suite with shuffle and repetition, and keep the two-store test as a required gate.

---

## D16 — Deliberate scope cuts

**Status:** Accepted · **Date:** 2026-10-20 · **Commits:** `b92f86d`, `785867b`

**Context:** The brief caps the work at four hours and explicitly accepts an API, CLI, or library. The risk is spending time on scaffolding that does not strengthen the money rules.

**Options considered:**
- **Build a UI** — not required; would consume the budget and add a second boundary to test.
- **Add authentication, KYC, and a funding policy** — not required for the exercise; a real banking service needs them, but they would displace correctness work.
- **Add multi-currency and FX** — not required; adds rounding and rate-source problems.
- **Add Docker, Kubernetes, or cloud deployment** — not required; harms the clean-checkout goal.
- **Add distributed consensus or an event broker** — not required; the guarantees fit inside one SQLite transaction.
- **Build only the four operations and prove the rules** — the chosen scope.

**Decision:** No UI, authentication, authorization, KYC, multi-currency, Docker, Kubernetes, cloud deployment, distributed consensus, or event broker. The service stays on localhost. The README records these choices and the fifth-hour plan.

**Why:** The assignment is about correctness under concurrency, not surface area. Cutting the extras keeps the code small enough to review and the test suite focused on the four rules. The localhost constraint is a direct consequence of having no auth: anyone who can reach the API can open a funded account.

**Evidence:** The [README](../README.md) has “What I chose not to build” and “What I would do next.” The [build log](BUILD_LOG.md) records the same trade-offs. The API surface is only the four required routes.

**Consequences:** The submission is not a production banking service. Account opening is not idempotent, history is unpaginated, keys never expire, and there is one writer. Those are disclosed rather than presented as finished production behavior.

**Revisit when:** The exercise becomes a real product; then auth, funding policy, retention, observability, and a production database come first.

---

## D17 — Known risks and deferred work

**Status:** Revisit · **Date:** 2026-10-20

This record is the honest risk register. None of these are hidden in the code; each has a user-visible consequence and a next step.

| Risk | Current behavior | Why it is acceptable for this exercise | Next step |
| --- | --- | --- | --- |
| **Uncertain `COMMIT`** | A `COMMIT` error returns a generic `500`. The transfer may have committed. | Retrying with the **same key and payload** is safe and documented. The gap is only dangerous if a client invents a new key after a 500. | On a commit error, read the idempotency key on a fresh connection and return the stored transfer if it exists; otherwise return a distinct retryable error. |
| **Account opening is not idempotent** | `POST /accounts` always inserts a new funded account. | The brief does not require idempotent opening. The README tells the caller to check before retrying. | Add a client idempotency key and a unique constraint. |
| **History is unbounded** | `History` loads every matching transfer and sorts in memory. | Fine for a local demo with a seeded 500-transfer audit. | Add cursor pagination and a bounded query. |
| **Idempotency keys never expire** | Successful keys live for the life of the database. | Correctness favors permanent keys; storage is small in the demo. | Define a retention policy and a cleanup job, with a documented replay window. |
| **Single SQLite writer** | Writes serialize through one database writer. | This is the mechanism that makes the race proof simple and reliable. | Move to PostgreSQL row locks for multiple nodes or higher write throughput. |
| **No authentication or funding policy** | Anyone who can reach the API can open a funded account. | Explicitly out of scope; the server binds to localhost. | Add auth, authorization, KYC, and a real funding source before exposure. |
| **No migrations** | Schema is created with `CREATE TABLE IF NOT EXISTS` at startup. | A fresh checkout needs no migration step. | Add versioned migrations before changing the schema in production. |
| **Duplicate JSON keys** | `encoding/json` accepts duplicates; the last value wins. | Low risk for a local demo with a small contract. | Reject duplicates with a streaming decoder or a custom unmarshaler. |
| **JavaScript precision above `2^53`** | Amounts are JSON integers; JS clients lose precision at large values. | The demo uses small cent values and PowerShell/Go clients. | Document string encoding for amounts or cap the maximum. |
| **`os.Exit` skips defers** | Fatal startup errors exit without a clean store close. | The process is terminating; SQLite recovers on the next open. | Return an exit code from `main` and close explicitly. |

The uncertain-commit row is the one I would fix first. It is the only item that can turn a transport failure into a double charge if the caller changes the key. The README and [build log](BUILD_LOG.md) state the same-key retry contract so a reviewer is not surprised.

---

## D18 — Rejected alternatives register

**Status:** Reference · **Date:** 2026-10-20

A quick index of suggestions that looked plausible and were deliberately not shipped.

| Alternative | Why it was rejected | Where discussed |
| --- | --- | --- |
| `SELECT ... FOR UPDATE` with sorted account IDs | SQLite has no row locks and rejects the syntax; sorting IDs does not create a lock. `BEGIN IMMEDIATE` is the real mechanism. | D08; [build log](BUILD_LOG.md) |
| **Floating-point or decimal-library money** | Floating point is inexact; a decimal library adds a second representation for no benefit when integer cents are exact. | D03 |
| **Docker + PostgreSQL** | Adds a service and credentials to a four-operation exercise; harms the clean-checkout goal. | D04, D16 |
| **A generic transfer retry loop** | No observed `SQLITE_BUSY` failure required it; `busy_timeout` handles normal contention, and retry machinery can hide a double-spend. | D09 |
| **A payload hash for idempotency** | The payload is three fields; direct comparison is simpler and produces a clearer conflict. | D10 |
| **A separate idempotency table** | Another table and lifecycle to keep in sync; the transfer row already owns the key. | D10 |
| **Reserving a key on a failed attempt** | A rejected transfer must not block a later retry with the same key. | D10 |
| **Sorting RFC3339Nano timestamps as text** | Variable-width fractions sort incorrectly; parse to instants instead. | D11 |
| **A single pooled connection** | It makes WAL readers wait behind a writer; eight connections with per-connection PRAGMAs preserve read availability. | D12 |
| **An in-memory store** | Loses durability and weakens the cross-connection race proof. | D04 |
| **An ORM or HTTP framework** | Four routes and a narrow `database/sql` surface do not justify the dependency or the indirection. | D02 |
| **Client-supplied account IDs** | Adds a duplicate-ID path the brief does not require. | D06 |
| **A UI, auth, KYC, multi-currency, or event broker** | Out of scope for the four-hour cap; correctness work comes first. | D16 |

---

## Maintaining this log

To add a decision:

1. Take the next `DNN` number and copy the template.
2. State the context and at least two real alternatives, including the cost of each.
3. Link the decision to a commit, a test, or a measurement; a choice without evidence is an opinion.
4. Record the consequence and the condition that would make you revisit it.
5. If a later decision replaces an earlier one, mark the earlier record `Superseded` and link forward instead of rewriting it.

The goal is not a perfect decision history. It is a history a reviewer can audit: what was chosen, what was rejected, what proved it, and what remains uncomfortable.

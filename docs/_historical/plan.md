# KOHO Move the Money Implementation Plan

> **Historical pre-build plan.** This records the decisions and proposed tests before implementation. Its unchecked tasks and setup statements are intentionally preserved as planning evidence, not current project status. See the [README](../../README.md), [build log](../BUILD_LOG.md), and [implementation journal](../IMPLEMENTATION_JOURNAL.md) for the shipped behavior and actual verification.

> **For an implementation session:** Work through the checked tasks in order, testing and committing each useful slice. The source documents, not this plan alone, remain the requirements. The user has asked for a plan only; implementation is a separate step.

**Goal:** Build a small Go service that opens accounts, transfers exact cents, returns balances and history, and proves that concurrent requests cannot break the four money rules.

**Architecture:** A standard-library HTTP API calls a small ledger package backed by one local SQLite database. Every successful transfer debits, credits, and writes its audit records inside one write transaction. SQLite serializes writers; the database constraints and transaction logic provide the financial guarantees, while tests exercise the service through both the ledger and HTTP boundaries.

**Tech stack:** Go 1.22+, `net/http`, `database/sql`, `log/slog`, `testing`, `modernc.org/sqlite` (pure Go), SQLite WAL mode. Confirm the installed Go toolchain and pin the actual module version during implementation.

**Sources read:** `Koho-assignment.txt` (the hiring assignment), `koho_definition_of_done_and_execution_spec.md` (expanded acceptance criteria), `codex_repository_instructions_agents_md.md` (proposed Go implementation rules), and `build-log-TEMPLATE.md` (submission log format). The directory has no application code, `go.mod`, tests, or Git commits yet.

## Requirements and decisions

- The original assignment requires four operations: open an account with a starting balance, send money, read balance, and read transaction history. It accepts an API, CLI, or library; this plan chooses HTTP because the supporting documents choose it and it is easy to demo.
- The non-negotiable rules are: balances never go negative, transfers are all or nothing, repeated transfers apply once, and amounts remain exact. A synchronized test of two requests competing for the same funds is the highest-priority proof.
- Represent every amount and balance as signed `int64` cents, including JSON and SQL. Reject decimals, numeric strings, zero/negative transfer amounts, negative initial balances, and values outside `int64`. Check credit overflow before updating a balance. No floating-point conversions.
- Use one currency, local storage, and no UI, authentication, signup, deployment, or distributed infrastructure. Record these scope choices in the README.
- Choose generated string IDs for accounts and transfers. The client does not supply account IDs in the first version, avoiding a duplicate-ID API path that the original brief never requires. Use UTC timestamps and stable ordering by `(created_at, id)` for history.
- Require a non-empty idempotency key for a transfer; cap its length at 255 bytes. The key identifies a successful transfer globally. A repeated key with identical source, destination, and amount returns the original transfer without a second debit; a repeated key with different fields returns HTTP 409. Invalid or unsuccessful attempts do not reserve the key. Document this contract.
- A successful initial balance is the account's opening balance, not a transfer. History lists transfers, and balance reconstruction uses `initial_balance_cents + credits - debits`. Store `initial_balance_cents` separately so that audit queries can prove this relationship.
- Every successful transfer creates **two** immutable ledger entries: sender `-amount_cents`, recipient `+amount_cents`. Therefore 100 successful transfers imply 200 entries; the expanded spec's phrase “100 transfers” beside “total ledger entries” is inconsistent.
- SQLite has a database-level single-writer model, so `SELECT ... FOR UPDATE` and row-lock ordering are not SQLite mechanisms. Acquire the write transaction before reading balances with `BEGIN IMMEDIATE` on a dedicated `*sql.Conn`; use a conditional sender debit and a checked recipient credit within it. If a future PostgreSQL version adds row locks, acquire account locks in sorted ID order there. Do not claim lexicographic row locking is implemented in SQLite.
- The expanded spec's fixed “100 requests under 2 seconds” target and “fresh clone under 60 seconds” are useful measurements, not correctness gates: machine load and first-time module download make them unreliable as hard CI thresholds. Record timings and investigate outliers without hiding failures.
- Prefer one coherent six-slice sequence below. The build-log template names different slices; adapt its headings to actual work rather than claiming a sequence that did not happen. Do not invent an AI mistake or a benchmark result.

## File map and responsibilities

| Path | Responsibility |
| --- | --- |
| `go.mod`, `go.sum` | Pin Go version and the SQLite driver. |
| `cmd/server/main.go` | Open the database, configure logging/server timeouts, start HTTP server, shut down cleanly. |
| `internal/ledger/model.go` | Request/result types and typed domain errors. |
| `internal/ledger/store.go` | Open/configure SQLite, create versioned schema, account creation, balance/history queries. |
| `internal/ledger/transfer.go` | Short atomic transfer transaction and idempotency lookup; keep this easy to inspect and modify live. |
| `internal/ledger/store_test.go` | Persistence, validation, replay, rollback, and ledger invariant tests. |
| `internal/ledger/concurrency_test.go` | Start-gated competing transfers, retries, bidirectional traffic, and conservation audit. |
| `internal/api/handler.go` | Routes, strict JSON decoding, domain-error to HTTP mapping, response encoding. |
| `internal/api/handler_test.go` | HTTP contract and status tests using `httptest`. |
| `README.md` | Setup, run, tests, example requests, architecture, limits, fifth-hour work. |
| `TECH_STACK_DECISIONS.md` | Go/SQLite rationale and alternatives. |
| `BUILD_LOG.md` | One-page authentic record based on `build-log-TEMPLATE.md`. |

Use a small, explicit interface at the API boundary, for example `OpenAccount(ctx, initialCents)`, `Balance(ctx, accountID)`, `History(ctx, accountID)`, and `Transfer(ctx, TransferRequest)`. Keep SQL transaction handling behind the ledger package. Exact Go signatures can be fixed when the first tests establish the return values; avoid adding a generic repository framework.

## Database and transfer contract

1. Create `accounts(id TEXT PRIMARY KEY, initial_balance_cents INTEGER NOT NULL, balance_cents INTEGER NOT NULL, created_at TEXT NOT NULL, ...)` with non-negative checks on both amounts. Prefer SQLite `STRICT` tables if supported by the pinned SQLite version; otherwise add explicit `typeof(...) = 'integer'` checks. Do not silently accept REAL or TEXT amounts through SQLite affinity.
2. Create `transfers(id TEXT PRIMARY KEY, idempotency_key TEXT NOT NULL UNIQUE, from_account_id TEXT NOT NULL REFERENCES accounts(id), to_account_id TEXT NOT NULL REFERENCES accounts(id), amount_cents INTEGER NOT NULL CHECK(amount_cents > 0), created_at TEXT NOT NULL, CHECK(from_account_id <> to_account_id))`. Store the request fields directly for replay comparison; a payload hash adds no value for three short fields.
3. Create `ledger_entries(id, transfer_id, account_id, amount_cents, created_at)` with foreign keys and an index on `(account_id, created_at, id)`. The application inserts exactly one debit and one credit per successful transfer in the same transaction. Add a unique `(transfer_id, account_id)` constraint; the two accounts must differ. Database constraints are backstops, while the transaction tests prove the pair and sum.
4. On every SQLite connection used for writes, enable foreign keys and a busy timeout; set WAL mode at database startup. Verify the driver/DSN behavior so connection-scoped PRAGMAs apply to every pooled connection. Use a file-backed temporary database in concurrency tests; a process-private `:memory:` database can split across connections.
5. A transfer validates IDs, distinct accounts, positive amount, and key; obtains a dedicated `*sql.Conn`; executes `BEGIN IMMEDIATE`; checks for an existing key **before** checking current balances; compares request fields; verifies both accounts exist; conditionally debits the sender; checks recipient overflow and credits; inserts the transfer and two entries; commits. On every error or canceled context before commit, roll back. Handle `SQLITE_BUSY` as a bounded retry of the entire transaction, if the chosen driver and busy timeout do not already wait reliably; never retry only a half-completed statement.
6. Define the commit boundary clearly. If commit succeeds, return the persisted transfer. If commit's outcome is uncertain, look up the idempotency key before reporting a retryable error. Never claim failure after a commit that may have succeeded without advising the caller to replay the same key.
7. A failed transfer leaves balances, transfers, and entries unchanged. If inserting a ledger entry fails after the debit, rollback must restore everything. Test this through a narrow failure hook or an SQL constraint failure that can be induced in a test, then remove any production-only fault-injection surface that is not needed.

## HTTP contract

| Route | Request | Success | Main errors |
| --- | --- | --- | --- |
| `POST /accounts` | `{ "initial_balance_cents": 10000 }` | `201` with ID and balance | `400` invalid JSON/amount |
| `GET /accounts/{id}/balance` | No body | `200` with ID and `balance_cents` | `404` missing account |
| `GET /accounts/{id}/transactions` | No body | `200` with ordered incoming/outgoing transfers | `404` missing account |
| `POST /transfers` | IDs, positive `amount_cents`, `idempotency_key` | `201` new transfer; `200` identical replay, same transfer ID | `400` invalid input/self-transfer; `404` missing account; `409` key mismatch; `422` insufficient funds or recipient overflow |

Set a small request-body limit. Decode a single JSON object with `DisallowUnknownFields`, then require EOF so trailing JSON is rejected. Decode monetary fields directly into `int64`, which rejects fractional, exponent, string, and out-of-range JSON numbers. Return stable JSON error codes such as `invalid_request`, `account_not_found`, `insufficient_funds`, and `idempotency_payload_mismatch`; reserve `500` for unexpected storage failures. Keep API tests at the boundary so a domain test alone cannot hide incorrect status mapping.

## Four-hour execution schedule

Budget approximately 15 minutes for setup, 35 minutes for schema/accounts, 60 minutes for transfer/idempotency, 60 minutes for the central concurrency proof and other invariants, 35 minutes for HTTP, and 35 minutes for docs, demo, and review. The schedule is a cap, not a reason to weaken a correctness gate. If time runs short, keep a well-tested ledger plus clear README and build log; cut optional storm sizes, performance tuning, and extra endpoints before cutting the two-request race proof.

### Slice 1 — Bootstrap, schema, and exact account values

**Files:** `go.mod`, `go.sum`, `internal/ledger/model.go`, `internal/ledger/store.go`, `internal/ledger/store_test.go`.

- [ ] Check `go version`; initialize the module and pin the SQLite driver. Add a single `Open`/`Close` path that creates a file-backed database and schema, enables WAL, foreign keys, and busy timeout, and verifies the PRAGMAs from actual connections.
- [ ] Write failing tests for account creation at 0 and at `math.MaxInt64`, rejection of negative opening balance, and database constraints against negative/non-integer balances. Add a reopen test to prove persistence.
- [ ] Implement account creation and typed errors. Store both opening and current balance. Give each account a generated ID and UTC creation time.
- [ ] Run `go fmt ./...`, `go vet ./...`, and `go test -race -count=1 ./...`; capture exact output. Commit this slice with an honest subject and verification in the message body.

### Slice 2 — Balance and ordered history reads

**Files:** `internal/ledger/store.go`, `internal/ledger/store_test.go`.

- [ ] Write failing tests that a new account reports the exact opening balance, an unknown ID returns the domain not-found error, and a new account has empty transfer history.
- [ ] Implement `Balance` and `History`, including an account-existence check before an empty history response. Use a deterministic `(created_at, id)` order; validate with two records sharing a timestamp once transfers exist.
- [ ] Run formatting, vet, and race tests. Commit only the verified read behavior.

### Slice 3 — Atomic transfer and rollback

**Files:** `internal/ledger/transfer.go`, `internal/ledger/store_test.go`.

- [ ] Write failing tests for a successful 25-cent transfer, insufficient funds, zero/negative amount, self-transfer, missing source/destination, receiver overflow, and forced failure between debit and credit. Assert both balances and both ledger rows after success; assert no changes after failure.
- [ ] Implement `BEGIN IMMEDIATE` on a dedicated connection and a deferred rollback. Read the recipient balance, guard `MaxInt64 - amount`, execute a conditional sender debit (`balance_cents >= amount`), credit the recipient, insert transfer plus exactly two signed entries, then commit. Check affected-row counts and map failures to typed errors.
- [ ] Audit the transaction path for context cancellation and every early return. Run formatting, vet, and race tests, then commit with the exact successful test command in the message.

### Slice 4 — Idempotency and the decisive concurrency proof

**Files:** `internal/ledger/transfer.go`, `internal/ledger/store_test.go`, `internal/ledger/concurrency_test.go`.

- [ ] Write failing tests for identical sequential replay (same transfer ID, balances unchanged), same-key payload mismatch (domain conflict), and replay after the sender's balance has fallen below the amount. Make key lookup happen before balance validation.
- [ ] Add a start-gated **two-request** test: one account has 100 cents; two distinct keys concurrently send 100 cents to a recipient. Expect exactly one success, one insufficient-funds result, source 0, recipient 100, one transfer, and two ledger entries. Use separate goroutines and a barrier channel; test through the real SQLite file and real store connections.
- [ ] Implement replay and conflict checks inside the write transaction. If many writers expose `SQLITE_BUSY`, add bounded whole-transaction retry with context-aware backoff and re-run the same tests. Avoid sleeps as a substitute for the barrier.
- [ ] Add a 20-caller same-key barrier test: all receive the same transfer ID; one mutation and two entries exist. Run focused tests repeatedly (for example `go test -race -count=10 -run 'TestConcurrent|TestIdempotency' ./internal/ledger`) plus full format/vet/race gate. Commit the verified behavior.

### Slice 5 — HTTP API and higher-contention audit

**Files:** `internal/api/handler.go`, `internal/api/handler_test.go`, `internal/ledger/concurrency_test.go`, `cmd/server/main.go`.

- [ ] Write failing `httptest` cases for every route's success and error statuses, strict monetary JSON (`10.5`, `1e2`, `"100"`, `0`, negatives, overflow), unknown fields, trailing JSON, and identical replay returning the same ID.
- [ ] Implement the four routes and server entrypoint. Add a short manual demo sequence to exercise open, transfer, balance, and history on a persisted database.
- [ ] Expand contention tests if time allows: 50 requests for the full balance (1 success/49 insufficient), 20 partial drains (10/10), 100 bidirectional transfers (balances conserved, 100 transfers/**200 entries**), and a seeded 500-attempt audit across 10 accounts. For every audit, assert no negative balance, global balance conservation, per-account reconstruction from opening balance and entries, and each transfer's entry sum equals zero. Seed the random input for reproducibility.
- [ ] Run formatting, vet, and full race suite. Repeat the focused concurrency suite enough times to expose flaky locking; investigate any busy error or timeout rather than accepting a lower success count. Commit the tested API and audit work, splitting into two commits if each reaches a distinct verified checkpoint.

### Slice 6 — Submission and interview readiness

**Files:** `README.md`, `TECH_STACK_DECISIONS.md`, `BUILD_LOG.md`; fix code/tests only if review finds a real defect.

- [ ] Write README with exact `go run ./cmd/server` and `go test -race -count=1 ./...` commands, route examples, four-rule explanation, and intentional omissions. Include a short “fifth hour” section.
- [ ] Document why Go and SQLite fit the four-hour local exercise, SQLite's one-writer ceiling and busy handling, how PostgreSQL row locks would differ, and realistic migration triggers. Describe the actual chosen implementation, not hypothetical features as shipped.
- [ ] Convert the template into a roughly one-page build log. Record what AI accelerated, actual rejected suggestions, real test failures/incorrect AI assumptions and their fixes, and any known limitation. If no confident AI error occurs, state that honestly and describe the strongest reviewed risk instead of fabricating an anecdote.
- [ ] From a clean checkout or fresh temporary directory, run setup, full tests, and a manual API smoke test. Run `go fmt ./...`, `go vet ./...`, `go test -race -count=1 ./...`, `git diff --check`, and inspect status/history. Prepare a 10-minute demo covering one transfer, one replay, one competing-transfer test, and one design trade-off.
- [ ] Commit the final documentation and any separately verified fixes. Submission of a remote repository or bundle is an external handoff to arrange after the user reviews the finished project.

## Review focus and stop conditions

The tests above must also pin five likely failure modes: simultaneous distinct-key transfers, simultaneous same-key retries, recipient overflow at `MaxInt64`, a failure after the first mutation, and a read/history query concurrent with writes. The last one should assert that a response represents a committed snapshot and never a half-transfer. Test on the file-backed database, not an in-memory stand-in.

Stop and investigate any race detector finding, `SQLITE_BUSY` leaking as a 500 under expected contention, balance/ledger mismatch, inconsistent replay ID, or test that only passes with timing sleeps. The original assignment gives the two competing transfers the most scrutiny, so ship that proof even if the optional larger stress tests must be deferred under the four-hour limit.

## Self-review against the source documents

- **Assignment:** all four operations, four invariants, competing-transfer proof, Git history, README, one-page build log, scope disclosure, and interview demo are mapped above.
- **Expanded specification:** strict cents, double-entry audit, idempotency mismatch, rollback, conservation tests, and advanced contention cases are mapped above. Its absolute speed targets are measurements; its ledger-entry count is corrected to two per transfer.
- **Go guidance:** Go 1.22+, pure-Go SQLite, standard-library HTTP, WAL/foreign keys/busy timeout, race tests, incremental commits, and honest AI telemetry are mapped above. The SQLite implementation deliberately uses a write transaction, not unsupported `FOR UPDATE` row locks.
- **Template:** reuse its questions and authenticity requirement; align slice headings with the actual commit history.

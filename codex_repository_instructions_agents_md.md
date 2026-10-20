# AGENTS.md — Codex Instructions for KOHO "Move the Money" (Go Stack)

> **Historical agent-instruction draft.** This file was research/setup input, not an active `AGENTS.md` or an exact record of shipped code. In particular, its PostgreSQL-style lock ordering and some proposed gates were revised for SQLite. See the [README](README.md) and [implementation journal](docs/IMPLEMENTATION_JOURNAL.md) for the final decisions.

Read this file and `DEFINITION_OF_DONE.md` before proposing or modifying any code.

## 1. Mission & Non-Negotiable Invariants
We are building a small, lock-safe banking ledger in **Go (1.22+)** backed by **SQLite (`modernc.org/sqlite` pure-Go driver in WAL mode)**.

Every change you make must preserve these four financial invariants:
1. **No Negative Balances (`balance_cents >= 0`):** An account balance can never go negative under any concurrent sequence of requests.
2. **All-or-Nothing Atomicity:** Debit, credit, immutable double-entry ledger records, and idempotency key registration must execute inside a single ACID transaction (`BEGIN IMMEDIATE ... COMMIT`).
3. **Strict Idempotency:** Submitting the same `idempotency_key` multiple times (sequentially or concurrently) applies the transfer **at most once**. Reusing an `idempotency_key` with different parameters (`from_account_id`, `to_account_id`, or `amount_cents`) must return `409 Conflict` (`ErrIdempotencyPayloadMismatch`).
4. **Exact Integer Cents (`int64`):** All monetary values are signed 64-bit integer cents (`int64`). Never use `float32`, `float64`, or imprecise parsing. Reject non-integer JSON numbers (`10.5`), strings (`"100"`), negative amounts, zero transfer amounts, and `int64` overflow.

## 2. Architecture & Technical Constraints (Keep It Lean)
- **Zero CGo Friction:** Use `modernc.org/sqlite` (pure Go SQLite driver) so `go test -race ./...` works without C compiler headaches, or document if a specific driver flag is used.
- **SQLite Concurrency Discipline:**
  - Enable `PRAGMA journal_mode = WAL;`, `PRAGMA foreign_keys = ON;`, and `PRAGMA busy_timeout = 5000;`.
  - Write transactions **must** acquire a write lock before reading balances (e.g., `BEGIN IMMEDIATE` or a dedicated serialized write transaction wrapper + database-level conditional `UPDATE accounts SET balance_cents = balance_cents - ? WHERE id = ? AND balance_cents >= ?`).
  - Lock/query accounts in canonical lexicographical order (`min(fromID, toID)` then `max(fromID, toID)`) so the design is 100% deadlock-free and portable to PostgreSQL `SELECT ... FOR UPDATE`.
- **No Framework Bloat:** Use Go standard library `net/http` (Go 1.22+ routing), `database/sql`, `log/slog`, and `testing`. Keep the entire core ledger logic under 250 lines of crystal-clear Go code so a human engineer can modify it live in 15 minutes.
- **Directory Layout:**
  ```text
  .
  ├── cmd/server/main.go           # HTTP entrypoint & graceful shutdown
  ├── internal/ledger/             # Core domain types, SQLite store, atomic transfer logic
  │   ├── model.go                 # Domain types, errors, integer validation
  │   ├── store.go                 # Schema, migrations, ACID transactions
  │   └── concurrency_test.go      # Barrier race tests, deadlock storm, property audit
  ├── internal/api/                # HTTP handlers & strict JSON validation
  │   ├── handler.go
  │   └── handler_test.go          # E2E HTTP tests
  ├── DEFINITION_OF_DONE.md
  ├── TECH_STACK_DECISIONS.md
  ├── BUILD_LOG.md
  └── README.md
  ```

## 3. Execution Protocol (Incremental Slices Only)
- **NEVER one-shot the entire project in a single step or a single commit.**
- Work strictly one slice at a time as instructed by the user.
- Before writing code for a slice:
  1. State the slice objective and files to touch.
  2. Implement the minimal, high-integrity code and tests for that slice.
  3. Run the verification commands (`go vet ./...` and `go test -v -race ./...`).
  4. Log any subtle edge cases, SQLite locking nuances, or rejected approaches in `BUILD_LOG_NOTES.md` (or report them clearly in your handoff) so we have authentic, concrete material for `BUILD_LOG.md`.
  5. Stop and wait for approval or commit only the current slice with an honest commit message.

## 4. Verification Commands (Mandatory Gate Before Every Commit)
Every slice must pass all of the following before completion:
```bash
go fmt ./...
go vet ./...
go test -v -race -count=1 ./...
```

## 5. Handoff Format After Each Slice
At the end of every task, report:
1. **Files Created/Modified**
2. **How Invariants Are Enforced** (specific SQL constraints, locks, or checks added)
3. **Verification Output** (exact result of `go test -v -race ./...`)
4. **AI / Engineering Catch Log** (any trap avoided or edge case discovered during this slice for our `BUILD_LOG.md`)
5. **Proposed Git Commit Message**

## 7. Git Commit & `BUILD_LOG.md` Telemetry Protocol

KOHO reviewers explicitly inspect the Git commit history and `BUILD_LOG.md` to see how we think, where AI tools helped, and where AI output was wrong or rejected. Follow these rules on every slice:

### Commit Rules (Incremental & Honest)
- **ALWAYS** run `go vet ./...` and `go test -race -count=1 ./...` before staging or committing.
- **ALWAYS** keep commits scoped to a single slice or a single bug fix.
- **ALWAYS** write multi-line commit messages that state:
  1. Subject line: `<type>(<scope>): <concise summary>` (e.g., `feat(ledger): enforce atomic double-entry transfers`).
  2. Body bullet 1 (`Why`): Which invariant (Rule 1–4) or slice requirement this satisfies.
  3. Body bullet 2 (`Verified by`): The exact test name(s) and command run (`go test -race -v -run TestName ./...`).
- **WHEN A BUG OR TEST FAILURE OCCURS**: Do **NOT** silently hide the mistake. Make the fix in a dedicated, honest commit (or document the failed attempt in the commit body and `BUILD_LOG.md`) so the commit history reflects real engineering iteration.
- **NEVER** run `git commit --amend`, `git rebase`, `git reset --hard`, or `git push --force`. History must remain strictly linear and authentic.

### Live `BUILD_LOG.md` Update Rules
Before finishing any slice, **append** a brief telemetry entry to `BUILD_LOG.md` under `## 2. Slice-by-Slice Execution & AI Telemetry`:
- Record what Codex generated cleanly on the first pass.
- Record **any wrong assumption, failed test, compiler error, or SQLite lock/concurrency issue** encountered during the slice, along with the exact root cause and fix.
- Record any complexity or pattern that was considered/suggested and rejected to stay within the 4-hour scope.
- **NEVER** fabricate bugs and **NEVER** write "everything worked on the first try." Be clinically honest about every edge case caught by tests or human review.

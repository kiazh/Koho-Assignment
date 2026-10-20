# KOHO "Move the Money" — Build Log

> **Unused starting template.** Its placeholders and numerical examples are prompts, not evidence of what happened. The completed [build log](BUILD_LOG.md) and [implementation journal](IMPLEMENTATION_JOURNAL.md) contain the actual record.

## 1. What I Set Out to Do (Scope & Strategy)
- **Goal**: Build a concurrency-safe, double-entry financial ledger and HTTP API in Go + SQLite (`modernc.org/sqlite`) within the 4-hour cap that provably satisfies all four money invariants:
  1. No negative balances under any concurrent request sequence.
  2. All-or-nothing atomic transfers with zero-sum double-entry ledger entries.
  3. Strict idempotency across network retries and concurrent double-clicks (`409 Conflict` on payload mismatch).
  4. Exact integer-cent arithmetic (`int64`) with overflow protection.
- **Workflow Strategy**: Research-first specification (`DEFINITION_OF_DONE.md`, `TECH_STACK_DECISIONS.md`), strict repository guardrails (`AGENTS.md`), Red-Green-Refactor TDD slices using Codex, and an independent fresh-context adversarial audit (`codex /review`).

---

## 2. Slice-by-Slice Execution & AI Telemetry
*(Updated live after each slice commit)*

### Slice 1: Storage Bootstrap & Account Creation
- **Commit**: `[pending]`
- **Where AI helped**: 
- **Where AI was wrong / needed correction**: 
- **What I rejected**: 

### Slice 2: Atomic Transfers & Double-Entry Ledger
- **Commit**: `[pending]`
- **Where AI helped**: 
- **Where AI was wrong / needed correction**: 
- **What I rejected**: 

### Slice 3: Idempotency & Payload Conflict Detection
- **Commit**: `[pending]`
- **Where AI helped**: 
- **Where AI was wrong / needed correction**: 
- **What I rejected**: 

### Slice 4: Rule 1 Concurrency Barrier Proof (`50` Competing Transfers)
- **Commit**: `[pending]`
- **Where AI helped**: 
- **Where AI was wrong / needed correction**: 
- **What I rejected**: 

### Slice 5: HTTP API & End-to-End Smoke Tests
- **Commit**: `[pending]`
- **Where AI helped**: 
- **Where AI was wrong / needed correction**: 
- **What I rejected**: 

### Slice 6: Fresh-Context Adversarial Audit (`/review`)
- **Commit**: `[pending]`
- **Defects surfaced by adversarial review**: 
- **How they were reproduced & fixed**: 

---

## 3. Highlight: Where the AI Was Confidently Wrong (And How I Caught It)
*(Summary of the most critical correctness failure caught during the build for the 10-minute interview walkthrough)*
- **The Symptom / Bad Suggestion**: 
- **Why It Looked Right**: 
- **How I Caught It (Test / Invariant Check)**: 
- **The Root-Cause Fix**: 

---

## 4. What AI Suggested That I Rejected (And Why)
- **Rejected Suggestion 1**: `SELECT ... FOR UPDATE` row locking (suggested by standard SQL habits/skills) — rejected because SQLite uses pager/WAL locking and throws a syntax error on `FOR UPDATE`.
- **Rejected Suggestion 2**: Floating-point/string decimal libraries (`shopspring/decimal`) — rejected in favor of pure `int64` minor units (cents) to keep arithmetic deterministic, dependency-free, and directly enforceable via SQLite integer `CHECK` constraints.
- **Rejected Suggestion 3**: Multi-container Docker + Postgres setup — rejected so reviewers can clone and run `go test -race ./...` in 5 seconds with zero infrastructure setup.

---

## 5. What I Shipped That I Am Not Fully Comfortable With (And Hour 5 Plan)
1. **Single-Writer SQLite Throughput Ceiling**: Serializing writes onto a single SQLite WAL writer guarantees strict serializability on a single node without `SQLITE_BUSY` deadlocks, but caps write throughput at ~2,000–5,000 TPS and prevents horizontal multi-node scaling.
   - *Hour 5 Fix*: Migrate the `Store` interface to PostgreSQL using deterministic ascending lock acquisition (`SELECT ... FROM accounts WHERE id IN ($1, $2) ORDER BY id ASC FOR UPDATE`).
2. **Unbounded Idempotency Key Retention**: Idempotency keys are stored permanently in the `transfers` table without a TTL eviction job or payload hash expiration.
   - *Hour 5 Fix*: Add `expires_at` indexing and a background cleanup worker for keys older than 24–72 hours.
3. **Synchronous Balance Denormalization**: `accounts.balance_cents` is updated synchronously alongside `ledger_entries`.
   - *Hour 5 Fix*: Add a periodic background reconciliation query verifying `accounts.balance_cents == SUM(ledger_entries.amount_cents)` across all accounts.

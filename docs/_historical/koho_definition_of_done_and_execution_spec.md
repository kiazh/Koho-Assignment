# KOHO "Move the Money" — Definition of Done & Execution Specification

> **Historical research/spec input.** This deliberately explores a larger design than the four-hour assignment. It includes proposed PostgreSQL row locks and optional features that are not in the Go/SQLite implementation. The original assignment is authoritative; the [README](../../README.md) and [build log](../BUILD_LOG.md) record the delivered scope.

## 1. Executive Summary & Core Philosophy
This document defines what "Completed" looks like for the KOHO **Move the Money** engineering assignment. 

The assignment is deliberately small in feature surface area, but uncompromising in **correctness under concurrency, fault tolerance, and financial invariants**. Our goal is not to build bloated banking scaffolding (no UI, no auth, no microservices), but rather a high-integrity, lock-safe ledger engine accompanied by deterministic proof that it never loses a cent, never double-spends, and never allows an account balance to go negative—even under adversarial, simultaneous requests.

Equally important is the **meta-assessment**: demonstrating complete mastery over our AI coding tools (Codex), maintaining an honest and granular Git commit history, and producing a high-signal `BUILD_LOG.md` that captures where the AI tried to lead us astray (e.g., naive read-then-write race conditions, floating-point math, or broken lock ordering) and how we caught and proved it.

---

## 2. Explicit Scope & Non-Goals (4-Hour Timebox Discipline)

### In-Scope (Must Be 100% Complete & Verified)
1. **Open Account:** Create an account with an initial non-negative integer balance (in cents/minor units).
2. **Transfer Money:** Move a positive integer amount from a source account to a destination account with an idempotency key.
3. **Get Balance:** Retrieve the exact current balance of an account.
4. **Get Transaction History:** Retrieve the ordered, immutable audit trail of all transfers affecting an account.
5. **Adversarial & Concurrency Verification Suite:** Automated tests proving all 4 core rules hold under high concurrency, race conditions, retries, and mid-transaction failures.
6. **Submission Artifacts:** Clean Git history, `README.md`, `TECH_STACK_DECISIONS.md`, and `BUILD_LOG.md`.

### Explicit Non-Goals (Cut to Protect Correctness within 4 Hours)
* **No User Interface (UI):** Exposed strictly via a clean HTTP JSON API (and/or direct programmatic service interface).
* **No Authentication / Authorization / KYC:** Out of scope per instructions.
* **No Multi-Currency / FX Conversion:** Single currency (e.g., `CAD` or `USD` represented exclusively in integer minor units / cents).
* **No External Cloud Deployment / Kubernetes:** Must run locally with zero external friction (single command startup & test execution).
* **No Distributed Consensus / Event-Driven Brokers:** No Kafka, RabbitMQ, or 2-Phase Commit across microservices; ACID guarantees belong inside a single transactional boundary.

---

## 3. The Four Inviolable Rules (System Invariants)

Every line of code and every test gate exists to enforce and prove these four invariants:

### Invariant 1: Non-Negative Balance Under Any Concurrency
$$\forall a \in \text{Accounts}, \quad \text{Balance}(a, t) \ge 0 \quad \forall t$$
* **Requirement:** An account's balance can never drop below $0$ cents under any sequence of requests, including $N$ simultaneous requests arriving at the exact same millisecond competing for the same funds.
* **The AI Trap to Avoid:** AI assistants frequently write `SELECT balance FROM accounts WHERE id = ?`, check `if balance >= amount` in application memory, and then issue `UPDATE accounts SET balance = balance - ?`. Under `READ COMMITTED` isolation without row locking, two concurrent requests read the same balance and both succeed, driving the balance negative.
* **Enforcement Mechanism:**
  1. Database-level constraint: `CHECK (balance_cents >= 0)` on the `accounts` table as the ultimate backstop.
  2. Deterministic row-level pessimistic locking (`SELECT ... FOR UPDATE` in sorted account ID order to prevent deadlocks) or atomic conditional update (`UPDATE accounts SET balance_cents = balance_cents - $1 WHERE id = $2 AND balance_cents >= $1`) inside a single ACID transaction.

### Invariant 2: Atomic Transfers (All-or-Nothing Conservation of Money)
$$\sum_{a \in \text{Accounts}} \text{Balance}(a, t_{\text{before}}) = \sum_{a \in \text{Accounts}} \text{Balance}(a, t_{\text{after}})$$
* **Requirement:** There is no state of the world where money leaves Account A and does not arrive in Account B, or where a ledger entry is recorded without the corresponding balance mutations.
* **Enforcement Mechanism:** Debit, credit, ledger entry insertion, and idempotency state recording **must** occur inside a single database transaction (`BEGIN` ... `COMMIT`), rolling back completely (`ROLLBACK`) if any step or invariant fails.

### Invariant 3: Strict Idempotency (At-Most-Once Execution)
* **Requirement:** Networks retry and users double-click. Submitting the identical transfer request (same `idempotency_key`) multiple times—whether sequentially or concurrently—must apply the financial mutation **at most once** and return the deterministic outcome of the original transfer.
* **Edge Cases Handled:**
  * *Sequential Retry:* Second request with the same `idempotency_key` and identical payload returns the cached response/transfer record without moving money again.
  * *Concurrent Duplicate Retry:* Two identical requests with the same `idempotency_key` arrive at the exact same microsecond. One acquires the idempotency lock/constraint; the other waits or catches the unique constraint violation and returns the exact same result without double-debiting.
  * *Idempotency Key Misuse (Payload Mismatch):* Reusing an existing `idempotency_key` with different parameters (different `from_account_id`, `to_account_id`, or `amount_cents`) must be rejected with `422 Unprocessable Entity` / `409 Conflict`.

### Invariant 4: Exact Arithmetic (Zero Floating-Point Drift)
* **Requirement:** If a single cent goes missing or is fabricated through rounding, the system is wrong.
* **The AI Trap to Avoid:** Using `float`, `double`, `number` (IEEE 754 floating point in JS/Python/etc.) where $0.10 + 0.20 = 0.30000000000000004$.
* **Enforcement Mechanism:**
  * All monetary values in the API, domain logic, and database are represented strictly as **64-bit signed integers (`BIGINT` / `int64`) representing minor units (cents)**.
  * Any non-integer input (e.g., `10.5`, `"10.99"`, `NaN`, `Infinity`) or non-positive transfer amount ($\le 0$) is rejected at the boundary validation layer before touching business logic.
  * Overflow checks ensure balances cannot exceed max safe 64-bit integer limits.

---

## 4. Functional Contracts & Data Model

### 4.1 Core Data Schema
1. **`accounts` Table**
   * `id`: `UUID` or string identifier (Primary Key)
   * `balance_cents`: `BIGINT NOT NULL CHECK (balance_cents >= 0)`
   * `created_at`: `TIMESTAMPTZ NOT NULL`
   * `updated_at`: `TIMESTAMPTZ NOT NULL`

2. **`transfers` Table (Immutable Ledger & Idempotency Registry)**
   * `id`: `UUID` (Primary Key)
   * `idempotency_key`: `VARCHAR(255) NOT NULL UNIQUE`
   * `from_account_id`: `UUID NOT NULL REFERENCES accounts(id)`
   * `to_account_id`: `UUID NOT NULL REFERENCES accounts(id)`
   * `amount_cents`: `BIGINT NOT NULL CHECK (amount_cents > 0)`
   * `created_at`: `TIMESTAMPTZ NOT NULL`
   * `CHECK (from_account_id <> to_account_id)`

3. **`ledger_entries` Table (Optional Double-Entry Detail / Audit View)**
   * Every transfer generates two balanced entries: `-amount_cents` on `from_account_id` and `+amount_cents` on `to_account_id`, satisfying $\sum \text{entries} = 0$, alongside `balance_after_cents` snapshots for fast, auditable history queries.

### 4.2 Operations & Edge-Case Behavior Matrix

| Operation | Input Contract | Happy Path | Required Rejections / Edge Cases |
| :--- | :--- | :--- | :--- |
| **1. Open Account** | `initial_balance_cents` (integer $\ge 0$), optional `id` | Creates account, records initial funding event if $>0$, returns `201 Created` with account state. | Negative starting balance (`400`), float/decimal input (`400`), duplicate account ID (`409`). |
| **2. Transfer Money** | `idempotency_key` (required string), `from_account_id`, `to_account_id`, `amount_cents` (integer $> 0$) | Atomically debits sender, credits receiver, records transfer & ledger entries, returns `201 Created` (or `200 OK` on idempotent replay). | Insufficient funds (`422`), `amount_cents <= 0` (`400`), `from == to` self-transfer (`400`), non-existent account (`404`), same `idempotency_key` with different payload (`409`). |
| **3. Get Balance** | `account_id` | Returns `200 OK` with `account_id` and exact `balance_cents`. | Non-existent `account_id` (`404`). |
| **4. Get History** | `account_id`, optional pagination (`limit`, `cursor`/`offset`) | Returns `200 OK` with chronological list of all incoming/outgoing transfers affecting the account. | Non-existent `account_id` (`404`). Must match current balance: `initial_balance + sum(incoming) - sum(outgoing) == current_balance`. |

---

## 5. Deadlock Prevention & Scalability Design

To ensure seamless performance under high contention without deadlocks or race conditions:

1. **Canonical Lock Ordering (Deadlock Immunity):**
   * Suppose Transfer 1 sends money from Account A $\rightarrow$ Account B at the exact moment Transfer 2 sends money from Account B $\rightarrow$ Account A.
   * If Transaction 1 locks A then waits for B, while Transaction 2 locks B then waits for A, a classic **AB-BA deadlock** occurs.
   * **Requirement:** Whenever locking two accounts in a transaction, the system **must sort the account IDs lexicographically** (`min(from_id, to_id)` locked first, `max(from_id, to_id)` locked second) before executing `SELECT ... FOR UPDATE` or mutating rows.
2. **Short Transaction Lifespans:**
   * Zero network calls, sleep timers, or heavy serialization inside the database transaction block.
3. **Indexed Lookups:**
   * Unique index on `transfers(idempotency_key)`.
   * Composite indexes on `transfers(from_account_id, created_at DESC)` and `transfers(to_account_id, created_at DESC)` (or `ledger_entries(account_id, created_at DESC)`) so transaction history queries remain $O(\log N)$ as history grows.
4. **Measurable Performance Targets (Local Benchmark Gate):**
   * 100 concurrent transfers competing for the same hot account must complete in $< 2\text{ seconds}$ with $0$ unhandled 500 errors, $0$ deadlocks, and $100\%$ mathematical conservation of funds.

---

## 6. Verification & Proof Strategy (The Most Scrutinized Deliverable)

The assignment explicitly states:
> *"At least one of your tests should demonstrate that rule 1 survives two transfers competing for the same money at the same time. How you construct that test is up to you, and it is the single thing we will look at most closely."*

Passing shallow unit tests is insufficient. The project is only "Done" when all five testing tiers pass deterministically:

### Tier 1: Deterministic Concurrency Barrier Test (The Crown Jewel Test)
* **Scenario A (Double-Spend Race):** Account A starts with `10000` cents ($100.00). Spin up $N = 50$ concurrent workers/threads synchronized via a **start barrier / countdown latch** so all 50 fire a transfer of `10000` cents to Account B at the exact same instant (with distinct idempotency keys).
  * **Required Proof:** Exactly **1** transfer succeeds (`201`). Exactly **49** transfers fail with `InsufficientFunds` (`422`). Account A's final balance is `0`. Account B's final balance is `10000`.
* **Scenario B (Partial Drain Contention):** Account A starts with `10000` cents. 20 concurrent workers each attempt to transfer `1000` cents ($10.00) simultaneously.
  * **Required Proof:** Exactly **10** succeed, **10** fail with `InsufficientFunds`, final balance of A is `0`, sum of recipient balances is `10000`.

### Tier 2: Bidirectional Deadlock Storm Test
* **Scenario:** Account A has `100000` cents, Account B has `100000` cents. 50 concurrent workers transfer `100` cents from A $\rightarrow$ B while 50 concurrent workers simultaneously transfer `100` cents from B $\rightarrow$ A.
* **Required Proof:** All 100 transfers succeed without a single deadlock error, timeout, or lost cent. Final balances: Account A = `100000`, Account B = `100000`. Total ledger entries = 100 transfers.

### Tier 3: Concurrent Idempotency Replay Storm
* **Scenario:** Account A has `50000` cents. 20 concurrent workers fire the **exact same transfer request** (`amount: 10000`, `idempotency_key: "retry-storm-key-1"`) at the exact same instant via a barrier.
* **Required Proof:** The money moves **exactly once**. Account A's final balance is `40000` cents (not `0`, and not negative). Account B's balance is `10000` cents. All 20 callers receive a consistent success representation of that single transfer ID.

### Tier 4: Atomicity & Fault-Injection Rollback Proof
* **Scenario:** Force an error mid-transaction (e.g., inject a failure after debiting Account A but before crediting Account B or inserting the transfer record).
* **Required Proof:** The transaction rolls back cleanly. Account A's balance remains untouched. No orphan ledger record exists.

### Tier 5: Property / Invariant Audit Check
* After running a randomized sequence of 500 concurrent valid and invalid transfers across 10 accounts:
  1. **Global Conservation Invariant:** $\sum \text{current\_balances} == \sum \text{initial\_balances}$.
  2. **Ledger Reconstruction Invariant:** For every account $a$, $\text{initial\_balance}(a) + \sum \text{credits}(a) - \sum \text{debits}(a) == \text{current\_balance}(a)$.
  3. **Non-Negativity Invariant:** $\forall a, \text{current\_balance}(a) \ge 0$.

---

## 7. Codex Workflow, Implementation Slices & Commit Strategy

Because the interviewers will inspect the Git commit history and walk through it for 10 minutes, **Codex must never one-shot the entire repository in a single commit**. Work is divided into 6 small, verifiable slices, each with its own commit and quality gate.

### Slice 1: Repository Scaffold, Domain Types & Exact Money Validation
* **Goal:** Set up project structure, database schema/migrations, integer-only money validation, and structured logging.
* **Gate:** Schema enforces `CHECK (balance_cents >= 0)` and `UNIQUE(idempotency_key)`. Unit tests reject floats, negative numbers, and zero transfers.
* **Commit Checkpoint:** `feat(domain): add schema with integer-cent invariants and strict input validation`

### Slice 2: Account Creation & Balance / History Queries
* **Goal:** Implement `open_account`, `get_balance`, and `get_transaction_history` with clean persistence.
* **Gate:** Integration tests verify creating accounts, querying balances, and 404/400 boundary conditions.
* **Commit Checkpoint:** `feat(accounts): implement account creation and balance/history queries`

### Slice 3: Atomic Transfer Engine & Canonical Lock Ordering
* **Goal:** Implement the core transfer execution inside an ACID transaction with lexicographical account lock ordering (`min(id)`, `max(id)`) to prevent AB-BA deadlocks and negative balances.
* **Gate:** Integration tests verify happy-path transfers, insufficient funds rejection, self-transfer rejection, and mid-transaction rollback.
* **Commit Checkpoint:** `feat(transfers): implement atomic transfer engine with canonical lock ordering`

### Slice 4: Idempotency State Machine & Payload Fingerprinting
* **Goal:** Enforce at-most-once execution per `idempotency_key`, handling both sequential replays and concurrent duplicate requests, plus payload mismatch detection (`409 Conflict`).
* **Gate:** Tests prove duplicate requests return the original transfer without double-charging, and mismatched payloads with the same key fail safely.
* **Commit Checkpoint:** `feat(idempotency): enforce at-most-once transfer execution and payload hash checks`

### Slice 5: Adversarial Concurrency Suite & Fresh-Context Review
* **Goal:** Build the synchronized barrier tests (50-way double-spend race, AB-BA deadlock storm, concurrent idempotency storm, and global conservation property test). Run a fresh-context adversarial model review to attempt to break the implementation.
* **Gate:** All concurrency stress tests pass 10/10 consecutive runs with `--race` / concurrency detectors enabled.
* **Commit Checkpoint:** `test(concurrency): add synchronized barrier race, deadlock, and idempotency stress tests`

### Slice 6: HTTP API Layer, Documentation & Recruiter Deliverables
* **Goal:** Expose clean HTTP endpoints (if not already wired), finalize `README.md`, `TECH_STACK_DECISIONS.md`, and `BUILD_LOG.md`.
* **Gate:** Fresh-clone commands (`setup`, `test`, `start`) execute cleanly in under 60 seconds.
* **Commit Checkpoint:** `docs(submission): add README, tech stack tradeoffs, and AI build log`

---

## 8. Required Submission Artifacts Checklist

Before considering the project complete, verify that the repository contains:

- [ ] **Clean, Honest Git Commit History:** 6–10 meaningful commits showing incremental engineering progression and test verification.
- [ ] **`README.md`:**
  - Exact one-liner commands to install, run the server, and run the concurrency test suite.
  - Architecture overview and how the 4 rules are enforced.
  - What we deliberately chose **not** to build to respect the 4-hour cap.
  - What we would build in **Hour 5** (e.g., two-phase pending/posted holds, cursor pagination, observability metrics, rate limiting).
- [ ] **`TECH_STACK_DECISIONS.md`:**
  - Justification of language, runtime, and storage engine.
  - Tradeoffs vs. alternatives (e.g., SQLite `BEGIN IMMEDIATE` vs. Postgres `SELECT FOR UPDATE` row locks vs. in-memory mutexes).
  - Scaling bottlenecks and when the architecture would need to evolve.
- [ ] **`BUILD_LOG.md` (1 Page, High Signal):**
  - What we set out to do and how we structured the workflow.
  - Where AI genuinely accelerated work (boilerplate, test harness setup, property assertions).
  - **Where AI was confidently wrong** (concrete code examples of naive race conditions, transaction isolation assumptions, or idempotency race windows that AI suggested, how we detected them, and why we rejected/fixed them).
  - Any explicit limitations or sharp edges we are aware of in the shipped code.

---

## 9. Interview Readiness & Live-Coding Preparedness (60-Minute Session)

The final gate of "Done" is ensuring the codebase is structured so you can effortlessly execute the 15-minute live modification during the KOHO interview. Common live-extension prompts in this interview include:
1. *"Add a daily transfer limit per account (e.g., max $1,000 sent per day) that also holds under concurrency."*
2. *"Add support for reversing/refunding a transfer safely and idempotently."*
3. *"Add a fee (e.g., 1% or flat 50 cents) credited to a KOHO fee account on every transfer."*
4. *"Filter transaction history by date range or direction (incoming vs. outgoing)."*

**Architectural Requirement for Live Coding:** Keep the core transaction logic in a single, crystal-clear module (< 250 lines) with zero magic frameworks so you and your AI assistant can add a new invariant or feature and test it in under 10 minutes during the live call.

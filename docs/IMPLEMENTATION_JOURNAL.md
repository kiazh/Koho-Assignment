# Implementation journal

This is the detailed engineering record behind the [build log](BUILD_LOG.md). The [original assignment](Koho-assignment.txt) is the requirement source. The expanded specification, agent instructions, template, and [pre-build plan](_historical/plan.md) are research and planning inputs; they are not a description of every feature shipped. The [decision log](DECISIONS.md) is the per-choice architectural record behind this journal.

## How I use agentic tools

My repeatable process is **research → choose a narrow design → set up the agent → implement in small slices → run the system → try to break it → independently review and verify**. I start with research because there is usually a better-known pattern than the first one I recall. Perplexity Deep Research is a useful way for me to survey options, but retrieved answers can mix database dialects, outdated library versions, and hypothetical requirements. I turn promising claims into tests or check them against the actual runtime before adopting them.

The setup matters more than a broad prompt to “build the project.” I give the coding agent the original brief, a definition of done, a plan with small tasks, and specific capabilities to apply: unit and adversarial testing, end-to-end checks, code-quality review, performance measurement, and an honest issue log. I define what a pass means for the risky paths. That reduces ambiguity without pretending model output is deterministic. Long agent contexts can accumulate stale assumptions, so on larger projects I would delegate bounded tasks to fresh contexts and keep an orchestrator focused on interfaces and review; this repo is small enough that the ledger stayed easy to inspect directly.

I ask an independent, often stronger reviewer to attack the completed work. My broader workflow can compare Argon, Claude, and GPT reviews to diversify what gets noticed, but agreement between models is not proof. A report earns a code change only when I can reproduce the issue with the real toolchain or a test. I then run the application myself. This project followed that pattern: the two external reports were written against an older export and could not run Go, so I checked each actionable claim in the current branch. Focused test output and this journal record the evidence; the application keeps runtime logging modest because this is a local financial demo.

## Design choices from the assignment

The original brief requires four operations and four money invariants, with special emphasis on two simultaneous transfers competing for the same funds. It explicitly accepts an HTTP API, CLI, or library. I chose Go 1.22+, the standard HTTP library, `database/sql`, and pure-Go SQLite so a reviewer can run the project without Docker or a hosted database. Amounts are `int64` cents in JSON, Go, and SQLite; floating-point math never enters a transfer.

The `accounts` table stores both opening and current balance. `transfers` stores the request fields and a unique idempotency key. `ledger_entries` stores one negative sender entry and one positive receiver entry per successful transfer. `STRICT` tables, non-negative/positive `CHECK` constraints, foreign keys, and the unique key provide database backstops. They do not themselves enforce the entire two-entry relationship, so the transaction code and audit tests check it.

`Store.Transfer` validates the request, obtains a dedicated connection, and begins `BEGIN IMMEDIATE` **before** reading the idempotency key or either balance. If the key already exists with the same source, destination, and amount, it returns the original transfer. A changed payload conflicts. Otherwise it checks account existence, funds, and receiver overflow; conditionally debits the source; checks that the receiver credit changed one row; inserts the transfer and two signed entries; and commits. An error before commit rolls the transaction back. SQLite permits one writer at a time, so this design serializes the affordability decision across separate Store handles. File-backed stores use up to eight connections so WAL readers can continue during a write; foreign keys and the busy timeout are configured on every pooled connection.

## Implementation and commit trail

| Commits | Decision and evidence |
| --- | --- |
| `3f9e5b5` → `c627f7c` | Built exact-cent account storage, then balance/history reads. Tested zero and large opening balances, missing accounts, and empty history. |
| `1e220f6` → `693f4bc` | Added atomic transfer, injected a failure after earlier mutations to prove rollback, then added replay/conflict behavior and the synchronized two-Store double-spend proof. |
| `828c502` → `e5a86a4` | Exposed the four HTTP routes and tested status/JSON boundaries. Added 50-way contention, partial drain, bidirectional traffic, and a seeded 500-attempt conservation audit. |
| `b92f86d` → `96e37c3` | Wrote the submission docs; fresh review found the timestamp-order and active-shutdown mistakes, fixed in `0a1da62`, then shortened the recruiter log. |
| `f1be2d5` → `7cb288c` | Benchmarked first, then fixed blocked WAL reads with a connection pool and connection-scoped settings. Expanded cross-Store and real HTTP contention tests. |
| `9f9a568` → `6228d0b` | Reconciled balances from signed entries, verified that malformed pairs fail the audit, forced connection replacement, and probed the raw SQL constraints. |
| `48c64fa` → `bb8e82c` | Reproduced and fixed the silently skipped receiver credit, clarified retry/router contracts, and recorded final verification plus a compiled-server restart smoke test. |
| `cb4e4b4` → `a2cf2d6` | Split the recruiter log from the detailed journal, added the presentation runbook and historical-input labels, moved planning records into `docs/_historical/`, repaired the resulting links, added the per-choice [decision log](DECISIONS.md), and closed the HTTP overflow/boundary test gap in `6fa9972`. |
| `a2cf2d6` → `b848b9a` | The final race gate caught `SQLITE_BUSY` while acquiring the write lock in the 500-way audit. Added a bounded retry around `BEGIN IMMEDIATE` only, a lock-release test, and a longer audit context. |

Each line is a reviewable checkpoint in Git, rather than a claim that an agent produced a complete project in one pass. The pre-build plan remains in the repository to show initial thinking; the actual commits and tests show what changed.

## Issues I had to resolve

| Observation | How I checked it | Resolution |
| --- | --- | --- |
| A newer SQLite driver pulled a Go 1.26 transitive requirement. | `go mod tidy -go=1.22.0` exposed the incompatibility. | Pinned `modernc.org/sqlite v1.34.5` for the Go 1.22 floor. |
| `RFC3339Nano` strings with `.12Z` and `.123Z` were ordered incorrectly as text. | A fractional-time history test failed before the fix. | Parse timestamps and compare instants, then IDs for ties. |
| Shutdown could return while a request was still active. | A blocked-handler test reproduced the early return. | Wait for graceful shutdown to finish. |
| A single pooled connection made a held writer block readers despite WAL. | A read-with-held-write test timed out on the old pool; benchmarks compared one and eight connections. | Allow eight file-backed connections and apply PRAGMAs per connection. |
| A zero-sum ledger check could miss wrong-size or absent entries. | Directly corrupted 40-cent transfers into ±39-cent or missing-entry fixtures. | Require the exact account and signed amount for each transfer; reconcile each account from signed entries. |
| A receiver `UPDATE` could affect zero rows without an SQL error. | A trigger using `RAISE(IGNORE)` made the old test fail: debit and records committed without receiver credit. | Check `RowsAffected() == 1`; otherwise roll back. |
| A 500-way audit intermittently failed with `SQLITE_BUSY` while acquiring the write lock. | The final full race gate failed; focused reruns reproduced 2/3, and raising `busy_timeout` to 15s still failed 1/5. | Kept the 10s timeout; added a bounded, context-aware retry around `BEGIN IMMEDIATE` only, plus a lock-release test; raised the audit context to 60s. |
| The external reports said `go.sum` was missing and connection PRAGMAs were fragile. | Checked tracked files and forced a physical connection replacement through the pinned Go driver. | `go.sum` was already tracked; the replacement connection retained WAL, foreign keys, and busy timeout after the pool fix. |
| The default demo port 8080 was unavailable on this Windows machine. | A real `go run` attempt failed to bind; the identical API demo passed on port 18178. | The presentation guide sets `ADDR` explicitly, and the README explains the fallback. |

I rejected `SELECT ... FOR UPDATE` because it is not SQLite syntax. I also rejected floating-point money, a Docker/PostgreSQL setup for a four-operation exercise, and a generic retry of an entire transfer. After the final gate exposed `SQLITE_BUSY` before a transaction began, I added a bounded retry around `BEGIN IMMEDIATE` only; retrying a transfer after an ambiguous `COMMIT` remains rejected because it can apply the transfer twice. The independent reports' duplicate-JSON-key and router-error observations were lower-priority API boundaries; I documented the router behavior and left duplicate-key hardening outside the assignment's four financial rules.

## How I tried to break it

The tests exercise both the ledger API and HTTP boundary. The central test starts two transfers against the same 100 cents from **separate** Store handles sharing one database file. It requires one success, one insufficient-funds result, a 0/100 balance split, one transfer, and two entries. Other tests send 50 full-balance attempts, 20 partial-drain attempts, 100 bidirectional transfers, 20 same-key calls, conflicting same-key payloads, and 20 competing HTTP requests. The final HTTP boundary test covers receiver overflow plus missing, zero, self-transfer, and overlong-key rejections. The seeded 500-attempt run checks non-negative balances, global conservation, each account's balance reconstructed from actual signed entries, and each transfer's exact debit/credit pair.

Fault injection aborts the second ledger insert and separately ignores the receiver update. Both must leave balances and records unchanged. `TestBeginImmediateRetriesBusyAndSucceedsWhenLockClears` holds the write lock on one connection, forces a 1 ms busy timeout on another, and proves the helper retries, classifies the busy error, then succeeds after the lock is released. Raw SQL probes reject negative/fractional stored cents, orphan transfers, duplicate keys, zero amounts, and self-transfers. Reopening the database preserves the transfer and replay result. A compiled-server HTTP smoke after restart produced the same transfer ID, balances of 60/40 cents, and one history row.

The last full gate ran `gofmt`, `go vet ./...`, `go build ./...`, and five shuffled race-suite runs with CPU counts 1, 4, and 12. The 500-way audit passed ten consecutive race-enabled runs after the begin retry. The shutdown test ran fifty repetitions; the focused double-spend and HTTP race tests each ran twenty. Coverage was 75.8% overall — 90.5% in the API and 79.6% in the ledger; the remaining server percentage is mostly process wiring. Local balance-read benchmarks moved from about 24–25 µs/op with one connection to 5.9–7.0 µs/op with eight; 100 concurrent bidirectional transfers finished in 0.49–0.58 seconds in three local race-enabled runs. These numbers characterize this machine, not a throughput promise. Test names and commands live in the [presentation guide](PRESENTATION_GUIDE.md) and [README](../README.md).

## What remains outside this exercise

There is no authentication, authorization, deployment, or real account-funding policy. `POST /accounts` is not idempotent; a caller must verify whether an account was created after a lost response. History has no pagination, transfer keys have no expiry, and SQLite has one writer. An uncertain transfer response must be retried with the same key and payload; the server does not resolve every ambiguous commit error internally. The fifth hour would go to bounded pagination and an operator reconciliation command. A production version would also need explicit account-opening retry semantics, retention policy, observability, migrations, and a storage choice appropriate to higher write throughput.

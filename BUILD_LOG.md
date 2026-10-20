# KOHO Move the Money — Build Log

## Goal and approach

Build the four requested account and transfer operations in Go and SQLite, then prove the money rules against a real database. I worked in six verified commits. AI accelerated the schema/API scaffolding, the trigger-based rollback test, and the synchronized contention harness; I checked each against the assignment and ran the tests rather than trusting generated code.

## Slice record

| Slice | Result and verification | AI help, correction, or trade-off |
| --- | --- | --- |
| 1 — account storage | `3f9e5b5`: exact `int64` opening balances, schema constraints; vet and full race suite passed. | First test failed on missing ledger types before code was added. Go was absent from PATH; I used a checksum-verified official temporary toolchain. |
| 2 — balance/history | `c627f7c`: missing account and empty-history behavior; full race suite passed. | Test failed before `History` existed. |
| 3 — atomic transfer | `1e220f6`: guarded debit, checked credit, two entries; full race suite passed. | A trigger-induced failure after balance mutation left both balances and tables unchanged. |
| 4 — replay and race proof | `693f4bc`: replay/conflict tests, 20 callers sharing a key, and the decisive two-transfer race across separate store handles; focused race suite passed 10 runs. | The race produced one success, one insufficient-funds result, and one transfer/two entries. |
| 5 — HTTP API | `828c502`: strict JSON and status mapping; vet, full race suite, and server build passed. | The initial latest-driver choice broke the intended Go version floor; see below. |
| 6 — broader audit | `e5a86a4`: 50 full drains, 20 partial drains, 100 opposite-direction transfers, and 500 seeded attempts; full race suite and focused 10-run race suite passed. | The audit checks global conservation, per-account reconstruction, nonnegativity, and zero-sum entry pairs. |
| 7 — submission docs | README, decisions, and this log; manual HTTP smoke returned 201 for first transfer, 200 for replay, 7500/2500 balances, and one history record. | I kept the log tied to observed failures and test output, rather than inventing an AI race bug. |

## AI mistakes and rejected ideas

I initially chose the newest `modernc.org/sqlite` as though it satisfied the plan's Go 1.22 floor. It did not: `go mod tidy -go=1.22.0` reported that `golang.org/x/sys@v0.48.0 requires go@1.26.0`. I pinned `modernc.org/sqlite v1.34.5`, regenerated transitive versions, and reran vet, race tests, and build. That is a real dependency-compatibility mistake, not a hypothetical race story.

I also rejected the planning documents' `SELECT ... FOR UPDATE`/sorted row-lock suggestion for SQLite: it has no such row-lock syntax. `BEGIN IMMEDIATE`, a conditional debit, and database constraints fit this storage engine. I rejected a UI, Docker/PostgreSQL setup, and decimal/floating-point amounts to keep the four-hour exercise focused on the money rules.

## Risks and fifth hour

SQLite permits one writer at a time and each store uses one pooled connection. History has no pagination, and successful idempotency keys are retained indefinitely. I would spend a fifth hour on bounded history pagination and an operator-facing balance/ledger reconciliation command. The API has no auth because the assignment excludes it; it is bound to localhost for demonstration.

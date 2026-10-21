# Build log — Move the Money

I treated this as a correctness exercise: open accounts, transfer exact cents, read balances and history, and prove the money rules under contention. I chose a Go HTTP API backed by one SQLite file so I could focus on transactions and tests.

## The setup was a substantial part of the work

I do not know every established pattern for a money system, so I began with research. I put the brief into Perplexity Deep Research to compare ledger designs, locking approaches, stack trade-offs, and likely failures. I treated its results as leads to verify. Some PostgreSQL advice did not apply to SQLite.

Before coding, I wrote a [definition of done](_historical/koho_definition_of_done_and_execution_spec.md), [agent instructions](_historical/codex_repository_instructions_agents_md.md), and a [step-by-step plan](_historical/plan.md). I gave the agent the brief and gates for adversarial tests, unit tests, code integrity, and an end-to-end run. The central gate was two simultaneous transfers competing for the same 100 cents. I also directed it to check for unnecessary code, performance issues, and unclear explanations. This setup defined what success meant while leaving room for the agent to fill in implementation details.

I had the agent work in small, testable slices: account storage, reads, atomic transfers, concurrency tests, then the API. The commits reflect that sequence. AI wrote much of the Go and saved time on JSON handling, test barriers, audit queries, and fault injection. I reviewed the assertions and ran the service myself because a green unit suite can still miss the real failure.

## Where the tools were wrong

The clearest wrong turn was a suggestion to use `SELECT ... FOR UPDATE` to lock account rows. That is a PostgreSQL pattern; SQLite has no row locks or that syntax. I changed the transfer to acquire SQLite's writer lock with `BEGIN IMMEDIATE` **before** reading balances. `TestCompetingTransfersCannotDoubleSpend` uses two separate store handles against the same file and releases two 100-cent transfers together. Exactly one succeeds; the other gets insufficient funds. The final balances are 0 and 100 cents, with one transfer and two ledger entries.

A fresh review also found that sorting variable-width `RFC3339Nano` strings could put history out of order. A regression fixture reproduced it, and I changed the sort to compare parsed times. More seriously, a receiver credit could affect zero rows without returning an SQL error. A trigger-based test made the old code commit a one-sided debit; I added a row-count check so it rolls back. The final 500-request audit then exposed intermittent `SQLITE_BUSY` at `BEGIN IMMEDIATE`. I rejected a suggested retry around the whole transfer because an error after `COMMIT` may leave the outcome uncertain. I retried only the failed begin, before any money had moved, and reran the race audit.

I used fresh-context review to challenge the implementation and reproduced findings before changing code. I also caught stale review claims against an incomplete export. I rewrote this log several times because AI drafts flattened the debugging trail into a polished summary.

## What I shipped and what I would do next

The full race-enabled suite passed, the competing-transfer test passed repeated runs, and I exercised the HTTP flow and persistence across a server restart. The main unresolved edge is an uncertain `COMMIT`: a client must retry a timeout or 500 with the **same** idempotency key, because a new key could send money again. With a fifth hour, I would add a post-commit lookup by key and test that ambiguous outcome. Account creation is also not idempotent, and history is unpaginated. Those limits are described in the [README](../README.md); the [technical decisions](TECH_STACK_DECISIONS.md) have the longer rationale.

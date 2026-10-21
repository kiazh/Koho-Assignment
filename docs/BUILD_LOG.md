# Build log — Move the Money

I built a Go and SQLite HTTP API to open accounts, move exact cents, and read balances and history. The main goal was proving the money rules under contention.

## The setup was a substantial part of the work

I began with Perplexity Deep Research to compare ledger designs, locking approaches, stack trade-offs, and likely failures. I treated its results as leads to verify; some PostgreSQL advice did not apply to SQLite.

Before coding, I wrote a [definition of done](_historical/koho_definition_of_done_and_execution_spec.md), [agent instructions](_historical/codex_repository_instructions_agents_md.md), and a [step-by-step plan](_historical/plan.md). I gave the agent gates for adversarial and unit tests, code integrity, and an end-to-end run. The central gate was two simultaneous transfers competing for the same 100 cents. I also asked it to check for unnecessary code, performance issues, and unclear explanations. This setup defined success while leaving room for implementation decisions.

I had the agent work through six small slices rather than build the whole project in one shot:

1. **Scaffold and accounts:** Set up the Go module, SQLite schema, and exact integer-cent account values.
2. **Reads:** Add balance lookups and ordered transaction history.
3. **Transfers:** Debit, credit, and write two ledger entries in one transaction; test rollback.
4. **Idempotency and the race proof:** Make retries apply once and test two transfers competing for the same funds.
5. **HTTP and stress tests:** Expose the four operations and audit balances under heavier contention.
6. **Submission:** Write the README, decisions, build log, and demo instructions.

AI wrote much of the Go and saved time on JSON handling, test barriers, and audit queries. After the slices, I debugged with fresh reviews: Repomix packed the repo into context I could paste into Perplexity, where I had effectively unlimited Argon usage to ask for plans to break it. I think the planning model matters more than the model executing each small fix. I passed the plans to the coding agent, then checked every finding with tests or the running service.

## Where the tools were wrong

The clearest wrong turn was a suggestion to use `SELECT ... FOR UPDATE` to lock account rows. SQLite has no row locks or that syntax. I used `BEGIN IMMEDIATE` to acquire its writer lock **before** reading balances. `TestCompetingTransfersCannotDoubleSpend` releases two 100-cent transfers together through separate stores on the same file. Exactly one succeeds; the other gets insufficient funds. The balances end at 0 and 100 cents, with one transfer and two ledger entries.

A fresh review found that sorting variable-width `RFC3339Nano` strings could misorder history; a regression test reproduced it, so I sorted parsed times. A trigger test exposed a more serious gap: the receiver credit could affect zero rows while the debit committed. I added a row-count check to force rollback. The 500-request audit then found intermittent `SQLITE_BUSY` at `BEGIN IMMEDIATE`. I rejected a whole-transfer retry because an error after `COMMIT` may leave the outcome uncertain. I retried only the failed begin, before money moved, and reran the audit.

I also caught stale review claims against an incomplete export. I rewrote this log several times because AI drafts flattened the debugging trail into a polished summary.

## What I shipped and what I would do next

The race-enabled suite and repeated competing-transfer runs passed; I also checked the HTTP flow across a restart. An uncertain `COMMIT` remains: a client must retry a timeout or 500 with the **same** idempotency key. With a fifth hour, I would look up the key after an ambiguous commit and test that path. Account creation is not idempotent, and history is unpaginated. See the [README](../README.md) and [technical decisions](TECH_STACK_DECISIONS.md) for those limits.

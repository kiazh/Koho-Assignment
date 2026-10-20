# Build log — Move the Money

This is the short version for the KOHO discussion. The [implementation journal](IMPLEMENTATION_JOURNAL.md) has the test evidence, issue trail, and commit map.

## Goal and working method

I built the four requested operations as a small Go/SQLite HTTP service. The priority was proving four rules: balances never go negative, transfers are atomic, retries move money once, and cents stay exact. The brief allows an API, CLI, or library; I chose an API because the routes make those behaviors easy to demonstrate. I cut UI, auth, and deployment from this exercise.

My normal workflow starts with research, often using Perplexity Deep Research to compare approaches. I treat that output as leads to test, not as authority. Here I read the assignment and the research/spec notes, chose Go with SQLite, then gave the coding agent a definition of done, a small-step plan, and explicit test and review gates. The useful setup was concrete: which invariants mattered, what to log, what to test at the HTTP and database boundaries, and when to commit. I used the agent to accelerate code and test scaffolding, then challenged its output with tests and fresh reviews. A second model's finding counted only after I reproduced it locally.

## What I built and checked

| Stage | Evidence in Git |
| --- | --- |
| Accounts and reads | `3f9e5b5`, `c627f7c`: integer-cent storage, balance and history tests. |
| Transfer and retry safety | `1e220f6`, `693f4bc`: one SQLite write transaction, two signed entries, rollback injection, two competing transfers, and 20 same-key callers. |
| Usable API and broader proof | `828c502`, `e5a86a4`: four routes, validation, 50-way/partial-drain races, 100 bidirectional transfers, and a seeded 500-attempt audit. |
| Review and follow-up | `0a1da62` through `bb8e82c`: history/shutdown fixes, reader performance, cross-store and HTTP races, stronger ledger reconciliation, constraint probes, and a skipped-credit fix. |

The decisive test uses two Store handles on the same database file. Both try to send the same 100 cents at once with different keys; exactly one succeeds, the other gets insufficient funds, and the persisted balances are 0/100 with one transfer and two entries. A repeated key returns the original transfer ID without a second debit.

## Where the tools were wrong

An early history implementation sorted `RFC3339Nano` timestamp strings. Variable-width fractions make text order differ from time order; a failing fractional-time test exposed it, and the code now sorts parsed instants. I also initially selected a SQLite driver version that required a newer Go release; module verification exposed the mismatch, so I pinned a Go 1.22-compatible version. The research notes proposed `SELECT ... FOR UPDATE`, which SQLite does not support; I used `BEGIN IMMEDIATE` before reading balances instead.

Independent review found a ledger-audit blind spot: zero-sum entries could still have the wrong amount. I added malformed fixtures and exact signed-entry checks. Another warning became a real failing test: a trigger could silently skip receiver credit while the old code committed. Checking the credit's affected-row count now forces rollback. I rejected unverified review claims and generic retry machinery; the client must replay the **same key and payload** after an uncertain response.

## Limits and next hour

SQLite still has one writer; history is unpaginated; successful idempotency keys remain indefinitely; and account opening is not retry-safe. This local demo has no authentication. I would next add bounded history pagination and an operator reconciliation command, then define key-retention and account-opening retry policies before any production use.

Final verification: `go vet`, build, and five shuffled race-suite runs at CPU counts 1, 4, and 12 passed. A compiled-server restart smoke test kept the original transfer ID, 60/40 balances, and one history row. Local benchmarks improved parallel reads from about 24–25 to 5.9–7.0 µs/op; 100 concurrent bidirectional transfers took 0.49–0.58 seconds in three local race-enabled runs. These are measurements on this machine, not universal guarantees.

# Build log — Move the Money

This is the honest account of the build for the KOHO discussion. The code and tests are the primary evidence; the [implementation journal](IMPLEMENTATION_JOURNAL.md) has the full issue trail and commit map, and the [README](../README.md) has the run commands. I am writing this myself rather than letting a tool summarise it, and I will say where that was the hard part.

## What I set out to do

The brief asks for four operations — open an account, transfer money, read a balance, read history — bound by four rules that must never break: balances never go negative, transfers are all-or-nothing, a repeated transfer applies once, and cents stay exact. It also asks, explicitly, for a record of where my tools were wrong. So the goal was not just a working ledger. It was a ledger whose correctness I could *prove* to someone who has not seen the code, plus an honest description of how I actually built it with AI assistance.

I chose Go with the standard library and a pure-Go SQLite driver so that a reviewer can run the whole thing from a clean checkout — no Docker, no Postgres, no credentials. Amounts are `int64` cents everywhere: JSON, Go, and SQL. Floating-point never enters a transfer. Each successful transfer writes one negative and one positive ledger entry, so its entry pair sums to zero, and the current balance is reconcilable from `initial_balance_cents` plus the signed entries.

My real workflow starts with research before I write anything. I usually survey approaches with Perplexity Deep Research and treat the output as leads to test, not as authority — retrieved answers routinely mix database dialects, outdated library versions, and requirements that came from the model's imagination rather than the brief. Here that meant reading the assignment and the planning notes, deciding on Go + SQLite, then giving a coding agent a narrow definition of done with explicit test and review gates instead of a broad "build this" prompt. The setup was the leverage, not the code generation.

## Where AI genuinely helped

AI moved me fastest on the mechanical, high-volume parts, and I will point at them rather than claim I hand-wrote everything. It scaffolded the strict-JSON decode that rejects `10.5`, `1e2`, numeric strings, and out-of-range values by decoding straight into `int64`. It wrote the concurrency test harness — the start-barrier channel that releases many goroutines at once — and the property-style audit assertions that reconstruct every account's balance from signed entries and check global conservation. It produced the first draft of the rollback test that injects a failure between the debit and the credit. On all of these my job was to decide what a *pass* meant on the risky paths and then read the output, not to type the boilerplate.

The single most useful thing AI did was let me run a lot of variations quickly: different SQLite driver versions, different pool sizes, different ways of structuring the transaction. That is real acceleration and I am not going to pretend I did it by hand.

## Where the tools were wrong, and how I caught it

This is the part the brief cares about most, so I am being specific rather than tidy. I had several catches, and one of them was not in the code at all.

**A driver that wanted a Go version I did not have.** An early dependency resolution pulled a newer `modernc.org/sqlite` that transitively required Go 1.26. I caught it with `go mod tidy -go=1.22.0`, which exposed the incompatibility against the Go 1.22 floor. I pinned `modernc.org/sqlite v1.34.5`. The toolchain was not being malicious; it was optimising for the newest version and I had to tell it the constraint.

**History ordering that looked right and was wrong.** My first history implementation sorted the `RFC3339Nano` timestamp strings directly. That is the natural thing to write, and it is incorrect: fractional seconds have variable width, so `2026-01-01T00:00:00.12Z` sorts *after* `2026-01-01T00:00:00.123Z` as text even though it is earlier in time. A test that inserts `.12Z` and `.123Z` failed and forced the fix. The code now parses the timestamps to instants and compares those, with `id` as the tie-break. This one stayed wrong until a test caught it, which is exactly the failure mode worth reporting.

**A receiver credit that could silently not happen.** During review I found that the receiver `UPDATE` did not check how many rows it changed. Under a specific failure — an `RAISE(IGNORE)` on the credit update — the sender could be debited and the ledger records written while the receiver's balance did not move, and the old code would have committed that. I reproduced it with a trigger, and the fix is to check `RowsAffected() == 1` on the credit and roll back otherwise. The debit already checked; the credit did not, and nothing was telling me.

**A ledger check that could pass while the numbers were wrong.** An early reconciliation only checked that each transfer had two entries summing to zero. That is not enough: two entries of the wrong amount, or a missing entry, can still sum to zero or hide in the aggregate. I corrupted test fixtures to ±39-cent and missing-entry pairs and required the exact account, sign, and amount for each side before the audit accepts it.

**The one I am still thinking about — an uncertain commit reported as a clean failure.** When the final `COMMIT` errors, the code returns a generic `500 internal_error`. But a commit error can mean the commit already landed. If a client sees that `500`, assumes the transfer failed, and retries with a *new* idempotency key, the money moves twice. The correct behaviour is to look up the idempotency key after a doubtful commit and return the stored transfer if it is there. I have not fully closed this in code. What I did do was make the contract explicit: an uncertain response must be retried with the *same* key and payload, and that is documented in the README and this log. I am not comfortable with the gap and I say so below.

**And the catch that was not code.** The hardest part of this assignment for me was not the ledger — it was writing this build log and the other docs. Every model I handed the documentation to flattened it. I asked for a specific, in-depth account of where the tools were wrong, and what I rejected and why, and it kept coming back as bullet-point tables and a confident summary that something had been "handled" without the actual detail. I went through several models and several passes. The version you are reading is the one where I stopped accepting the tool's first structure and wrote it myself against the real commits and test output. I mention this because KOHO is testing exactly this skill — directing an AI tool, recognising when its output is hollow, and not shipping the hollow version. The tool was not wrong about a fact here. It was wrong about depth, and the wrongness was smooth enough that I almost missed it.

## What I rejected, and why

- **`SELECT ... FOR UPDATE` with sorted lock ordering.** The planning notes and the research output both suggested this. It is not valid SQLite — SQLite has no row locks, and sorting account IDs cannot create one. The real mechanism is `BEGIN IMMEDIATE`, which takes the writer lock before any balance is read and serialises the affordability decision across separate connections. I used that instead and I did not claim the Postgres pattern was implemented.
- **Floating-point or decimal money.** Rejected outright. `int64` cents is exact and maps directly to a database integer constraint.
- **Docker Postgres.** Rejected for a four-operation exercise. The point was a clean-clone run with no services; SQLite gets me there.
- **A generic retry loop.** An agent suggested wrapping transfers in a bounded-retry wrapper. I rejected it because I had not observed a `SQLITE_BUSY` failure that needed it — the busy timeout already handles contention, and adding retry machinery for an unobserved problem is how you introduce a double-spend.
- **A payload hash for idempotency.** Suggested, then rejected. Comparing the three stored fields (source, destination, amount) directly is simpler and has nothing to get wrong.

## What I shipped that I am not comfortable with

- **The uncertain-commit window above.** A `500` after a commit that may have succeeded is a real double-charge risk if the client changes the key. I documented the same-key replay contract but did not fully implement the post-commit idempotency lookup. That is the sharpest edge in the code.
- **Account opening is not idempotent.** `POST /accounts` always inserts. A timeout followed by a retry mints a second funded account. The brief does not require idempotent open, but it is the other retry trap beside transfers, and a demo client that "retries until it works" will create phantom money. It is documented in the README; I would not want it to be a surprise.
- **What my hardest test actually proves.** The decisive test, `TestCompetingTransfersCannotDoubleSpend`, opens two separate `Store` handles on one database file and proves the SQLite file lock holds. That is the real proof. The larger storm tests (50-way drain, partial drain, bidirectional traffic, the 500-transfer audit) mostly share a single store and therefore a single connection — they prove in-process serialisation plus the conditional debit, not cross-connection locking. I would not want a reviewer to read the storm tests as a stronger concurrency proof than they are.
- **Unbounded history and key retention.** History has no pagination and successful idempotency keys are kept for the life of the database. Fine for a local demo; a production service needs policies for both.
- **No authentication, by design.** Anyone who can reach the API can open a funded account. That is the assignment, not a defect — but it must stay on localhost.

## How I verified it

`go vet ./...`, a build, and the full race suite passed across repeated shuffled runs at CPU counts 1, 4, and 12. The focused double-spend proof runs ten repetitions clean. I also ran a compiled-server restart smoke test: after stopping and reopening the process, the original transfer ID, the `60/40` balances, and the single history row all survived. Local benchmarks moved balance reads from about 24–25 µs/op with one connection to roughly 6–7 µs/op with the eight-connection pool; 100 concurrent bidirectional transfers finished in about 0.5 seconds. Those numbers describe this machine, not a throughput promise. The commands are in the README and the [presentation guide](PRESENTATION_GUIDE.md).

# KOHO Move the Money — Full Debugging and Verification Report

> **Historical external review.** This report examined an older repository export and could not execute the Go application. Its findings are leads, not the current verification status; see the [implementation journal](IMPLEMENTATION_JOURNAL.md) for reproduced issues and fixes.

- Audit date: October 20, 2026
- Project: KOHO Move the Money assignment
- Reviewed input: `repomix-output-kiazh-Koho-Assignment.xml`
- Report type: Source review, isolated SQLite verification, and E2E test preparation
- Application execution status: **Blocked — Go toolchain unavailable**

## 1. Executive Summary

The source review found a coherent implementation of the assignment's core money-transfer rules, along with two medium-priority gaps:

1. Required SQLite connection settings are applied during startup, but are not guaranteed to survive replacement of the physical database connection.
2. The randomized ledger audit does not independently verify the complete relationship between transfers, signed ledger entries, and stored balances.

Two additional low-priority contract concerns involve router-generated error responses and the exact meaning of “strict JSON.”

No critical money-loss bug was established. That does not mean the application has been certified correct: the native Go tests, race detector, vet, build, and real-server end-to-end scenarios could not execute in the audit environment.

Fourteen isolated SQLite observations were executed against schema statements extracted from the uploaded source. Those observations support several database protections and reproduce the audit-query blind spots. They do not establish the behavior of the actual Go driver, HTTP server, cancellation paths, or process-level concurrency.

This Markdown file presents the full findings from the preceding audit. It is not a fresh execution of the application tests.

## 2. Scope and Evidence

### 2.1 Reviewed material

The audit reviewed the uploaded Repomix snapshot, including:

- Five production Go files, totaling 496 lines.
- Four Go test files containing 17 top-level test functions.
- The original assignment requirements.
- The README and technical-stack decisions.
- The implementation plan and expanded execution specification.
- The build log and supporting planning documents.

The snapshot contained 19 extracted files. Production code and existing tests were inspected.

The review concerned the uploaded snapshot, not a checked-out Git branch. No production source was modified, and nothing was pushed to GitHub.

### 2.2 Evidence limitations

- The snapshot does not contain `.git` history, so historical commit claims were not independently verified.
- Test results described in `BUILD_LOG.md` were treated as historical claims, not current execution results.
- `go.sum` is absent from the export. Repomix can omit files, so that absence is not sufficient evidence that the actual repository lacks it.
- Ignored files, binaries, and runtime artifacts may be absent from the snapshot.
- Isolated SQL checks used Python's SQLite 3.46.1, not the application's pinned `modernc.org/sqlite v1.34.5` runtime.

## 3. Verification Status

| Verification | Status | Evidence or limitation |
| --- | --- | --- |
| Production-source and requirements review | Completed | Extracted code, tests, and supporting documents inspected |
| Isolated SQLite/schema observations | Executed | Fourteen observations recorded during the audit |
| Strengthened transfer-audit query | Executed | Tested against missing-entry, wrong-magnitude, and valid-pair fixtures |
| Existing Go test suite | Blocked | Go executable unavailable |
| Go race detector | Blocked | Go executable unavailable |
| `go vet` | Blocked | Go executable unavailable |
| Native server build | Blocked | Go executable unavailable |
| Actual HTTP end-to-end scenarios | Blocked | Runner stopped during setup; zero application tests ran |
| New Python E2E runner syntax | Checked | Python compilation succeeded; 19 scenario methods defined |
| New Go regression test | Not compiled or executed | Requires the original repository and a Go toolchain |
| Driver-specific connection replacement | Unverified | Requires execution through the actual Go driver |
| Request cancellation and crash recovery | Unverified | No native runtime execution completed |

A direct attempt to execute `go test` failed because the executable was missing. The prepared E2E runner also stopped during setup with:

```text
BLOCKED: Go is not installed; no application tests ran
Ran 0 tests
```

These are blocked checks, not passing checks.

## 4. Findings Summary

| ID | Priority | Finding | Evidence class |
| --- | --- | --- | --- |
| F01 | Medium | Connection-scoped SQLite settings are not reapplied to replacement connections | Source review plus isolated SQLite reproduction |
| F02 | Medium | Randomized audit misses incomplete or incorrectly valued ledger records | Source review plus malformed SQL fixtures |
| F03 | Low | Router errors are not normalized to the documented JSON error shape | Source-review finding; HTTP behavior not executed |
| F04 | Low / optional hardening | Strict JSON decoding does not reject duplicate members or require exact field-name case | Source review and documented decoder behavior; HTTP reproduction not executed |

Severity reflects the assignment's scope. Accepted limitations such as missing authentication and unpaginated history are discussed separately rather than inflated into critical findings.

## 5. F01 — SQLite Connection Configuration Does Not Survive Guaranteed Replacement

### Location

`internal/ledger/store.go:20–59`, especially the pool limit and PRAGMA initialization around lines 25–29.

### Current behavior

`Open()` limits the pool to one open connection, then executes initialization statements including:

```sql
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 10000;
```

The required settings are applied through `sql.DB` during startup. There is no visible mechanism that reapplies them every time a new physical connection is created.

`SetMaxOpenConns(1)` caps concurrent connections. It is not a guarantee that one particular physical connection will remain alive forever.

### Reproduction and observation

An isolated SQLite probe configured one connection, closed it, and opened a fresh connection without reapplying the connection settings.

| Setting | Configured connection | Fresh unconfigured connection |
| --- | ---: | ---: |
| `foreign_keys` | 1 | 0 |
| `busy_timeout` | 10000 | 0 |

The fresh connection accepted an orphan transfer inserted directly through SQL when foreign-key enforcement was disabled.

The Python replacement connection explicitly used `timeout=0` so Python would not install its own default busy timeout and obscure the observation.

### Impact and boundaries

A replacement Go connection may lose the intended foreign-key backstop and lock-wait behavior. This can expose unexpected constraint behavior or immediate lock errors under contention.

This is not a demonstrated public-API path that creates orphan transfers. The application still checks account existence, uses write transactions, and has non-negative-balance constraints.

The isolated probe demonstrates SQLite's connection-scoped configuration behavior. Actual replacement through `modernc.org/sqlite` remains to be tested.

### Recommended fix

Configure required PRAGMAs on every physical connection using settings or an initialization hook supported by the pinned driver. Retain a one-connection pool if that is the intended throughput tradeoff.

Merely setting `MaxIdleConns(1)` is not a complete fix, because a connection can still be replaced.

### Regression test

The prepared `audit_regression_test.go`:

1. Opens a test store.
2. Calls `SetMaxIdleConns(0)` to close the idle physical connection.
3. Obtains a replacement connection.
4. Asserts `foreign_keys = 1` and `busy_timeout = 10000`.

The test was supplied but not compiled or executed in the audit environment.

## 6. F02 — The Randomized Audit Does Not Prove the Complete Ledger Invariant

### Location

`internal/ledger/concurrency_test.go`, specifically `TestSeededConcurrentAuditPreservesEveryCent` and its final grouped query.

### Current audit behavior

The audit groups existing rows in `ledger_entries` and checks that each group has two entries with a zero sum.

This leaves two blind spots:

1. A transfer with no entries has no group and is invisible to the query.
2. Two entries can sum to zero while having the wrong magnitude or account assignment.

The per-account reconstruction reads `History()`, which obtains transfer amounts from the `transfers` table. That is not an independent reconciliation of the signed ledger-entry amounts.

### Reproduced malformed fixtures

| Fixture | Existing grouped audit result |
| --- | --- |
| Transfer row exists with no ledger entries | Zero bad groups |
| Transfer amount is 40; entries are -39 and +39 | Zero bad groups |

For the second fixture, balances of 60 and 40 remain consistent with a transfer of 40, so transfer/history-based reconstruction can pass even though the signed ledger is wrong.

These were deliberately malformed direct-SQL fixtures. They do not show that the production transfer function creates such states, or that every other existing test would miss a simple omission.

### Recommended fix

For every transfer, require:

- Exactly two ledger entries.
- Exactly one sender entry equal to the negative transfer amount.
- Exactly one recipient entry equal to the positive transfer amount.
- A zero sum.
- Correct account and transfer references.

Separately reconstruct every account balance from opening funds plus actual signed ledger entries.

A strengthened transfer-level query is:

```sql
SELECT t.id
FROM transfers AS t
LEFT JOIN ledger_entries AS e ON e.transfer_id = t.id
GROUP BY t.id
HAVING COUNT(e.id) <> 2
   OR COALESCE(SUM(e.amount_cents), 0) <> 0
   OR SUM(CASE
       WHEN e.account_id = t.from_account_id
        AND e.amount_cents = -t.amount_cents
       THEN 1 ELSE 0 END) <> 1
   OR SUM(CASE
       WHEN e.account_id = t.to_account_id
        AND e.amount_cents = t.amount_cents
       THEN 1 ELSE 0 END) <> 1;
```

This query was executed against missing-entry, wrong-magnitude, and correct-pair fixtures. It detected the first two and accepted the third.

The prepared E2E runner also performs independent signed-entry reconciliation using Python integers, avoiding fixed-width aggregate overflow when summing long transaction histories.

## 7. F03 — Router Errors Do Not Share the Application JSON Error Contract

### Location

`internal/api/handler.go:23–30` and the README's API error description.

### Finding

Application handlers write JSON errors, but unmatched routes and unsupported methods are delegated to the default `ServeMux`. There is no visible normalization layer ensuring that these responses have the same JSON shape.

A client expecting every unsuccessful response to contain a JSON `error` member can therefore encounter a different format.

### Status and impact

This is a source-review finding. Actual router responses were not executed during the audit. It does not affect the core money invariants.

### Recommendation

Either:

- Document that the JSON error contract applies to application-handler errors only; or
- Add a small fallback/error boundary that produces consistent responses.

Add boundary tests for unknown routes and unsupported methods. Preserve correct method semantics and `Allow` headers if customizing method handling.

## 8. F04 — Strict JSON Still Allows Ambiguous Member Representations

### Location

`internal/api/handler.go:33–47`.

### Finding

The decoder uses `DisallowUnknownFields()`, but that does not reject duplicate object members or require case-sensitive matching of struct fields.

For ordinary scalar duplicate members, later values can replace earlier values. For example:

```json
{"initial_balance_cents":-1,"initial_balance_cents":100}
```

The request can be interpreted as an opening balance of 100 rather than rejected as ambiguous.

Cases worth explicitly testing include:

- Duplicate `initial_balance_cents` members.
- Duplicate `amount_cents` members.
- Case aliases such as `INITIAL_BALANCE_CENTS`.
- Duplicate members involving `null`.

### Status and boundaries

This is based on source inspection and documented Go decoder behavior, not an executed HTTP reproduction.

Rejecting duplicates and enforcing exact case are not explicit requirements of the original assignment. The final decoded amount still undergoes domain validation. This is a hardening and contract-clarity choice, not an established violation of the four money rules.

### Recommendation

Define the policy explicitly. If duplicate rejection is desired, validate object members before normal struct decoding. A map-only preparse is insufficient because a map loses duplicate occurrences.

Avoid changing JSON packages solely for this issue without considering the project's Go 1.22 target and the assignment timebox.

## 9. Executed SQLite Observations

### 9.1 Safety and locking checks that behaved as expected

1. Negative stored balances were rejected by `CHECK(balance_cents >= 0)`.
2. Orphan transfers were rejected with foreign-key enforcement enabled.
3. Duplicate idempotency keys were rejected by the unique constraint.
4. Adding one cent to a `MaxInt64` stored balance was rejected because the resulting REAL value could not be stored in a STRICT INTEGER column.
5. An injected failure on the positive ledger entry followed by rollback restored balances to 100/0 and left zero transfers and entries.
6. Fractional stored cents were rejected by STRICT INTEGER typing.
7. Zero transfer amounts were rejected by `CHECK(amount_cents > 0)`.
8. Same-account transfers were rejected by the distinct-account constraint.
9. A second independent `BEGIN IMMEDIATE` was blocked by the first writer and returned `database is locked` after approximately 101 ms with a deliberately configured 100 ms busy timeout.

### 9.2 Observations exposing limits or clarifying claims

10. A fresh connection did not inherit the configured foreign-key enforcement and busy timeout.
11. The grouped audit did not detect a transfer with zero entries.
12. The grouped audit did not detect incorrect entry magnitudes that still summed to zero.
13. Direct SQL `UPDATE` and `DELETE` of ledger entries succeeded. “Immutable” describes application behavior, not an enforced database property.
14. STRICT tables losslessly converted numeric text `'42'` and integral REAL `42.0` into stored INTEGER values. STRICT enforces the stored type, not rejection of every input representation. HTTP `int64` decoding is a separate boundary.

These observations are not native Go test results.

## 10. What Appears Sound in the Implementation

The following source-level design choices support the assignment's requirements:

- Money is represented as signed `int64` cents, with no visible floating-point conversion in the application money path.
- Transfer amounts must be positive, source and destination must differ, and blank or oversized keys are rejected.
- `BEGIN IMMEDIATE` occurs before balance reads, serializing competing writers before the affordability decision.
- The sender debit is conditional and its affected-row count is checked.
- Recipient overflow is checked before applying the credit.
- Balance updates, transfer metadata, the idempotency key, and both signed entries share one connection and transaction.
- The idempotency lookup occurs before current-balance checks, allowing identical replay after the sender's balance is drained.
- Changed valid payloads using an existing key return a mismatch error.
- Failed attempts do not reserve an idempotency key.
- The principal two-transfer race test uses separate store handles on the same database file.
- The failure-injection test targets the second ledger entry, after earlier mutations, making it a useful rollback test.
- History ordering compares parsed instants rather than variable-width timestamp text.
- The normal shutdown path waits for active requests, with a dedicated test for that path.

These are source-review conclusions, not substitutes for executing the Go suite.

## 11. Accepted Limits and Additional Hardening

### 11.1 Authentication and funded account opening

No authentication and arbitrary funded account opening are intentional assignment choices. The README documents localhost binding and warns against production exposure. These should not be labeled unexpected security failures within the requested scope.

### 11.2 Unpaginated history and connection occupancy

`History()` reads all matching records, parses timestamps, and sorts in memory. Its deferred `rows.Close()` runs when the function returns.

With one connection per Store, other operations can wait behind history processing. Add bounded pagination before large-scale use.

Do not reintroduce a naive lexical sort on variable-width `RFC3339Nano` strings when moving ordering into SQL. Use a correctly sortable representation and a stable tie-breaker.

### 11.3 Single-writer and one-connection throughput

SQLite serializes writes. The one-connection pool also serializes operations within a Store. WAL does not make one connection execute a long read and write concurrently.

This is an acceptable simplicity/throughput tradeoff for the assignment, provided it is explained accurately.

### 11.4 Ledger immutability

The schema does not prohibit direct updates or deletion of ledger entries. Exact pair construction is also an application-flow invariant rather than a fully enforced database invariant.

Clarify documentation. Add triggers only if protecting against direct database writers is part of the intended threat model.

### 11.5 Rollback cleanup

The raw deferred rollback ignores errors and uses `context.Background()`.

A rollback after a successful commit naturally differs from an unsuccessful rollback before commit. Consider explicit transaction state, bounded cleanup, and discarding a connection if its state cannot be confirmed.

No rollback-failure or poisoned-pool bug was reproduced.

### 11.6 Forced shutdown

The graceful-shutdown period is bounded at ten seconds. If it expires, `serve()` does not explicitly force-close active handlers; the error path in `main()` calls `os.Exit`, skipping deferred cleanup.

Define and test the intended forced-shutdown behavior. The normal drain path should not be described as an unlimited graceful guarantee.

### 11.7 Schema evolution

Initialization uses `CREATE TABLE IF NOT EXISTS`, not versioned migrations. The plan mentions versioned schema, but a migration mechanism is not implemented in the current service.

This affects future upgrades rather than a fresh one-version demonstration.

## 12. Remaining Coverage Gaps

The following remain important to execute or add:

1. Replacement-connection PRAGMA behavior through the pinned Go driver.
2. Compiled server startup, real HTTP requests, signal shutdown, and restart durability.
3. Larger contention and same-key storms through independent stores or processes.
4. Cancellation while waiting for a pooled connection, waiting on a database lock, and executing a transaction.
5. Pool usability and rollback correctness after cancellation.
6. A conflicting payload racing with the first successful use of a key.
7. Race-detector and vet results with explicit timeouts and repeated stress runs.
8. Router errors, duplicate members, case aliases, exact key-byte boundaries, and oversized bodies.
9. Controlled crash/restart and ambiguous commit outcomes.

Some existing tests discard setup, read, or COUNT-query errors. Downstream assertions generally still expose unexpected failures, but checking the original errors improves diagnostics.

Several concurrency result receives are unbounded. Explicit deadlines or suite timeouts would prevent indefinite hangs and make failures easier to diagnose.

## 13. Prepared End-to-End Test Suite

The supplied Python standard-library runner builds and launches the actual Go executable. It uses temporary databases and localhost listeners rather than a deployed service.

It defines 19 scenarios:

| # | Scenario |
| ---: | --- |
| 1 | Account creation, transfer, balances, and both account histories |
| 2 | Empty history and unknown-account reads |
| 3 | MaxInt64 transfers and recipient overflow |
| 4 | Invalid opening JSON and missing, null, fractional, exponent, string, or out-of-range amounts |
| 5 | Invalid transfer fields, missing required fields, self-transfers, and key-byte limits |
| 6 | Unknown fields, trailing JSON, and exponent transfer amounts |
| 7 | Oversized request-body rejection |
| 8 | Identical replay and source/destination/amount conflicts |
| 9 | Replay after the source balance is drained |
| 10 | Same-key retry after an initial rejection and subsequent funding |
| 11 | Unknown sender or receiver without mutation |
| 12 | Restart persistence of balances, history, and idempotency |
| 13 | Twenty concurrent same-key HTTP requests to one process |
| 14 | Two processes competing for the same funds |
| 15 | Twenty same-key requests across two processes |
| 16 | Twenty partial-drain requests across two processes |
| 17 | One hundred bidirectional requests across two processes |
| 18 | Injected second-entry failure, rollback, trigger removal, and same-key retry |
| 19 | Seeded mixed transfers with independent signed-ledger reconciliation |

The runner was syntax-checked. An actual execution attempt stopped during setup because Go was missing; none of the scenarios is claimed to pass.

The runner does not replace dedicated native tests for cancellation, replacement connections, forced shutdown, or abrupt crash recovery.

## 14. Deliverable Inventory

The preceding audit bundle includes:

- `e2e_audit.py` — prepared real-server E2E runner.
- `audit_regression_test.go` — replacement-connection regression test.
- `strong_transfer_audit.sql` — strengthened transfer-pair audit.
- `observations.json` — recorded isolated SQLite observations and input fingerprint.
- `e2e_blocked.log` — actual setup failure showing zero application tests executed.
- A detailed report in PDF and Markdown formats.

This standalone Markdown report does not embed or replace the separate test scripts.

## 15. Verification Commands

Run these in the original repository with Go 1.22+ installed:

```sh
go mod download
go vet ./...
go test -race -count=1 -timeout=120s ./...
go test -race -count=20 -timeout=120s \
  -run TestCompetingTransfersCannotDoubleSpend ./internal/ledger
```

Copy the supplied regression test into `internal/ledger/`, apply the per-connection initialization fix, and run:

```sh
go test -race -count=1 \
  -run TestAuditConnectionReplacementKeepsRequiredPragmas ./internal/ledger
```

Run the supplied E2E script with Python 3:

```sh
python e2e_audit.py --repo /path/to/original/repository
```

Use the appropriate local Python launcher and repository path. Graceful signal behavior is best verified on Linux or macOS because Windows process termination does not follow the same SIGTERM path. Race-enabled Go builds also need a supported platform and corresponding compiler setup.

## 16. Recommended Work Order

1. Fix connection initialization and prove required PRAGMAs survive replacement.
2. Strengthen the transfer-pair audit and independent signed-entry reconciliation.
3. Run the existing native Go verification gates.
4. Run the prepared real-service E2E suite.
5. Add targeted cancellation, timeout, forced-shutdown, and crash-recovery tests.
6. Resolve or explicitly accept the JSON and router-error contract choices.
7. Keep accepted scope cuts documented instead of adding unnecessary infrastructure.

## 17. Acceptance Criteria

Before treating the application as end-to-end verified:

- Native Go tests and `go vet` succeed.
- Race-enabled tests and repeated cross-handle contention pass.
- Required connection settings survive forced connection replacement.
- The strengthened audit rejects malformed fixtures and accepts valid state.
- All 19 real-service E2E scenarios execute and pass.
- Cancellation, cleanup, shutdown, and uncertain commit behavior are tested or explicitly recorded as remaining limits.
- Historical build-log claims remain distinct from current verification evidence.

## 18. Final Assessment

The transfer algorithm looks coherent for the assignment: exact integer arithmetic, early write-lock acquisition, checked debit and credit, durable idempotency, and atomic ledger writes are all visible in the source.

The main concerns are preserving connection-level safeguards and strengthening the evidence used to claim ledger correctness. The audit did not establish a critical money-loss failure, and it did not certify the application as fully tested.

The decisive next step is to execute the native suite and real-server scenarios in a Go-enabled environment after addressing the two medium-priority gaps.

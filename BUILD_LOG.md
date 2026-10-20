# KOHO Move the Money — Build Log

## Goal and approach

Build the four requested account and transfer operations in Go and SQLite, then prove the money rules with tests that run against a real database. I am working in verified slices, each with its own commit and test result. I chose a local database and standard-library HTTP API to keep setup and the live interview change small.

## Slice record

| Slice | Result and verification | AI help, correction, or trade-off |
| --- | --- | --- |
| 1 — account storage | `go fmt ./...`, `go vet ./...`, and `go test -race -count=1 ./...` passed. Account tests cover zero, 10,000, and `MaxInt64` cents, negative input, and missing IDs. | Go was absent from PATH; a checksum-verified official archive supplied the temporary toolchain. The first test failed on undefined ledger types before implementation, then passed. |

## AI mistakes and rejected ideas

Keep this section factual as implementation continues. The planning documents proposed SQLite `SELECT ... FOR UPDATE` and lexicographic row locks. SQLite does not have that row-lock API, so the transfer implementation will use an immediate write transaction and conditional debit. A database test must verify this choice under contention.

## Risks and fifth hour

SQLite permits one writer at a time; this is suitable for a small local exercise but limits write throughput. If time remains, measure contention and document when a move to PostgreSQL would make sense. Do not claim any performance number without a measured run.

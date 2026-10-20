# KOHO Move the Money — Build Log

## Goal and approach

Build the four requested account and transfer operations in Go and SQLite, then prove the money rules with tests that run against a real database. I am working in verified slices, each with its own commit and test result. I chose a local database and standard-library HTTP API to keep setup and the live interview change small.

## Slice record

| Slice | Result and verification | AI help, correction, or trade-off |
| --- | --- | --- |
| 1 — account storage | `go fmt ./...`, `go vet ./...`, and `go test -race -count=1 ./...` passed. Account tests cover zero, 10,000, and `MaxInt64` cents, negative input, and missing IDs. | Go was absent from PATH; a checksum-verified official archive supplied the temporary toolchain. The first test failed on undefined ledger types before implementation, then passed. |
| 2 — balance and history reads | `TestHistoryEmptyAndMissingAccount` failed before `History` existed, then the full race suite passed. | The history query joins ledger entries so an account will only see transfers that affected it. Its ordering needs a populated-history test in the transfer slice. |
| 3 — atomic transfer | Transfer tests first failed because `Transfer` did not exist. The full `go test -race -count=1 ./...` suite then passed, including a trigger-induced failure after balance mutation that left both balances and tables unchanged. | The transfer uses `BEGIN IMMEDIATE` and a conditional debit. A recipient-overflow check prevents `int64` wraparound; the transaction rolls back on every return before commit. |
| 4 — replay and contention proof | The key-replay, two-transfer race, and 20-caller retry tests passed 10 consecutive race-enabled runs; the full race suite also passed. The two-transfer test opens separate SQLite store handles on the same file, starts both goroutines together, and proves one success, one insufficient-funds result, and exactly two ledger entries. | Replay comparison was implemented with the transfer transaction in slice 3, so this slice adds its missing independent proof rather than claiming new replay code. A distinct key with changed payload is rejected while the original key still replays after funds are spent. |
| 5 — HTTP API | Route tests failed before `New` existed, then `go vet ./...`, `go test -race -count=1 ./...`, and `go build ./cmd/server` passed. | The latest SQLite driver pulled a Go 1.26 minimum, which conflicted with the planned Go 1.22 floor. I pinned `modernc.org/sqlite v1.34.5` and retidied the module for Go 1.22; the full suite passed with that version. |

## AI mistakes and rejected ideas

Keep this section factual as implementation continues. The planning documents proposed SQLite `SELECT ... FOR UPDATE` and lexicographic row locks. SQLite does not have that row-lock API, so the transfer implementation will use an immediate write transaction and conditional debit. A database test must verify this choice under contention.

## Risks and fifth hour

SQLite permits one writer at a time; this is suitable for a small local exercise but limits write throughput. If time remains, measure contention and document when a move to PostgreSQL would make sense. Do not claim any performance number without a measured run.

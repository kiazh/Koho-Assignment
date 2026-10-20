# Interview presentation guide

Use this as a run sheet, not a script to recite. The [assignment](Koho-assignment.txt) schedules 10 minutes for a demo, 10 for the build log and commits, 15 for design questions, 15 for a live change, and 10 for your questions. Keep the [README](../README.md) open for commands, the [build log](BUILD_LOG.md) for your decisions, and [the key race test](../internal/ledger/concurrency_test.go) for proof.

## The one-minute explanation

> “I built the four requested operations as a Go HTTP API backed by SQLite. My main decision was to make each transfer one write transaction: check the idempotency key and balances only after acquiring SQLite's writer lock, then debit, credit, and record two signed entries together. I used integer cents and tests that compete for the same funds, inject failures, and reconcile the ledger. I used AI to move faster, but I treated plans and reviews as hypotheses until the code and tests confirmed them.”

The assignment explicitly accepts an API, CLI, or library. Be ready to say why the API was useful: a reviewer can see `201` for a new transfer, `200` for an identical replay, and the error codes for conflicts and insufficient funds.

## Before the call

In PowerShell, from the repository root, find Go and run the focused checks. This workspace has also used a temporary Go toolchain when `go` was not on `PATH`.

```powershell
$installedGo = Get-Command go -ErrorAction SilentlyContinue
$go = if ($installedGo) { $installedGo.Source } else { Join-Path $env:TEMP 'koho-go-toolchain/go/bin/go.exe' }
& $go version
& $go mod download
& $go vet ./...
& $go test -race -count=1 ./...
& $go test -race -count=10 -run "^TestCompetingTransfersCannotDoubleSpend$" ./internal/ledger
```

If neither Go path exists, install Go 1.22 or newer and reopen PowerShell. The race test should say `PASS`; it does not print the balances unless it fails, so explain the assertions while showing the test source. Check `git status --short` before sharing your screen. Keep local review reports and Repomix exports out of the submission unless you intentionally choose to include them.

The service defaults to port 8080, but that port was unavailable in one local Windows smoke test. The commands below explicitly use port 18178, which passed the live demo check; choose another free port via `ADDR` if needed.

## Ten-minute live demo

**0:00–1:30 — Problem and design.** Name the four operations and four invariants. Open [transfer.go](../internal/ledger/transfer.go) and point to `BEGIN IMMEDIATE`, the conditional debit, checked credit, two entries, and `COMMIT`. Explain that a failed step rolls back the whole transaction.

**1:30–5:00 — Use the running API.** Start the server in the first terminal. `interview-demo.db` is ignored by Git; rerunning the demo creates new accounts each time. Keep this terminal running.

```powershell
$env:LEDGER_DB = Join-Path (Get-Location) 'interview-demo.db'
$env:ADDR = '127.0.0.1:18178'
& $go run ./cmd/server
```

In a second terminal, run the example. The generated key avoids collisions with earlier demos.

```powershell
$base = 'http://127.0.0.1:18178'
$a = Invoke-RestMethod -Method Post -Uri "$base/accounts" -ContentType application/json -Body '{"initial_balance_cents":10000}'
$b = Invoke-RestMethod -Method Post -Uri "$base/accounts" -ContentType application/json -Body '{"initial_balance_cents":0}'
$body = @{from_account_id=$a.id; to_account_id=$b.id; amount_cents=2500; idempotency_key=[guid]::NewGuid().ToString('N')} | ConvertTo-Json -Compress
$first = Invoke-RestMethod -Method Post -Uri "$base/transfers" -ContentType application/json -Body $body
$replay = Invoke-RestMethod -Method Post -Uri "$base/transfers" -ContentType application/json -Body $body
$aBalance = Invoke-RestMethod "$base/accounts/$($a.id)/balance"
$bBalance = Invoke-RestMethod "$base/accounts/$($b.id)/balance"
$history = Invoke-RestMethod "$base/accounts/$($b.id)/transactions"
[pscustomobject]@{SameTransfer=($first.id -eq $replay.id); SourceCents=$aBalance.balance_cents; DestinationCents=$bBalance.balance_cents; HistoryRows=$history.transactions.Count}
```

Expected display: `SameTransfer=True`, `SourceCents=7500`, `DestinationCents=2500`, `HistoryRows=1`. Reusing the same request returns the original transfer; changing the key means a new transfer. Stop the server with Ctrl+C after the demo.

**5:00–8:00 — Show the decisive proof.** Open `TestCompetingTransfersCannotDoubleSpend` in [concurrency_test.go](../internal/ledger/concurrency_test.go). It uses two separate Store handles on the same file and a start gate. Run:

```powershell
& $go test -race -count=10 -run "^TestCompetingTransfersCannotDoubleSpend$" -v ./internal/ledger
```

Explain the expected result before showing `PASS`: exactly one success, one insufficient-funds error, source balance zero, recipient balance 100, one transfer, and two ledger entries. Then mention the separate rollback, same-key replay, and 500-attempt signed-ledger audit tests.

If they ask for a deeper failure check, run one of these focused tests instead of the entire suite during the demo:

```powershell
& $go test -race -run "^TestTransferRollsBackWhenReceiverUpdateIsIgnored$" -v ./internal/ledger
& $go test -race -run "^TestSeededConcurrentAuditPreservesEveryCent$" -v ./internal/ledger
```

**8:00–10:00 — One judgment call and one limitation.** The initial plan suggested `SELECT ... FOR UPDATE`; SQLite has no such row locks, so the implementation uses `BEGIN IMMEDIATE`. The first history sort also looked plausible but was wrong for variable-width fractional timestamps; a failing test caught it. SQLite remains a single-writer choice, appropriate for this local exercise rather than a production banking deployment.

## Ten-minute build-log and commit walkthrough

Open the [one-page build log](BUILD_LOG.md). Use three commits to show the working pattern instead of scrolling through every commit:

```powershell
git show --stat 693f4bc  # the concurrency proof
git show --stat 0a1da62  # a review failure reproduced and fixed
git show --stat 48c64fa  # skipped receiver credit reproduced and fixed
git log --oneline --reverse
```

Tell the sequence plainly: research and scope, small implementation slices, tests, hands-on API use, adversarial review, reproduction, fix, repeat. The [implementation journal](IMPLEMENTATION_JOURNAL.md) has the longer issue table and the complete commit map if they ask. The external reports were useful but were written against an older snapshot and did not run Go; you tested their claims in the current repository before changing production code. Do not present every research-note requirement as shipped functionality.

## Design questions you may get

| Question | Answer to be ready with |
| --- | --- |
| Why SQLite? | One local database, no infrastructure, and `BEGIN IMMEDIATE` serializes the money decision. WAL plus an eight-connection pool helps reads; writes remain single-writer. |
| Why integer cents? | Exact arithmetic and direct integer constraints; receiver overflow is checked before credit. |
| How do retries work? | A successful key is unique and permanent. Identical payload returns the same transfer ID; different payload conflicts; failed attempts do not reserve the key. After a lost response, reuse the same key and payload. |
| How is atomicity proved? | Both balances, transfer record, key, and two entries share a transaction. Fault-injection tests abort the second entry and silently skip receiver credit; neither may leave a partial transfer. |
| How do you know the ledger agrees with balances? | The seeded test checks opening balance plus signed entries against each stored balance and verifies each entry's account, sign, and exact amount. |
| What would change for production? | Authentication and real funding policy first; then account-opening retry semantics, pagination, key retention, reconciliation tooling, migrations, monitoring, and a storage decision for higher write throughput. |

## During the live change

Repeat the same method on a smaller scale: restate the requested behavior, locate the owner in code, write a focused failing test, make the smallest change, run that test, then run the relevant race and vet checks. Ask one precise question if a requirement is ambiguous. Do not treat an AI suggestion or a green unit test alone as proof that the money invariant still holds.

For your final ten minutes, useful questions include which invariant the team finds hardest to maintain in its real systems, how it reviews AI-assisted changes, and what evidence it expects before a money-moving change is deployed.

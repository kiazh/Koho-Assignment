# Move the Money

A small Go and SQLite HTTP service for opening accounts, transferring integer cents, and reading balances and transaction history. It is an interview exercise, with the financial rules and their concurrency tests kept visible.

## Read this repository

| If you want to... | Read |
| --- | --- |
| Run the service and understand its API | This README |
| Review the assignment submission and AI catches | [Build log](docs/BUILD_LOG.md) |
| Follow the implementation, tests, and issue trail | [Implementation journal](docs/IMPLEMENTATION_JOURNAL.md) |
| Prepare the live demo and interview discussion | [Presentation guide](docs/PRESENTATION_GUIDE.md) |
| Understand storage and scope decisions | [Technical decisions](docs/TECH_STACK_DECISIONS.md) |
| See the original brief | [Assignment text](docs/Koho-assignment.txt) |

The files under [`docs/_historical/`](docs/_historical/) — the pre-build plan, expanded specification, agent-instruction draft, and log template — are planning inputs kept for transparency. Some features proposed there were deliberately excluded; the code and tests describe what is shipped.

## Run locally

Install Go 1.22 or newer, then from this directory:

```sh
go mod download
go run ./cmd/server
```

The server listens on `127.0.0.1:8080` and stores data in `ledger.db`. Set `ADDR` and `LEDGER_DB` to change those defaults. If Windows reserves port 8080, set `$env:ADDR = '127.0.0.1:18178'` before starting and use that port in the requests below. No external database or service is needed.

Run the full suite and the most important race proof:

```sh
go test -race -count=1 ./...
go test -race -count=10 -run 'TestCompetingTransfersCannotDoubleSpend' ./internal/ledger
```

On PowerShell, use double quotes around the `-run` test name. `go vet ./...` is the other verification gate.

## Try the API

In a second PowerShell terminal:

```powershell
$a = Invoke-RestMethod -Method Post -Uri http://127.0.0.1:8080/accounts -ContentType application/json -Body '{"initial_balance_cents":10000}'
$b = Invoke-RestMethod -Method Post -Uri http://127.0.0.1:8080/accounts -ContentType application/json -Body '{"initial_balance_cents":0}'
$body = @{from_account_id=$a.id; to_account_id=$b.id; amount_cents=2500; idempotency_key=[guid]::NewGuid().ToString('N')} | ConvertTo-Json
$transfer = Invoke-RestMethod -Method Post -Uri http://127.0.0.1:8080/transfers -ContentType application/json -Body $body
Invoke-RestMethod "http://127.0.0.1:8080/accounts/$($a.id)/balance"
Invoke-RestMethod "http://127.0.0.1:8080/accounts/$($b.id)/transactions"
```

Repeat the transfer request with the same body and key: the response has the original transfer ID and the balances do not change again. A changed amount with the same key returns `409`.

| Route | Success | Relevant errors |
| --- | --- | --- |
| `POST /accounts` with `initial_balance_cents` | `201` account | `400` invalid amount/JSON |
| `GET /accounts/{id}/balance` | `200` exact cents | `404` unknown account |
| `GET /accounts/{id}/transactions` | `200` ordered transfers | `404` unknown account |
| `POST /transfers` with account IDs, positive `amount_cents`, and `idempotency_key` | `201` new, `200` replay | `400` invalid request, `404` unknown account, `409` key mismatch, `422` insufficient funds or receiver overflow |

Amounts are JSON integers in cents. Decimal values, exponents, numeric strings, zero or negative transfer amounts, and values outside `int64` are rejected. An idempotency key is required and may contain at most 255 bytes. Only a **successful** transfer reserves a key. If a transfer response is lost or returns 500, retry the **same payload and key**; a new key means a new transfer. Account opening has no idempotency key, so check whether an account was created before retrying it. Application-route errors have a short JSON `error` code; the Go router returns plain-text 404/405 responses for unknown routes or methods.

## Why the money rules hold

`internal/ledger/transfer.go` validates the request, starts `BEGIN IMMEDIATE` on one SQLite connection, checks for an existing idempotency key, and then checks balances. A conditional sender debit, checked receiver credit, transfer record, and two signed ledger entries all commit together. Any error rolls the transaction back. A database `CHECK` prevents negative balances even if application validation regresses; the transfer key has a database `UNIQUE` constraint.

Starting funds live in `accounts.initial_balance_cents`. A transfer creates one negative and one positive ledger entry, so its entry sum is zero. The account audit equation is `opening balance + incoming transfers - outgoing transfers = current balance`. SQLite WAL supports readers while a writer holds its transaction; writes serialize. The two-request race test opens **separate store handles on the same database file** and releases the competing calls through one start gate. The tests also cover 50-way contention, concurrent retry, rollback after an injected ledger failure, bidirectional traffic, and a seeded 500-attempt audit.

## What I chose not to build

The brief accepts an API, CLI, or library and requires no UI, auth, or deployment. I built the API and deliberately cut everything else to protect correctness within the four-hour cap:

- **No user interface.** Exposed only through the HTTP JSON API.
- **No authentication, authorization, or KYC.** Anyone who can reach this local demo can open a funded account, so it must stay on localhost and must not be exposed as a real banking service.
- **No multi-currency or FX.** One currency, `int64` minor units only.
- **No Docker, Kubernetes, or cloud deployment.** A clean checkout runs with no services.
- **No distributed consensus or event brokers.** The ACID guarantees live inside one SQLite transaction; there is no two-phase commit across services.

## What I would do next

With a fifth hour I would add bounded history pagination and an operator reconciliation command that compares every stored balance against opening funds plus ledger entries. Before any production use I would define an account-opening retry policy (opening is not idempotent today) and an idempotency-key retention policy, add authentication and a real funding policy, and add observability and migrations. The next larger architectural step would be PostgreSQL row locks if multiple service nodes or higher write throughput were required. The [technical decisions](docs/TECH_STACK_DECISIONS.md), [implementation journal](docs/IMPLEMENTATION_JOURNAL.md), and [build log](docs/BUILD_LOG.md) explain the trade-offs and engineering catches.

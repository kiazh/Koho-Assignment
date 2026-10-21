# Move the Money

I built a small Go and SQLite HTTP service that opens accounts, transfers integer cents, moves money between them, and reads back balances and history. It is an interview exercise, so I kept the financial rules and the tests that prove them visible in the code rather than burying them.

The four rules I committed to never breaking:

1. An account's balance can never go negative — not under any sequence of requests, including ones that arrive at the same moment.
2. A transfer is all or nothing — there is no state of the world where money left one account and did not arrive at the other.
3. The same transfer submitted twice is applied once — networks retry and users double-click, and I assume both will happen.
4. Amounts are exact — everything is `int64` cents, end to end, so no cent can be lost to rounding.

## Read this repository

| If you want to... | Read |
| --- | --- |
| Run the service and understand its API | This README |
| Review the submission and where AI was wrong | [Build log](docs/BUILD_LOG.md) |
| Follow the implementation, tests, and issue trail | [Implementation journal](docs/IMPLEMENTATION_JOURNAL.md) |
| Prepare the live demo and interview discussion | [Presentation guide](docs/PRESENTATION_GUIDE.md) |
| Understand storage and scope decisions | [Technical decisions](docs/TECH_STACK_DECISIONS.md) |
| See every design choice and rejected alternative | [Decision log](docs/DECISIONS.md) |
| See the original brief | [Assignment text](docs/Koho-assignment.txt) |

The files under [`docs/_historical/`](docs/_historical/) — the pre-build plan, expanded specification, agent-instruction draft, and log template — are planning inputs I kept for transparency. Some features proposed there were deliberately excluded; the code and tests describe what actually shipped.

## How to run it

I chose Go and a pure-Go SQLite driver so that you can run the whole thing from a clean checkout — no Docker, no Postgres, no credentials. Install Go 1.22 or newer, then from this directory:

```sh
go mod download
go run ./cmd/server
```

The server listens on `127.0.0.1:8080` and stores data in `ledger.db`. No external database or service is needed. I set `ADDR` and `LEDGER_DB` to change the port or database path.

One honest note about Windows: port 8080 is sometimes reserved on this machine, and the server fails to bind there. If that happens, start it on a free port instead:

```powershell
$env:ADDR = '127.0.0.1:18178'
go run ./cmd/server
```

## How to run the tests

```sh
go vet ./...
go test -race -count=1 ./...
```

The single most important proof is the two-request double-spend race. I run it on its own, several times, before I trust it:

```sh
go test -race -count=10 -run 'TestCompetingTransfersCannotDoubleSpend' ./internal/ledger
```

On PowerShell, use double quotes around the `-run` test name. `go vet ./...` is the other gate I run. The [implementation journal](docs/IMPLEMENTATION_JOURNAL.md) maps every test to the invariant it proves, and it is honest about which tests prove cross-connection locking versus in-process serialisation.

## Try the API

Start the server in one terminal on a free port, then in a second terminal:

```powershell
$env:ADDR = '127.0.0.1:18178'
$base = 'http://127.0.0.1:18178'

# open two accounts
$a = Invoke-RestMethod -Method Post -Uri "$base/accounts" -ContentType application/json -Body '{"initial_balance_cents":10000}'
$b = Invoke-RestMethod -Method Post -Uri "$base/accounts" -ContentType application/json -Body '{"initial_balance_cents":0}'

# send 2500 cents from a to b, with a fresh idempotency key
$key = [guid]::NewGuid().ToString('N')
$body = @{ from_account_id = $a.id; to_account_id = $b.id; amount_cents = 2500; idempotency_key = $key } | ConvertTo-Json -Compress
$transfer = Invoke-RestMethod -Method Post -Uri "$base/transfers" -ContentType application/json -Body $body

# replay the exact same request: same transfer ID, no second debit
$replay = Invoke-RestMethod -Method Post -Uri "$base/transfers" -ContentType application/json -Body $body

# read the results
[pscustomobject]@{
    SameTransfer     = ($transfer.id -eq $replay.id)                          # True
    SourceCents      = (Invoke-RestMethod "$base/accounts/$($a.id)/balance").balance_cents            # 7500
    DestinationCents = (Invoke-RestMethod "$base/accounts/$($b.id)/balance").balance_cents            # 2500
    HistoryRows      = (Invoke-RestMethod "$base/accounts/$($b.id)/transactions").transactions.Count  # 1
}
```

I expect `SameTransfer=True`, `SourceCents=7500`, `DestinationCents=2500`, `HistoryRows=1`. Reusing the same request returns the original transfer ID; changing the key means a new transfer.

| Route | Success | Relevant errors |
| --- | --- | --- |
| `POST /accounts` with `initial_balance_cents` | `201` account | `400` invalid amount/JSON |
| `GET /accounts/{id}/balance` | `200` exact cents | `404` unknown account |
| `GET /accounts/{id}/transactions` | `200` ordered transfers | `404` unknown account |
| `POST /transfers` with account IDs, positive `amount_cents`, and `idempotency_key` | `201` new, `200` replay | `400` invalid request, `404` unknown account, `409` key mismatch, `422` insufficient funds or receiver overflow |

Amounts are JSON integers in cents. Decimal values, exponents, numeric strings, zero or negative transfer amounts, and values outside `int64` are rejected. An idempotency key is required and may contain at most 255 bytes. Only a **successful** transfer reserves a key. If a transfer response is lost or returns `500`, the right move is to retry the **same payload and key**; a new key means a new transfer. Account opening has no idempotency key, so I check whether an account was created before retrying it — that is the other retry trap besides transfers, and it is a limitation I am not comfortable with (see the build log). Application-route errors return a short JSON `error` code; the Go router returns plain-text `404`/`405` responses for unknown routes or methods.

## Why the money rules hold

`internal/ledger/transfer.go` validates the request, starts `BEGIN IMMEDIATE` on one SQLite connection, checks for an existing idempotency key, and then checks balances. A conditional sender debit, a checked receiver credit, the transfer record, and two signed ledger entries all commit together. Any error rolls the whole transaction back. A database `CHECK (balance_cents >= 0)` prevents negative balances even if my application validation regresses, and the transfer key has a database `UNIQUE` constraint.

Starting funds live in `accounts.initial_balance_cents`. A transfer writes one negative and one positive ledger entry, so its entry pair sums to zero. The account audit equation is `opening balance + incoming transfers - outgoing transfers = current balance`. SQLite WAL lets readers operate while a writer holds its transaction; writes serialize. The decisive test opens **separate store handles on the same database file** and releases the competing calls through one start gate — that is what proves the file lock holds, not just the in-process queue. The tests also cover 50-way contention, concurrent retry, rollback after an injected ledger failure, bidirectional traffic, and a seeded 500-attempt audit.

## What I chose not to build

The brief accepts an API, CLI, or library and requires no UI, auth, or deployment. I built the API and deliberately cut everything else to protect correctness within the four-hour cap:

- **No user interface.** Exposed only through the HTTP JSON API.
- **No authentication, authorization, or KYC.** Anyone who can reach this local demo can open a funded account, so it must stay on localhost and must not be exposed as a real banking service.
- **No multi-currency or FX.** One currency, `int64` minor units only.
- **No Docker, Kubernetes, or cloud deployment.** A clean checkout runs with no services.
- **No distributed consensus or event brokers.** The guarantees live inside one SQLite transaction; there is no two-phase commit across services.

## What I would do next

With a fifth hour I would add bounded history pagination and an operator reconciliation command that compares every stored balance against opening funds plus ledger entries. Before any production use I would define an account-opening retry policy (opening is not idempotent today) and an idempotency-key retention policy, add authentication and a real funding policy, and add observability and migrations. The next larger architectural step would be PostgreSQL row locks if multiple service nodes or higher write throughput were required. The [technical decisions](docs/TECH_STACK_DECISIONS.md), [implementation journal](docs/IMPLEMENTATION_JOURNAL.md), and [build log](docs/BUILD_LOG.md) explain the trade-offs and engineering catches.

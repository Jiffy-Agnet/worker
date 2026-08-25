# Jiffy Worker

Standalone, stateless execution component for Jiffy. Connects to a shared
Redis Stream, consumes programming-task descriptors published there, and
executes each one inside an isolated sandbox.

See [`docs/adr/ADR-distributed-stateless-workers.md`](docs/adr/ADR-distributed-stateless-workers.md)
for the full architecture decision behind this component.

## Status

Early scaffold. Every function under `internal/` is a stub with a `TODO`
pointing at the relevant ADR section — see the ADR's Action Items for the
current build-out plan.

## Layout

- `cmd/worker` — entrypoint: starts the asynq server and the stream consumer
- `internal/config` — environment-based configuration (no persisted state)
- `internal/stream` — Redis Stream consumer-group bridge into the local asynq queue
- `internal/task` — the task execution flow (clone/checkout/env/prompt handoff/pre-setup/report)
- `internal/sandbox` — sandbox container lifecycle
- `internal/callback` — HTTP callback reporting back to the producer
- `docs/adr` — architecture decision records

## Requirements

- Go 1.23+
- Docker (to run the sandbox image)
- Network access to the shared Redis instance (TLS required)

## Getting started

```bash
go mod tidy
go build ./...
```

`go.sum` is intentionally not committed yet — run `go mod tidy` in an
environment with access to the Go module proxy to generate it.

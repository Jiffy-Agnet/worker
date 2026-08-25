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

## Configuration (environment variables)

| Variable | Default | Purpose |
|---|---|---|
| `REDIS_ADDR` | `127.0.0.1:6379` | Shared Redis instance (Stream + local asynq queue) |
| `REDIS_PASSWORD` | — | Redis auth |
| `REDIS_TLS` | `true` | Redis TLS |
| `JIFFY_TASK_STREAM` | `jiffy:tasks` | Stream name for the Gateway<->Worker boundary |
| `JIFFY_CONSUMER_GROUP` | `jiffy-workers` | Redis Streams consumer group |
| `HOSTNAME` | `worker-unknown` | Consumer name within the group |
| `WORKER_CONCURRENCY` | `1` | Max sandbox executions this Worker runs in parallel |
| `JIFFY_SANDBOX_IMAGE` | — | Default sandbox image reference, if not set per-task |
| `JIFFY_CALLBACK_SECRET` | — | Shared secret for authenticating callbacks to the producer |
| `JIFFY_REPO_CACHE_DIR` | OS temp dir | Base directory for cached repo clones |
| `JIFFY_SANDBOX_MEMORY_LIMIT` | unset (no limit) | `docker run --memory` per sandbox — size so `WORKER_CONCURRENCY x` this fits the host's RAM |
| `SANDBOX_MEMORY_SWAP_LIMIT` | unset (no limit) | `docker run --memory-swap` per sandbox |
| `JIFFY_SANDBOX_CPU_LIMIT` | unset (no limit) | `docker run --cpus` per sandbox |
| `JIFFY_SANDBOX_CLEANUP` | `true` | Whether to `--rm` the container on exit; set `false` to leave it running for debugging |
| `SANDBOX_CONTAINER_TTL_HOURS` | unset (no backstop) | Hard limit on container lifetime, independent of the cleanup setting and of task status — force-removed after this many hours regardless |
| `SANDBOX_ENV_PASSTHROUGH` | unset | Comma-separated names of Worker's own env vars to forward into every sandbox container as-is (e.g. `OPEN_API_BASE_URL,OPEN_API_KEY`) — only names are configured here, values are read from Worker's own environment |
| `CALLBACK_MAX_ATTEMPTS` | `3` | Local delivery attempts for a single callback before queuing it for the producer to retry |
| `CALLBACK_RETRY_BACKOFF_SECONDS` | `2` | Base delay between local callback attempts (doubles each attempt) |
| `FAILED_CALLBACK_STREAM` | `jiffy:failed-callbacks` | Redis Stream a callback is queued on once local retries are exhausted |
| `SANDBOX_DETECTION` | unset | `pattern=key` pairs, comma-separated, checked in order against the repo root (e.g. `Gemfile=ruby,Cargo.toml=rust,*.csproj=dotnet`) — first match wins |
| `SANDBOX_IMAGE_MAP` | unset | `key=image` pairs, comma-separated, mapping a detected key to an actual sandbox image (e.g. `ruby=jiffy-sandbox-ruby:1.0.0,rust=jiffy-sandbox-rust:1.0.0`) — falls back to `JIFFY_SANDBOX_IMAGE` if nothing matches or the key has no image mapped |

## Getting started

```bash
go mod tidy
go build ./...
```

`go.sum` is intentionally not committed yet — run `go mod tidy` in an
environment with access to the Go module proxy to generate it.

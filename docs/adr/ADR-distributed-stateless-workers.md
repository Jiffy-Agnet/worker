# ADR-XXX: Distributed, Stateless Sandbox Workers (Go + asynq)

**Status:** Proposed
**Date:** 2026-08-24
**Deciders:** Lo0ser (lead architect)

## Context

Development throughput is currently limited by single-machine execution capacity for programming tasks. This ADR defines a standalone Worker component: it connects to Redis, consumes programming-task descriptors published there, and executes them inside isolated sandboxes. This allows execution capacity to scale horizontally across independent, low-cost machines — a small VPS, a spare laptop, a Raspberry Pi — without depending on further detail about the system that produces those tasks.

This ADR builds on the existing Scheduled Phase Execution ADR, which introduced a Redis-based semaphore to serialize phase execution, and on the existing no-autonomous-task-selection principle: a new phase/subtask may only start without a fresh human trigger when that chain was already explicitly started by a human for a specific project — never for unrelated, newly-selected Issues.

Ordering of dependent tasks (e.g. task 2 must not start before task 1 finishes) is out of scope for this repository: it is enforced by not publishing task 2's descriptor onto the Redis Stream until task 1 completes. Worker never sees a task before it is meant to run, so it needs no dependency-checking logic of its own.

## Decision

Rewrite the Worker as a standalone, stateless Go service using asynq, connected to the task producer through a standard, tool-independent Redis protocol.

### 1. Worker: standalone Go binary, fully stateless

- Separate repository/binary, cross-compiled for Linux/macOS/Windows and amd64/arm64 — deployable on a VPS, a spare laptop, or a Raspberry Pi.
- Holds no database and no long-lived state between tasks, other than a shared local mirror clone per repository (a performance cache only — see item 2, step 1). Each task's actual working copy is a fresh, isolated clone that is removed once the task finishes.
- Capacity is added simply by starting another Worker instance anywhere with Docker and network access to Redis.

### 2. Task execution flow

1. **Clone or update:** maintain one shared, local *mirror* clone per repository, updated via `git fetch` on every task (never a `git pull` into a working tree — the mirror has no working tree). From that local mirror, clone a fresh, fully isolated working copy for this task alone, then point its `origin` at the real repository URL so the code agent can push to it. Concurrent tasks on the same repository each get their own working copy this way — one task's checkout, changes, or failure can never touch another's. The working copy is removed once the task finishes.
2. **Environment setup:** configure the Sandbox's required config and environment variables for this run, including the Active Branch name and the short-lived credential. The code agent uses these to `git pull` on its own branch, switch to (or create) the Active Branch, and later push and open a PR — none of that is something Worker does via git itself.
3. **Task + system prompt handoff via /tmp:** write the task description and the system prompt to separate files under `/tmp`, each named to include the Issue ID (e.g. `/tmp/jiffy-task-<issue_id>.md`, `/tmp/jiffy-prompt-<issue_id>.md`). The code agent reads the task and system prompt from these file paths instead of receiving them as CLI arguments or piped input, removing any length limit on task content.
4. **Pre-setup / entrypoint script:** if the project defines a pre-setup or entrypoint script, run it inside the Sandbox first, before invoking the code agent.
5. **Execute and report:** launch the code agent to perform the task; on completion, send the report back to the producer as a **callback**, not as a return value on the queue.

Security requirement: the Redis connection must be TLS-encrypted and authenticated, since task descriptors carry credentials to potentially remote, less-controlled hardware.

### 3. Dispatch protocol: Redis Streams + Consumer Groups

The producer is a Python service using its own task-queue library (Celery) internally; a Go/asynq consumer cannot attach to a Celery-produced queue directly, since the two do not share a wire format. Decision: task descriptors are published as JSON on a **Redis Stream** — including an explicit `schema_version` field for forward compatibility — consumed via `XREADGROUP` / consumer groups, independent of both Celery and asynq internals.

- Either side can change its internal task-queue library later without breaking the other, since the actual contract is the Stream's JSON schema, not either library's format.
- Consumer-group semantics give the concurrency model "for free": a Worker only claims a new message when it has a free execution slot for its own configured concurrency. No central custom scheduler is introduced.

### 4. Sandbox image: pre-built, multi-arch, registry-only

The sandbox image is built once in CI with `buildx` for both amd64 and arm64, pushed to a registry. Every Worker — regardless of host architecture, including a Raspberry Pi — pulls the same versioned reference; no Worker builds the image locally.

### 5. Capacity model: sum of Worker concurrency, no central scheduler

Total system throughput is the sum of each connected Worker's own configured concurrency, sized to that node's hardware. No additional resource-gating component is introduced.

### 6. Phase-ordering semaphore: re-scoped from global to per-project

The existing Redis semaphore (previously global, default capacity 1) is re-scoped to a per-project/per-dependency-chain key, e.g. `lock:project:<id>`. This preserves the original purpose — don't start phase N+1 of a project before phase N finishes — while letting unrelated projects, or independent standalone Issues, run fully in parallel across the Worker pool.

### 7. Parallel sub-tasks: isolated branch + PR per sub-task

When a phase is decomposed into parallel sub-tasks distributed across Workers, each sub-task branches independently off the current Active Branch and opens its own PR. No two Workers commit to a shared branch.

### 8. Sibling-PR conflict handling

When sibling PRs from parallel sub-tasks of the same phase conflict, the Orchestrator (itself an agent) first attempts to resolve the conflict automatically. If it cannot, the Issue is tagged **Human Review** — reusing the existing Human Review status/webhook pattern — so the project manager can step in.

### 9. Worker observability: consumer-group membership only

No separate Worker registration or heartbeat mechanism is introduced at this phase. Redis consumer-group membership is sufficient for now; may be revisited if the Admin Panel later needs finer-grained Worker health data.

## Options Considered

### Dispatch protocol

| Dimension | Producer-native protocol | Raw asynq protocol from producer | Redis Streams (chosen) |
|---|---|---|---|
| Complexity | Low to implement, high to maintain | High — reverse-engineers an evolving Go library's internal schema | Low; both languages have first-class support |
| Coupling | Locks Worker to producer's library forever | Locks producer to asynq's internals forever | Neither side is coupled to the other's library |
| Future rewrites | Breaks if Worker changes stack again | Breaks if producer changes stack again | Either side can be rewritten independently |

**Decision:** Redis Streams, for the coupling and longevity reasons above.

### Worker capacity control

| Dimension | Central custom scheduler | Native per-worker concurrency (chosen) |
|---|---|---|
| Complexity | New component to design, run, and keep consistent | None — already built into queue consumption |
| Fit with "lightweight, low-cost" principle | Adds infrastructure | Adds nothing new |

**Decision:** no central scheduler; total capacity is the sum of Worker concurrency settings.

## Consequences

**Easier:**
- Horizontal scaling of sandbox capacity using low-cost or already-owned hardware.
- No length limit on task/system-prompt content, since it's handed off via file, not CLI/pipe.
- Either side of the Redis boundary can change its internal stack later without breaking the other.

**Harder / new costs:**
- Redis must be reachable — securely — from remote/distributed nodes (TLS + auth becomes mandatory).
- A new CI requirement: multi-arch (`buildx`) builds for the sandbox image.
- The Stream task-descriptor schema must be formally defined and versioned so both sides don't drift apart.

## Action Items

1. [ ] Define the Redis Stream task-descriptor JSON schema (repo URL, short-lived credential, Active Branch, Issue ID, callback URL, project/phase lock key, `schema_version`).
2. [ ] Scaffold the standalone Go/asynq Worker repository.
3. [ ] Implement the task execution flow (clone-or-fetch, env/config setup incl. Active Branch name, /tmp task+prompt handoff, pre-setup script, agent invocation, callback report).
4. [ ] Re-scope the existing phase semaphore from global to per-project key.
5. [ ] Add multi-arch (`buildx`) build step to the sandbox image CI pipeline.
6. [ ] Harden Redis: TLS + auth for remote Worker connections.
7. [ ] Implement sibling-PR conflict resolution in the Orchestrator, with Human Review tagging as fallback.

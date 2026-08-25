# ADR-XXX: Distributed, Stateless Sandbox Workers (Go + asynq)

**Status:** Proposed
**Date:** 2026-08-24
**Deciders:** Lo0ser (lead architect)

## Context

Development throughput is currently limited by single-machine execution capacity for programming tasks. This ADR defines a standalone Worker component: it connects to Redis, consumes task descriptors published there, and executes them inside isolated sandboxes. This allows execution capacity to scale horizontally across independent, low-cost machines — a small VPS, a spare laptop, a Raspberry Pi — without depending on further detail about the system that produces those tasks.

This ADR builds on the existing Scheduled Phase Execution ADR, which introduced a Redis-based semaphore to serialize phase execution, and on the existing no-autonomous-task-selection principle: a new phase/subtask may only start without a fresh human trigger when that chain was already explicitly started by a human for a specific project — never for unrelated, newly-selected Issues.

Ordering of dependent tasks (e.g. task 2 must not start before task 1 finishes) is out of scope for this repository: it is enforced by not publishing task 2's descriptor onto the Redis Stream until task 1 completes. Worker never sees a task before it is meant to run, so it needs no dependency-checking logic of its own.

## Decision

Rewrite the Worker as a standalone, stateless Go service using asynq, connected to the task producer through a standard, tool-independent Redis protocol.

### 1. Worker: standalone Go binary, fully stateless

- Separate repository/binary, cross-compiled for Linux/macOS/Windows and amd64/arm64 — deployable on a VPS, a spare laptop, or a Raspberry Pi.
- Holds no database and no long-lived state between tasks, other than a shared local mirror clone per repository (a performance cache only — see item 2, step 1). Each task's actual working copy is a fresh, isolated clone that is removed once the task finishes.
- Capacity is added simply by starting another Worker instance anywhere with Docker and network access to Redis.

### 2. Task execution flow

The task descriptor is the producer's own payload, exactly as published — Worker does not add fields to it:

```json
{
  "repo": { "url": "string", "token": "string", "username": "string" },
  "issue": {
    "text": "string",
    "turns": [
      { "role": "user", "author": "string", "body": "string", "created_at": "2026-08-25T17:33:50.127Z" }
    ],
    "external_issue_id": "string"
  },
  "callback": { "url": "string", "secret": "string" }
}
```

Notably, there is **no** active-branch, sandbox-image, or system-prompt field in this payload. Those are handled as follows:

1. **Clone or prepare working copy:** maintain one shared, local *mirror* clone per repository, updated via `git fetch` on every task (never a `git pull` into a working tree — the mirror has no working tree). From that local mirror, clone a fresh, fully isolated working copy for this task alone, point its `origin` at the real repository URL, and configure the push/pull credential (`repo.username` + `repo.token`) directly into *that* working copy's `.git/config` — since it is single-task and gets deleted afterward, this is safe, and it means the code agent can run plain `git push`/`git pull` without ever having to handle the token itself. Concurrent tasks on the same repository each get their own working copy this way — one task's checkout, changes, or failure can never touch another's. The working copy is removed once the task finishes.
2. **Task file:** compose the task content from `issue.text` followed by `issue.turns` (in order), and write it to `/tmp/jiffy-task-<external_issue_id>.md`. The code agent reads the task from this file path instead of receiving it as a CLI argument or piped input, removing any length limit on task content. There is no separate system-prompt file: project-specific agent instructions live in the repository's own `AGENTS.md`, which the agent reads directly from the mounted working copy at `/workspace` — Worker never copies or duplicates it. Branch selection (switching to, or creating, a branch) and everything after that — commits, push, PR creation — is entirely the code agent's own job, driven by the task content and `AGENTS.md`.
3. **Pre-setup / entrypoint script:** if the project defines a pre-setup or entrypoint script, run it inside the Sandbox first, before invoking the code agent.
4. **Execute and report:** launch the code agent to perform the task; on completion, send the report back to the producer as a **callback** to `callback.url`, authenticated with `callback.secret` — not as a return value on the queue.

The sandbox image itself is a Worker-level default (`JIFFY_SANDBOX_IMAGE`, see internal/config), not part of the per-task payload.

Security requirement: the Redis connection must be TLS-encrypted and authenticated, since task descriptors carry credentials to potentially remote, less-controlled hardware.

### 3. Dispatch protocol: Redis Streams + Consumer Groups

The producer is a Python service using its own task-queue library (Celery) internally; a Go/asynq consumer cannot attach to a Celery-produced queue directly, since the two do not share a wire format. Decision: task descriptors are published as JSON on a **Redis Stream**, consumed via `XREADGROUP` / consumer groups, independent of both Celery and asynq internals.

- Either side can change its internal task-queue library later without breaking the other, since the actual contract is the Stream's JSON schema (the payload shown above), not either library's format.
- Consumer-group semantics give the concurrency model "for free": a Worker only claims a new message when it has a free execution slot for its own configured concurrency. No central custom scheduler is introduced.

### 4. Sandbox image: pre-built, multi-arch, registry-only

The sandbox image is built once in CI with `buildx` for both amd64 and arm64, pushed to a registry. Every Worker — regardless of host architecture, including a Raspberry Pi — pulls the same versioned reference (a Worker-level config default); no Worker builds the image locally.

### 5. Capacity model: sum of Worker concurrency, no central scheduler

Total system throughput is the sum of each connected Worker's own configured concurrency, sized to that node's hardware. No additional resource-gating component is introduced.

### 6. Phase-ordering semaphore: re-scoped from global to per-project

The existing Redis semaphore (previously global, default capacity 1) is re-scoped to a per-project/per-dependency-chain key, e.g. `lock:project:<id>`. This preserves the original purpose — don't start phase N+1 of a project before phase N finishes — while letting unrelated projects, or independent standalone Issues, run fully in parallel across the Worker pool. This lives on the producer/Orchestrator side, not in this repository.

### 7. Parallel sub-tasks: isolated branch + PR per sub-task

When a phase is decomposed into parallel sub-tasks distributed across Workers, each sub-task's code agent branches independently off the appropriate base and opens its own PR. No two Workers, or their agents, commit to a shared branch.

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

### Credential handling

| Dimension | Pass token as a sandbox env var | Configure directly into the per-task working copy's git config (chosen) |
|---|---|---|
| Exposure surface | Visible via `docker inspect`, process env dumps, crash logs | Scoped to one repo directory, standard git mechanism |
| Agent ergonomics | Agent must know to use the token itself | Agent just runs plain `git push`/`git pull` |
| Lifetime | Same as the container | Same as the ephemeral working copy — removed with it |

**Decision:** configure into the working copy's `.git/config`, since it is single-task and deleted afterward — safe in a way it would not be for the shared mirror cache.

## Consequences

**Easier:**
- Horizontal scaling of sandbox capacity using low-cost or already-owned hardware.
- No length limit on task content, since it's handed off via file, not CLI/pipe.
- The code agent never has to handle a raw token — it just runs ordinary git commands.
- Either side of the Redis boundary can change its internal stack later without breaking the other.

**Harder / new costs:**
- Redis must be reachable — securely — from remote/distributed nodes (TLS + auth becomes mandatory).
- A new CI requirement: multi-arch (`buildx`) builds for the sandbox image.

**Will need to revisit:**
- If the payload schema needs to evolve later, a versioning strategy (e.g. a Stream-message-level field, separate from the JSON body) will need its own decision — not assumed here.

## Action Items

1. [x] Confirm the task descriptor schema with the producer team (repo/issue/callback, as documented above).
2. [x] Scaffold the standalone Go/asynq Worker repository.
3. [x] Implement the task execution flow (clone-or-fetch + isolated working copy + git credential config, task file composition, pre-setup script, agent invocation, callback report).
4. [ ] Re-scope the existing phase semaphore from global to per-project key (producer/Orchestrator side).
5. [ ] Add multi-arch (`buildx`) build step to the sandbox image CI pipeline.
6. [ ] Harden Redis: TLS + auth for remote Worker connections.
7. [ ] Implement sibling-PR conflict resolution in the Orchestrator, with Human Review tagging as fallback.
8. [ ] Implement the actual HTTP callback (`internal/callback`), signing requests with `callback.secret`.
9. [ ] Add retry handling with a bounded attempt count, reporting final failure via callback after the limit is reached.

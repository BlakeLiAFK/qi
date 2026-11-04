# Durable bounded task queue

The daemon accepts independent task turns into a SQLite-backed FIFO queue.
`-max-concurrent` defaults to 8 executing turns; `-max-queued` defaults to 64
waiting turns. Both must be positive. A task may have only one queued or running
turn. More future turns for the same task are rejected as busy rather than mixed
into an executing transcript.

## Acceptance and dispatch

Normal messages, selected-skill messages and explicit question continuation all
use `AcceptQueued`. One transaction validates transcript revision and backlog
capacity, inserts the queued run, persists its accepted transcript, inserts a
monotonic dispatch sequence, and optionally records the consumed question card.
Failure rolls back all these writes. Skills are resolved before acceptance and
stored in that task's prompt, not reloaded later.

The single engine dispatcher selects the smallest committed sequence and marks
its run running before calling the factory or model. At most MaxConcurrent
workers execute; FIFO describes start order, not completion order. Factory
failure or panic becomes a durable failed run, retaining the accepted prompt,
and frees the slot for the next task. Worker completion wakes dispatch.

Every worker loads only its task's persisted transcript and checks its accepted
revision. Task-scoped tool wrappers, approval identity and workspace execution
locks remain independent of queue admission. Queue acceptance grants no new tool
permission. Pending approvals occupy their worker slot until resolved or expired.

## Cancellation, waiting and shutdown

A queued task can be cancelled without invoking its factory/model. The prompt
remains in history and the run becomes cancelled. Running cancellation signals
the context and does not release capacity until the worker actually exits. The
engine mutex serializes claims, cancellation and shutdown. Wait covers queued
and running work; the attached CLI polls through both states.

Graceful shutdown stops dispatch, cancels queued work and cancels/waits for active
work before releasing the owner lock. If durable dispatch or completion writes
fail, the engine stops admitting and dispatching work and returns the operational
error to waiters. It does not retry ambiguous work.

## Recovery policy

**Neither queued nor running work resumes automatically after restart.** Both
become interrupted, retaining accepted prompts and records for user review.
Queued work has not been dispatched, but the daemon configuration could have
changed; explicit resubmission is required. Running external tool outcomes may
be unknown. Executing approvals are reconciled as unknown and never replayed.
A new explicit turn inserts unknown-result markers for unanswered old tool calls.

This intentionally conservative policy avoids automatic execution under changed
model/tool configuration. Future automatic recovery requires a persisted
execution-configuration identity and a separate review policy. A cancelled or
interrupted run is not proof that an external action did not happen.

## Storage upgrade

The engine acquires its exclusive database ownership lock before the queue schema
upgrade. The migration transaction rebuilds the runs CHECK constraint to admit
queued status, preserves IDs and foreign-key targets, creates a unique index for
queued/running task ownership, and creates durable dispatch records. Foreign-key
and database consistency checks run before commit. The reserved connection's
foreign-key enforcement is restored and checked before returning. Existing
history, card events and approvals remain in place. Migration is idempotent.

`Run.StartedAt` remains the legacy admission timestamp, including for queued runs;
it is not the actual model invocation time. Queue order uses integer sequence,
not timestamps. List and Get expose queued/running/terminal state consistently.

## Questions and known limits

Answering a question only saves an answer. Explicit Continue accepts a new queued
turn and consumes that answered card atomically; queue-full leaves it retryable.
Continuation requires the latest completed run and cannot replay a consumed card.
Question expiry governs accepting the answer, not retaining an already accepted
answer: Continue may use it after expiry if the other checks still pass.

The HTTP API has no submission idempotency key. If a client loses an acceptance
response, it must inspect task history/state before manually retrying; the server
does not automatically resend prompts. Queue sequence records are retained with
run history. Queue bounds limit outstanding work, not total database growth.
Shared model/tool implementations supplied by a custom factory must themselves
be safe for concurrent use. SQLite and task wrappers are not a process sandbox.

## Verification

Storage tests cover migration with existing event/approval references, bounded
FIFO claiming, task ownership, queue cancellation, recovery and transaction
rollback. Engine tests cover bounded FIFO execution, transcript isolation,
queue-full no-write behavior, queued Wait, cancellation, factory failure and
shutdown. Continuation tests cover replay/stale/cross-task rejection and atomic
consumption. Run `go test ./...` and `go test -race ./...` after integration.

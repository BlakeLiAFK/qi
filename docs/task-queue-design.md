# Durable bounded task queue: implementation plan

Status: design only. The daemon currently runs up to `MaxConcurrent` independent
turns and rejects additional work. This document does not claim queue support.
The accompanying admission safety tests exercise the existing rejection behavior,
not a queue implementation.

## Why a channel or a capacity-check patch is insufficient

- `tasks.Engine.Start` writes a running run and its user transcript in separate
  commits. A crash between them can leave a run without its accepted prompt.
- `session.initTasks` forbids `queued` in the runs CHECK constraint and its unique
  index covers only `running`. `FinishRun` also only handles running rows.
- Both task snapshot and metadata queries must agree on status and ordering.
- `Engine.Wait`, cancellation and shutdown currently know only goroutines in the
  active map. A memory-only backlog would disappear on restart.
- The attached terminal treats every status other than `running` as finished.
- Question continuation consumes a card and starts a turn. It must participate in
  the same admission transaction rather than bypassing bounds or FIFO order.
- Pending approvals do not authorize replay after a crash. Running external work
  can have an unknown outcome even if the daemon recorded no successful result.

## First release scope and invariants

Queue independent tasks, not multiple future turns for one task. Retain a clear
busy error when a task already has any queued or running turn. This avoids
appending a new user message while that task's model/tool cycle is still writing
history. More than one queued turn per task is a separate transcript design.

Use explicit positive bounds: `MaxConcurrent` for executing runs and `MaxQueued`
for accepted, not-yet-started runs (suggest default 64). Do not make queue length
unbounded. Enforce task, prompt, expanded skills, message and event limits too.
Expose the bounds in daemon configuration, CLI help and documentation.

Admission returns success only after the prompt, run, queue ordering record and
any consumed continuation card commit together. Queue-full, busy, stale card,
revision conflict and cancellation-before-commit leave all of them unchanged.
Every successful admission has a stable run ID. Responses should include that ID
and the initial status; an HTTP timeout is an uncertain delivery, not proof of
rejection. Add a task-scoped caller submission ID and payload digest to make a
retry return the original admission, or reject mismatched reuse. Do not silently
retry a submission without that identifier.

FIFO is defined by a committed monotonic integer sequence, not a wall clock or
random run ID. It governs dispatch, not completion. A newly submitted task cannot
jump ahead of an already accepted task merely because a worker just finished.

## Storage and migration

Introduce a versioned migration under exclusive engine ownership. Current Open
initializes tables before Engine.New acquires ownership; a schema-changing queue
migration must not run in that window against a live old daemon. Use an explicit
migration command requiring the same ownership lock, or move ownership before
migrations. Back up and test old databases, including ones with approvals/events.

Rebuild the runs table to allow `queued`, retain its composite `(id,session_id)`
identity and foreign-key targets, and replace the unique index with one covering
both queued and running. Preserve old IDs, timestamps, error strings and all
foreign-key references. Do not disable foreign-key checks on a shared pool;
perform SQLite's documented table rebuild on a pinned exclusive connection and
verify `foreign_key_check` and `integrity_check` before publishing the version.
If migration or verification fails, roll back and refuse startup.

Add a durable dispatch table with an AUTOINCREMENT sequence, unique run ID,
composite run/task foreign key, accepted transcript revision, accepted time,
submission ID/digest, and an execution-configuration fingerprint. The fingerprint
covers model/provider identity (not credentials), enabled tools, skills snapshots
and execution configuration relevant to authorization. Skills content is already
captured in the accepted prompt; do not reload a modified skill at dispatch.
Submission IDs must remain queryable after dispatch for idempotent retries.
Use separate accepted and actual execution timestamps; do not rename the meaning
of existing `StartedAt` silently. Update all readers consistently.

## Atomic admission transaction

1. Validate/encode user input, selected skills and optional continuation data.
2. Under the engine scheduling mutex, open a short write transaction. Never call
   Store methods which acquire another pooled connection from inside it (the
   current pool has one connection). Share transaction-level helpers instead.
3. Check existing submission identity, task existence, expected revision, absence
   of queued/running task run, and the queue bound in that transaction. Bounds
   include all durable queued work, not just an in-memory cache.
4. Repair unanswered interrupted tool calls with explicit unknown-result markers.
   Validate the resulting history length including repairs and the new prompt.
5. Insert queued run and dispatch record, persist history with revision CAS,
   and, for question continuation, atomically consume the exact answered card
   revision once. No consumption when admission fails.
6. Commit. Only then signal the dispatcher and return acceptance. Request-context
   cancellation after commit cannot cancel the accepted run.

All entry points, including normal Start, StartWithSkills and explicit question
Continue, must use this path. Do not instantiate models or execute tools inside
the transaction. Factory setup failures after acceptance become durable failed
runs, with their accepted prompt still visible.

## Dispatcher and lifecycle

A single engine-owned dispatcher holds the same mutex as Start/Cancel/Close when
claiming work. With fewer than MaxConcurrent active workers, select the lowest
queued sequence, load its task-local persisted snapshot, and check configuration
and transcript revision. In one transaction CAS queued to running and record the
execution start. Only after that commit construct the task-scoped runner and
launch execution. Claim plus active-map insertion must be serialized with Cancel
and Close. A crash after claiming but before model invocation is conservatively
interrupted on recovery, never requeued automatically.

The dispatcher is the only path to launching runs. Worker completion releases its
slot and wakes it; cancellation of queued work also wakes it. Factory errors and
panics must record terminal state and continue to the next task without a hot
retry loop. Persist failures must fail closed: stop admitting/dispatching work if
its durable status cannot be established, expose an operational error, and leave
ambiguous running rows for restart reconciliation. Do not report success merely
because a goroutine exited.

Queued cancellation CASes queued to cancelled and records a terminal event in
one transaction; the accepted user prompt stays in history. Running cancellation
signals the run's context and waits for actual termination before releasing its
slot. Cancellation never proves an external action did not happen. The mutex and
CAS make cancellation-versus-claim have exactly one winner. Wait binds to a
specific run ID and waits through queued/running until a durable terminal state,
including factory failure; it must not return just because no worker exists yet.

Graceful shutdown stops admission and dispatch first, cancels/waits for active
runs and leaves unstarted queued runs durable for the next owner. Release the
engine lock only after active workers stop. Define this behavior in API and CLI
help rather than pretending shutdown cancelled queued work.

## Recovery and unknown external outcomes

After acquiring exclusive ownership, reconcile running runs to interrupted and
executing approvals to unknown, using the existing conservative semantics.
Never put an interrupted run back in the queue. Never execute a consumed approval
again. A later explicit user turn gets the existing unknown-result history marker
and must check the external outcome before a repeated action.

Only rows still durably queued have never been dispatched and are eligible for
startup dispatch. Load these in FIFO order. If the execution configuration no
longer matches their accepted fingerprint, fail them with a clear explanation or
hold them for explicit review; do not silently execute with expanded tool access.
Reject corrupted task/run links, inconsistent revisions and unknown schema
versions. Startup recovery cannot invent user approval or answer a question.

## Isolation and API/UI compatibility

Every query and mutation binds task ID plus run ID; every card operation binds
card ID, task ID, run ID and revision. Preserve independent transcripts and
per-run cancellation. Copy runner configuration and tool slices before adding
task-scoped tools. Shared factories/models/tools must be concurrency-safe;
serialization of SQLite does not make arbitrary tool instances safe. Preserve
existing per-workspace coding locks and one-use approval claims.

List/Get must expose queued status consistently, without loading all transcripts
for the sidebar. Show queue state without implying model work has started. A
queued task permits cancellation, but not another Start. The attached terminal
must poll queued runs rather than return the previous assistant answer. Continue
buttons must remain retryable after queue-full and must disable only after the
continuation was durably accepted. All entry points should use the same typed
busy/full/closed/stale errors. Describe queue-full as rejection, not acceptance.

## Required release tests (not implemented by this design)

- Capacity 1: hold A, accept B/C, reject D at bounded queue capacity, then verify
  exact FIFO model-entry order and no cross-task prompt/tool/card leakage.
- Two simultaneous requests for one idle task: precisely one accepted; loser
  leaves no prompt, queue row or consumed card. Test separate Store connections.
- Inject failure at each admission write and before commit: all-or-nothing run,
  transcript, sequence record and continuation consumption. Reopen the database.
- Submission retries after lost HTTP response: same run, no duplicate prompt;
  changed body with the same key is rejected.
- Queue cancellation before dispatch, cancellation at claim barrier and active
  cancellation: no cancelled queued model call; no early slot release.
- Simulated process death before admission commit, after commit, after claim and
  after external execution before result persistence. Only never-claimed queued
  work resumes; claimed work interrupts and external outcome remains unknown.
- Pending/approved/executing/succeeded approvals across restart cannot become new
  authorization. Continue replay and cross-task card IDs remain rejected.
- Graceful Close with backlog; two competing owners; migration against old DBs;
  unchanged history/event/approval IDs; migration failure rollback; bad FK rows.
- Skills/config changes while queued; invalid revision; factory error/panic; full
  history after interruption repair; terminal-write failure; bounded memory.
- CLI waits through queued/running and reports failure/cancellation accurately;
  WebUI cancel/continue refreshes; metadata/detail status snapshots agree.
- Run race tests and all package tests after integrating every entry point.

The current regression tests deliberately retain rejection assertions until the
entire queue implementation lands. Replace those assertions with acceptance/FIFO
checks as part of that change, not ahead of its transaction and recovery support.

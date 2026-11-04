# Idempotent turn admission

`POST /api/tasks/{id}/messages` accepts `idempotency_key` alongside `prompt` and
optional `skills`. Supply a client-generated UUID per logical turn; keep and
reuse it when the response is lost or ambiguous. An accepted keyed request
returns `{ "accepted": true, "run": { ... } }`. Every identical retry returns
that same **original queued acceptance**, including the run ID and timestamp,
even when the run has since finished. Fetch the task or event stream for its
current status. A changed request with that key receives HTTP 409.

The digest uses a versioned JSON representation of the exact decoded prompt and
ordered skill-name list. Omitted and empty skill lists are equivalent; JSON
whitespace and property order are irrelevant. Prompt whitespace and skill order
are significant. The selected skill snapshot is saved on initial acceptance;
retries do not reread modified or missing skill files. Invalid requests and
queue-full rejections consume no key.

The key namespace is `(authenticated principal, task ID, key)`. This server is
single-user, so the server-selected principal is `local-owner`, stable across
authentication-token rotation. Never take a principal from untrusted JSON.
A future multi-user server must authenticate and authorize task ownership before
calling Submit with its stable account principal.

Key records, transcript, queued run and FIFO dispatch record commit in the same
SQLite transaction. Engine serialization, exclusive engine ownership and the
SQLite active-run constraint protect admission. Keys are 1–128 printable ASCII
characters with no whitespace, retained for the task lifetime without expiry or
eviction. A hard cap of 10,000 keys per task bounds this metadata; further new
keyed turns reject at the cap while existing keys still replay. Create a new task
when the cap is reached. Records store a digest and original run response, not a
second copy of the prompt.

A process restart interrupts unfinished runs, **never automatically replays
execution**. Retrying a committed key finds its original run even if the process
died before dispatch or before the HTTP response. This is idempotent admission,
not an exactly-once guarantee for external tool effects. Inspect interrupted
runs before explicitly submitting new work with a new key.

The browser generates UUIDs and retains a pending request key in memory and
sessionStorage until acceptance is received. That protects same-tab retry and
reload when sessionStorage is available. Browser storage disabled/cleared or a
new tab cannot recover an unknown key; inspect task history before resubmitting.
The stored pending signature includes the prompt and selected names and is
removed after known acceptance. Compatibility callers that omit a key use the
legacy non-idempotent path and must not automatically retry uncertain requests.
Task creation has its own idempotency namespace (below); question-action endpoints
are separate from turn admission.

Tests cover concurrent same-key requests, principal/task separation, mismatched
payloads, mutation-free rollback at the identity write, saturated-queue replay,
capacity without eviction, durable database reopen, crash after atomic commit,
restart without execution, mutable skills, authenticated HTTP routing and browser
key retention/reload.

## Task creation

`POST /api/tasks` also accepts `idempotency_key` alongside `title`. Its namespace
is `(authenticated principal, key)`, separate from turn-admission keys. Titles
are trimmed before hashing a versioned request; identical creation retries
return the original task ID and immutable initial task response with HTTP 202.
A different normalized title for an existing key returns 409. Use GET to retrieve
current task status. Replaying creation does not execute a turn or unarchive a
task.

Task and creation-key record commit atomically. Each keyed task has exactly one
creation record; retention is indefinite, no silent eviction. Keys use the same
1–128 printable ASCII bound as turn keys, and record count is bounded by the
configured task capacity (1000 by default). Replays are checked before capacity,
so a lost-response retry still works at the cap. Archived tasks continue to count
toward capacity. Failed writes roll back both records; database reopen preserves
accepted identities. Credentials and bearer tokens are never included in keys or
digests; the server supplies the stable `local-owner` principal.

Legacy creation requests without a key remain supported and non-idempotent.
Automated clients should generate a UUID before the first creation attempt,
retain it across uncertain retries, and then independently key message admission.

The browser's creation identity covers the full initial prompt and ordered skill
selection, not just its truncated display title. It survives an uncertain creation
response and reload in sessionStorage, is bound to the returned task ID, and is
retained until the first message is accepted on that task. A send on another
task cannot clear it. Changing the initial prompt or skills generates a fresh
creation identity. Logout clears pending identities; after logout or storage
loss, inspect existing tasks before retrying an uncertain request.

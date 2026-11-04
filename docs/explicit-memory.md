# Explicit curated memory management

This is a deliberately narrow implementation of design-plan section 9.4. It is
**authenticated local CRUD management with explicit per-turn selection**. Normal
sessions and tasks do not automatically read or write these records. There is no automatic extraction, inference, background
learning, embedding service, FTS index, or cross-task recall.

## API and integration boundary

The `session.Store` methods in `internal/session/memory_store.go` operate on a
separate pair of SQLite tables in the existing private session database.

- `CreateMemory(scope, fact)` requires nonempty source and explicit user approval
  references, a kind (`preference`, `verified`, `inferred`), confidence in [0,1],
  confidentiality (`public` or `private`), and bounded content. Expiry is optional.
- `GetMemory` and `ListMemory` inspect current records, including expired ones.
- `RetrieveMemory(scope, ids)` retrieves at most 32 explicitly selected IDs in one
  consistent snapshot. It excludes expired records, deduplicates IDs, and fails
  without returning partial results if any ID is unavailable. Empty selection
  returns nothing. No query searches across scopes.
- `EditMemory` requires the current revision and a new approval reference. It
  retains old versions, their source, approval, confidence and timestamps.
  `MemoryHistory` exposes them only for explicit inspection, not retrieval.
- `DeleteMemory` requires the current revision and removes all live versions.
  Concurrent stale edits/deletes fail; callers must reload rather than retry a
  superseded user decision blindly. SQLite busy errors are also possible with
  competing database connections and must not be treated as successful writes.

Choose `MemoryScope{TaskID: id}` for a task record. The session/task must exist.
An empty scope is rejected, not interpreted as global. Choose
`MemoryScope{Global: true}` only for an explicit global management or selection
operation. Combining global and task scope is rejected. The scope is immutable;
a task record cannot silently become a global record through an edit. Deleting a
session cascades to its task-scoped records and their versions. Archiving a task
does not delete its facts. Global records remain user-managed independently.

**None of these structs or metadata fields grants authority.** ApprovalRef is a
bounded audit locator, not proof that the referenced message exists or authorizes
an action. SourceRef likewise does not establish that a fact was verified. These
low-level methods are for trusted control-plane callers, not model tool exposure.
The `/memory` page and `/api/memory` endpoint require the same single-owner
authentication as tasks. Every mutation requires exact same-origin verification,
`X-Qi-Request: 1`, JSON and explicit `confirm: true`. Writes additionally require
`non_sensitive: true`; the UI asks the owner to review the exact content and scope.
The source is a user assertion, not a fetched or independently verified document.
A fresh server-generated `web-owner:` approval reference identifies the accepted
request. Client-supplied approval references are rejected. This reference is
an audit label, not a reusable authorization token or signed proof of consent.
Authentication plus the explicit current request is the authorization boundary.
There is no transcript-derived or model-generated approval path.

The page starts with no scope selected. Choose one existing task (including
archived tasks), or explicitly choose global. Create, inspect current records,
inspect all revisions, edit with the current revision, or confirm deletion of all
revisions. Scope cannot change during edit. A changed form clears its approval
checkbox. Navigation cancels the edit and clears its contents. Responses from an
older scope selection cannot populate the new scope. Browser-session mode has
a local sign-out button. Same-origin tabs broadcast logout without content;
focus, pageshow, visibility and 30-second checks revalidate authentication.
The protected view hides synchronously during revalidation; a five-second deadline
covers both the response and JSON body, aborts timed-out requests and fails closed.
Detected logout/expiry clears record contents, drafts and scope labels. Basic
Auth remains browser-managed and cannot be logged out by this page. Failed writes are never
retried automatically: reload and inspect before making another decision, since
a lost response may follow a committed mutation. Creates are not idempotent;
manually resubmitting the same content can create duplicates.

Classification is metadata, not semantic content validation or access control.
Sensitive/secret classification values and missing owner declarations are refused,
but dishonest or mistaken declarations cannot be detected. No semantic secret
filter is claimed. Do not store credentials or sensitive personal information.

## Explicit per-turn transmission

Open the composer's saved-facts picker, explicitly choose this task or global,
review individual facts and their source/revision, select records and confirm
transmission to the displayed provider, model and exact request endpoint. The
public destination metadata excludes credentials, query strings and fragments.
Unsafe destination metadata disables selection. Nothing is preselected; scope
changes, navigation, sign-out and successful acceptance clear the selection.
Closing the picker before sending clears it. An uncertain submission retains the
same request and idempotency key for a safe retry. Changed selection is a new request.

`POST /api/tasks/{task}/messages` accepts `memory` containing at most 32
`{id, revision, global}` selectors plus `memory_disclosed: true`. Memory requires
an idempotency key and the reviewed `memory_destination` ID from authenticated
`GET /api/memory-destination`. That ID binds provider, model and request endpoint.
A different configuration requires fresh review for new admissions. Already
accepted retries return the original run without dispatching again, even when
the current destination differs. There is no caller-supplied other-task selector. Non-global
IDs must belong to the destination task; global IDs require explicit global
selection. Unknown, deleted, expired, duplicate or stale revisions fail closed.
The combined prompt, skill references and serialized memory must fit the engine's
prompt budget. Empty selection follows the existing provider path unchanged.

The admission transaction validates current scope/revision/expiry, saves the full
versioned records (including source, approval, confidence and timestamps) in
`turn_memory`, and saves the exact destination audit metadata, binding destination, semantic
selection and prompt to the idempotency key.
Retries, including after restart or source edits/deletion/expiry, return the
original admission without re-reading records or making another provider call.
A new submission must pass current validation. Each provider call rechecks snapshot
expiry, including after queued waits or tool execution, and fails the run if expired.

Only the accepted run's request projection includes these snapshots as labeled
untrusted reference data appended to its user message. Stored chat messages remain
unchanged; later turns and question-continuation runs do not inherit selected
snapshots. Tool definitions, authorization and provider transports are unchanged.
The model can quote facts into its ordinary replies, which remain conversation
history. Labels do not guarantee resistance to prompt injection and never confer
authority. There is no extraction, automatic recall or expanded cross-task access.

The task view exposes collapsed audit snapshots by run, not a promise that a
provider received them: cancelled/failed admissions may never send data. Editing
or deleting a curated record does not erase previously accepted run snapshots or
revoke data already sent to the provider. Queued/running submissions retain their
accepted snapshots even after edits/deletion; cancel the task to stop pending
disclosure (already-sent requests cannot be recalled). No CLI selector or model memory tool is
installed.

## Limits and privacy

Limits are 1,024 live records per database, 16 versions per record, 4,096 UTF-8
bytes of content, 512 bytes each for source and approval references, and 32 records
per retrieval. Record IDs must be 32 hexadecimal characters; scopes are also bounded. Version limits reject
further edits instead of silently deleting provenance; users can inspect/delete
records, then explicitly create replacements. There is no automatic pruning.
Limits are not an OS quota or protection against a user directly modifying SQLite.

The existing private database permission checks, WAL durability and foreign-key
constraints apply. Data is plaintext at rest, not encrypted. Confidentiality is
metadata and does not protect against another process under the same OS account,
an administrator, database backups, or a compromised control plane. No real user
facts or credentials are seeded; tests use synthetic preferences only.

Deletion removes the record and all its historical versions from subsequent live
queries. There are no separate search indexes, embeddings or summaries to purge
in this implementation. SQLite free pages/WAL, filesystem snapshots and backups
may retain bytes; forensic erasure is not promised. Existing transcript messages,
exports and already-sent provider inputs are separate data and are not rewritten
by deleting a memory. Future indexing/caching must implement invalidation before
claiming deletion removes future retrieval hits.

## Verification

`go test ./internal/session` and `go test -race ./internal/session` cover explicit
scopes, cross-task/global denial on every operation, no implicit selection,
provenance validation, expiry, versions, stale revisions, reopen durability,
record/retrieval/version limits, cascading deletion, competing edits and
edit-versus-delete across connections, and injected create/edit/delete failures
that must roll back atomically. Tests also cover malformed/oversized IDs,
all-or-nothing mixed retrieval, and ignored UPDATE/DELETE operations that must
fail rather than report a successful mutation. Full repository tests
remain the integration gate. `go test ./internal/webui` covers authenticated
CRUD, explicit confirmation, server-generated approval labels, scope isolation,
revision conflicts and deletion of all live versions. `node --test
internal/webui/memory_test.cjs` tests scope switching, repeated clicks, cancellation,
confirmation invalidation and expired-session clearing. Task and WebUI tests additionally cover per-run selection, cross-task/global
isolation, revision/expiry rejection, size limits, concurrent retry and restart
idempotency, no implicit later-turn carry-over, audit preservation after deletion,
explicit disclosure, stale UI responses and selection reset. Saving a record alone
never sends it to a model. No live provider calls are used in these tests.

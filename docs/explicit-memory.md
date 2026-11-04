# Explicit curated memory primitives

This is a deliberately narrow implementation of design-plan section 9.4. It is
**storage infrastructure, not a finished memory UI or model capability**. Normal
sessions, tasks, transcripts, provider requests, and context building do not read
or write these records. There is no automatic extraction, inference, background
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
A future UI/API integration must authenticate the user, resolve source and approval
evidence, verify approval of the exact fact and scope and its current validity, apply its permission policy,
and reject credentials/sensitive content before calling a write. Approval freshness
must be checked against the actual user evidence, including revocations and older
references: storage only rejects reuse of the immediately current approval
reference during an edit, and does not authenticate references or prevent reuse
of a reference from an older version. A different string is not fresh approval.
Deletion must
also be explicitly user-authorized by that caller. Merely setting confidentiality
to `private` cannot make sensitive content safe. No semantic secret detector is
claimed here; sensitive/secret classification values are refused, but dishonest
or incorrect classifications cannot be detected by storage.

Before including any record in a provider request, a future context builder must
obtain explicit selection for the destination task and identify that provider
transmission. A global storage approval is **not** permission to inject the fact
into every task, channel, project, workspace, provider, or subagent. Even a public
record is not automatically exposed. Present selected facts as untrusted reference
data with provenance, confidence and update time, never as system instructions or
permission grants. Revalidate selection at request construction; do not retain an
unbounded context cache. A previously returned snapshot cannot be revoked in-place.
This initial slice deliberately does not install context integration.

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
remain the integration gate. No HTTP endpoint, CLI, browser UI, model tool or
external data integration has been added by this slice.

# Local database backup and verification

Backups are explicit, local operations. They do not contact a provider, launch
workers/plugins, read environment credentials, or change the daemon configuration.
Create a new private backup directory yourself (for example `mkdir -m 700 backups`),
then use explicit paths:

```
qi backup -source /private/qi/history.db -destination /private/backups/qi-2026-11-04.db
qi backup-verify -source /private/backups/qi-2026-11-04.db -work-dir /private/checks
```

Both parent directories must already exist, be owned by the current account and
have mode 0700 or stricter. The database and its sidecars must be private regular
files owned by the current user, not symlinks or hard links. See
[storage privacy](storage-privacy.md) for ancestor ownership checks and platform
limits. Commands never repair existing permissions. Output is mode 0600. Existing
destinations, including symlinks and orphan SQLite sidecars, are rejected.

## What is captured

`backup` opens the source with SQLite `mode=ro`, not the application's writable
store opener. Missing source files fail without database creation; no schema
migration, checkpoint or application recovery is requested. A pinned read
transaction and SQLite's online backup API capture a single consistent snapshot,
including committed, uncheckpointed WAL writes. Writes committed after the pinned
snapshot are excluded. This is not a plain copy of a live `.db` file. Concurrent
writes may continue; a long backup can hold back WAL reclamation and consume disk.

Read-only here means no logical database writes. SQLite may create or update WAL
shared-memory sidecars for coordination; it does not promise byte-for-byte source
filesystem immutability. Do not use `immutable=1` against a live WAL database.

The snapshot contains database records: tasks, history, events, approval state,
curated memory and versions, turn-memory snapshots, persisted idempotency keys,
and artifact metadata where present. Artifact **bytes**, workspace files, model
credentials, environment settings, external secret/configuration files and plugin
binaries are **not** bundled. Secrets previously written into conversation or
other database fields remain in the snapshot: backup is not a redaction operation.
Backups are unencrypted and must stay private, outside the repository.

## Publication and cancellation

The destination is built in a new 0700 staging directory next to the final path.
The command checks SQLite integrity and foreign keys, reads known table counts,
closes and syncs the snapshot, then atomically publishes it with a no-clobber hard
link. Staging links are removed and directories synced. This requires a local
filesystem supporting hard links and directory sync; unsupported operations fail.
A cancellation or failure before publication removes temporary output; it never
replaces an existing file. Cancellation is checked between bounded backup page
batches and before publication. After publication, a sync or cleanup error is
reported explicitly: the final file may already exist, so inspect the path rather
than retrying with overwrite. A process crash can leave a hidden `.qi-backup-*`
staging directory; normal cancellation cleans it up. Do not blindly delete a
staging directory while another backup may be using it.

## Verification is not a production restore

`backup-verify` reconstructs a database through SQLite's online backup API in a
new disposable directory under `-work-dir`, then checks integrity, foreign keys
and counts of recognized tables on that disposable copy. Its JSON output contains
counts only, not record payloads. Missing optional tables are omitted for older
schemas; core conversation tables are required. No migrations are performed.
The disposable database is removed afterward; cleanup failures are returned.

Both `backup` (before publication) and `backup-verify` validate only their newly
constructed private copies using `mode=rw`, `query_only=ON`, `trusted_schema=OFF`.
The original source database or original backup is always opened `mode=ro`.
No application initialization, migrations or logical writes run on the validation
connection. This distinction matters: the pinned SQLite implementation omits
parsed CHECK expressions for read-only databases, so read-only integrity_check
can miss CHECK violations. The copy's non-read-only B-tree retains those
expressions while query_only prevents logical writes. A failing copy is removed,
not published. Earlier read-only verification did not establish CHECK validity;
recheck older snapshots with this copy-based validation when that assurance is
needed. SQLite integrity/FK checks still do not establish application-version
compatibility or validate every application-level invariant.


The automated restore test additionally opens a restored fixture through the
application store and compares task state/history, approvals, curated memory and
idempotency replay. All fixtures are test-owned temporary databases. This does
not establish recovery of artifact bytes, provider credentials, external side
effects, or deployment configuration.

There is deliberately no auto-overwrite restore command. For a real recovery,
stop the daemon, preserve the original database and its sidecars together, verify
the chosen backup separately, and select a **new** database path for a controlled
restart. Do not overlay a snapshot on a running database or reuse stale WAL/SHM
files. Review interrupted/unknown action state before taking any further action;
a database restore does not undo external actions or authorize replaying them.

# Read-only SQLite schema diagnostics

```
qi schema-inspect -source /private/qi/history.db
```

This standalone command inspects an **existing** private SQLite file. It does
not use the application Store.Open path, initialize qi tables, migrate schema,
change version fields, perform startup recovery, start workers/plugins or read
model credentials. It does not change daemon startup behavior or add a version
compatibility gate. See the [migration plan](schema-migration-plan.md).

The JSON report contains:

- `application_id` and `user_version`, as observed in one SQLite read snapshot.
- `version_state`: `unversioned` when both values are zero, otherwise
  `metadata_present`. Neither value is a compatibility verdict.
- `integrity` and `foreign_keys`: status and diagnostic/violation counts.
- `check_constraints: "not_validated_read_only"`: an explicit coverage limit.
- `objects`: counts of tables, indexes, views and triggers in sqlite_schema,
  including SQLite-owned objects where present.
- `compatibility: "not_assessed"` and an explicit limitation warning.

The report deliberately omits record values, object names, raw DDL, default
expressions, row identifiers and SQLite's raw error messages. Schema SQL and
integrity diagnostics can themselves contain sensitive literals. Aggregate
object counts are not a schema fingerprint or proof of provenance. No stored
view or trigger is executed to produce those counts.

A valid check produces JSON and exits successfully. Failed integrity/FK checks
produce the aggregate JSON report and a nonzero exit. If inspection cannot finish
(for example, a malformed database or cancellation), it returns an error without
a partial JSON report. Integrity problem counts reflect SQLite's returned
messages, which may be capped by SQLite; they are not an exhaustive defect count.
With the pinned modernc.org/sqlite v1.34.5 dependency, SQLite deliberately omits
parsed CHECK expressions for read-only databases. Consequently, read-only
`integrity_check` can return `ok` for a row violating a CHECK constraint. This
was verified against a disposable fixture and the driver's SQLite implementation.
The command does not switch to a writable connection to work around this. Foreign
key checks remain independent; this report is not complete constraint validation.

A healthy database, zero version, or recognized-looking structure does **not**
establish qi compatibility, identify a historical release, authorize migration
or prevent an old binary from writing the database.

## Filesystem and SQLite boundaries

Empty paths, missing files, zero-byte files, directories, symlinks, hard links,
nonregular files and insecure ownership/permissions are rejected. The source
must be a regular file owned by this account with mode 0600 or stricter; its
parent must already be private (0700 or stricter). Existing SQLite/engine
sidecars and ancestor directories are checked by the same
[private storage rules](storage-privacy.md) as backups. Modes are never repaired.
Unsupported privacy-validation platforms fail closed. These checks do not
protect against an administrator or another process controlling the same account.

The connection uses `mode=ro`, `query_only=ON` and `trusted_schema=OFF`. Version
fields, checks and object counts are read within one transaction, including
committed WAL data. It does not use `immutable=1`, which would ignore live WAL
state. Inspection does not request a checkpoint. A long read can delay WAL
reclamation while other connections keep writing; interrupt with Ctrl-C if needed.

Read-only means **no logical database writes**, not unchanged filesystem bytes:
SQLite may create or update WAL shared-memory coordination sidecars. The command
never creates a missing database. A database that requires unsupported recovery
or cannot be read safely fails rather than falling back to writable Open.

Tests use only temporary fixtures: uncheckpointed WAL metadata/schema, private
read-only files, corrupt inputs, constraint failures with redacted output,
unsafe paths/sidecars and cancellation. They are not historical migration
certification or production recovery tests.

# Database versioning and safe migration plan

Status: **design only, not implemented**. Reviewed against the current source on
2026-11-04. This document does not assert that existing startup is an atomic
upgrade, that historical releases have been certified, or that downgrade is
prevented. All proposed verification uses test-owned databases.

## Current boundaries and risks

| Entry point | Current behavior | Atomicity / ownership |
| --- | --- | --- |
| `session.Open` | Reserves/opens the file, sets WAL mode, creates sessions/messages, adds revision/archived columns | Separate statements; runs before engine ownership is acquired |
| `initTasks` | Creates task/run/event tables and indexes | Separate automatically committed statements |
| `initApprovals` | Creates approvals | One statement, not atomic with other initializers |
| `initMemory` | Creates memory tables/index in one multi-statement Exec | No explicit transaction; one Exec is not one transaction |
| `initResultRead` | Creates result journal/watermarks, baselines old terminal runs, installs triggers | Its own transaction only |
| `EnsureQueueSchema` | Rebuilds runs, creates queue/admission structures, reinstalls result triggers | Its own transaction after engine lock; foreign-key PRAGMA changes are outside it |
| `initArtifacts` | Creates artifacts lazily from save, list and get methods | Even application read methods can execute DDL |
| `tasks.New` | Acquires engine lock, initializes queue, interrupts abandoned runs and approvals | Recovery updates are separate from schema migration and from each other |

There is currently no `application_id` / `user_version` compatibility gate.
SQLite's `schema_version` used by backup code is an internal schema cookie, not
an application version. Backups open the source read-only and do not use Open.

Existing sentinels are incomplete: queue checks for `dispatch_records`, result
initialization checks for `task_result_reads`, and session upgrades check column
names. `CREATE IF NOT EXISTS` does not verify a same-named object's definition.
A failed startup can leave earlier automatically committed changes in place.

## Supported historical inputs must be explicit

Do not treat `user_version=0` as an identified release or as permission to repair
an arbitrary SQLite database. One release can produce multiple valid shapes:
Open before queue initialization, initialized queue, and artifacts absent/present.
Interrupted initialization can produce other, unsupported partial shapes.
Identical SQL schema can also have different payload or application semantics.
Schema inspection cannot prove provenance or identify a unique historical binary.

Before promising a migration path:

1. Pin each supported historical source revision and generate fixtures with that
   code. Existing synthetic tests are useful but do not certify real releases.
2. Define a finite set of accepted structural/data contracts. Inspect columns,
   order, types, defaults, nullability, primary keys, composite foreign keys and
   their actions, unique/partial indexes, CHECK constraints and trigger bodies.
3. Use SQLite introspection plus audited SQL definitions where PRAGMAs are
   insufficient. Whitespace stripping or table-name matching is not a reliable
   SQL equivalence check. Account explicitly for SQLite-owned objects.
4. Check integrity, foreign keys and migration-specific data invariants. Unknown
   objects, missing constraints, mixed profiles and version/schema disagreement
   fail closed with a diagnostic. Never silently stamp them as current.

Historical fixtures and the compatibility matrix are **not yet completed**.

## Phased implementation

### Phase 1: read-only inspection and refusal gate

- Add an existing-file inspection path using SQLite `mode=ro`, before file
  reservation, WAL mode changes, initializers or business recovery.
- Read application identity, application schema version and the structural
  profile. Reject foreign application IDs, unsupported nonzero versions and
  inconsistent metadata before opening for writes.
- Keep missing-file creation separate from existing-file inspection. An empty
  existing file is not evidence of a historical qi release.
- Provide diagnostics without messages, secrets or other stored payloads.
- Preserve backup's read-only path; do not implement inspection through Open.

Read-only means no logical database writes. SQLite may create/update WAL shared
memory for coordination; do not promise byte-for-byte filesystem immutability.
Do not use `immutable=1` on a live WAL database.

**Exit criterion:** unsupported inputs cannot reach writable initialization.
If unversioned inputs still use the old Open path, label this only a forward
compatibility guard, not completed safe migration support.

### Phase 2: narrowly scoped version adoption

- Support new versioned databases and explicit adoption of fully validated
  current profiles first. Refuse other legacy/partial shapes pending fixtures.
- Require a maintenance window and a verified consistent backup for adoption.
- Acquire the existing engine ownership lock before migration writes; recheck
  file identity, version and structure after acquiring it.
- Write application/version metadata atomically with any adoption changes.
  Do not run the old initializers first and then label their output safe.

Use `application_id` as a format identifier, not authentication; use
`user_version` as the authoritative schema version. Keep a migration ledger
consistent with it in the same transaction. Binary release version is diagnostic
metadata, not a substitute for schema compatibility. Do not claim forward
compatibility merely because a schema change appears additive.

**Exit criterion:** accepted profiles are enumerated and tested; interrupted
adoption leaves the original contract or the completed new contract, never a
misleading version stamp.

### Phase 3: explicit, tested migrations

Refactor schema helpers to accept one shared connection/transaction executor.
Do not wrap existing methods in a transaction unchanged: with MaxOpenConns=1,
methods calling `s.db.Exec` can wait on their own pinned connection; existing
self-managed transactions cannot simply be nested.

For each supported path:

1. Stop serving work, hold ownership and create/verify the backup separately.
2. Use one dedicated connection. Set required connection PRAGMAs outside the
   transaction where necessary; verify their values.
3. Acquire a write transaction and recheck the expected input version/profile.
4. Apply audited transformations, preserving references, indexes, triggers,
   idempotency records, result watermarks and relevant sequence state. Avoid
   unvalidated `SELECT *` copies between table versions.
5. Check foreign keys, integrity and migration-specific invariants before commit.
6. Commit schema/data changes together with their version and ledger entry.
7. Restore and verify connection settings. On failure, close the connection and
   refuse to expose a usable Store.
8. Run business recovery only after schema success, with separately documented
   idempotent recovery behavior.

`journal_mode` changes and `foreign_keys` toggles cannot simply be moved into an
active transaction. A correctly refactored path may guarantee atomic
**schema/data/version changes**. File creation, backup, WAL mode changes, lock
files and business recovery remain outside that guarantee. Never call the entire
startup or backup-plus-upgrade sequence one atomic operation.

Move lazy artifact DDL into a defined schema contract or explicitly version its
optional profile. Normal reads must not unexpectedly evolve a versioned schema.

## Downgrade and concurrency limits

Old binaries do not read newly introduced version metadata. No application ID,
version number or ledger can retrospectively make those binaries refuse writes.
Some old startup code writes through Open before acquiring the engine lock, so
an advisory lock alone does not exclude every old startup write path.

Operational controls remain necessary: stop the daemon, prevent old instances
from automatically restarting, pin the deployed binary and do not give an old
binary the upgraded database. Locks protect cooperating code, not arbitrary SQL
clients or same-account administrators.

Rollback requires a separately verified pre-upgrade snapshot and a controlled
restart on a separate path. It may lose post-snapshot data. It does not undo
external side effects or authorize replaying them. Automatic reverse migration
and transparent downgrade are not part of this plan.

## Required test matrix

| Area | Required evidence |
| --- | --- |
| Historical inputs | Fixtures generated by pinned supported source revisions, including queue/artifact variants and representative data |
| Early refusal | Future version, foreign ID, false current stamp, malformed/partial schema and unknown objects never reach writable initialization |
| Transaction failure | Failure after each migration step leaves schema, data, version and ledger consistent; retry is safe |
| References and semantics | Rebuilt queue preserves all foreign keys, partial unique indexes, result triggers, approvals, memory and idempotency/read state |
| Concurrency | Active engine, two migrators, changed schema between inspection and lock acquisition, stale version after waiting |
| Interruptions | Cancellation, busy locks, disk-full, process termination before/after commit and successful subsequent reopen |
| Connection hygiene | Foreign-key enforcement is restored; restoration failure cannot return a usable Store |
| Recovery ordering | Failed schema migration does not interrupt runs/approvals; business recovery is tested independently |
| Downgrade | Actual old binaries demonstrate the limitation; no test substitutes a new binary's version check for old-binary protection |
| Privacy and backup | Test-only private paths, WAL-aware snapshots, no payloads in diagnostics, no claim of physical filesystem immutability |

Release a migration path only after its matrix passes. Unsupported paths remain
read-only diagnostics until their input contracts and recovery behavior are
established.

# Local persistence privacy

`session.Open` performs a fail-closed privacy preflight before opening SQLite,
then checks the resulting files again after initialization. It does not repair
permissions on preexisting user files. Stored conversations, tool outputs and
model data remain plaintext.

## Supported checks

On Linux, macOS, DragonFly BSD, FreeBSD, NetBSD and OpenBSD:

- The dedicated database parent directory must belong to the effective user
  and have no group or other permission bits (`0700` or stricter).
- Ancestors must be directories owned by root or the current user and must not
  be writable by other users. Root-owned sticky directories such as `/tmp` are
  allowed. Root-managed ancestor symlinks, including macOS `/tmp` and `/var`,
  are resolved and checked; the private data directory itself cannot be a link.
- Existing database, `-wal`, `-shm`, `-journal` and `.engine.lock` files must be
  regular, owned by the current user, have no group/other permissions (`0600`
  or stricter), and have exactly one hard link. Symlinks and special files are
  rejected. New files are created privately with a no-follow open.
- Engine ownership uses an advisory file lock. The lock file is not unlinked
  on release, avoiding competing lock-file inodes. Unix releases the held lock
  when the process exits. Do not manually remove a live engine's lock file.

These checks assume a trusted OS account and a local filesystem honoring Unix
permission and locking semantics. They do not isolate hostile same-account
processes, administrators, or network filesystem behavior. Backups and exported
artifacts need their own privacy controls.

## Explicit directory API

`EnsurePrivateDirectory(path)` creates only the final directory component with
mode `0700`, or validates an existing directory. Its parent must already exist.
It never chmods, chowns or replaces existing paths.

`ValidatePrivateStorage(path)` performs the preflight without creating a database.
Missing database/sidecar files are allowed. `ErrUnsafeStorage` identifies unsafe
ownership, permissions or file types. Review old data before moving it to a
private directory or changing its permissions manually; qi will not do that
silently. Old private databases still receive normal schema migration.

## Platform boundary

Windows database open fails with `ErrPrivacyUnsupported` before creating or
mutating data. Unix-looking Go permission bits do not prove Windows ACL privacy;
native ACL validation has not been implemented. Other unimplemented platforms
also fail closed. Windows cross-compilation checks code compatibility only, not
runtime support. The older conservative non-Unix engine-lock fallback is not a
bypass for this storage validation.

## Verification

Tests cover public existing databases without mutation, unsafe parent directory
rejection before file creation, all sidecar classes, symbolic/hard links,
nonregular files, foreign ownership, writable ancestors, private SQLite WAL/SHM,
private legacy migrations, process ownership and crash-release behavior.

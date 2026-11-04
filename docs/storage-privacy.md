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

### Requirements before enabling Windows persistence

Windows persistence remains disabled. The existing Go `0700` directory and
`0600` file modes do not create a private Windows DACL. A post-open ACL check is
not sufficient: SQLite may already have written sensitive data to a database or
sidecar with inherited access. Cross-compiling does not establish privacy.

A future implementation needs all of the following, with native Windows tests:

- Query the effective account SID and security descriptors using the official
  `golang.org/x/sys/windows` APIs (`Token.GetTokenUser`, `GetSecurityInfo`,
  descriptor `Owner`/`DACL` and `GetAce`), checking errors and descriptor validity.
  Validate object ownership as well as access grants. Do not assume that an
  account name, Unix UID or mode bits establishes ownership.
- Define an explicit trusted-principal policy for the current account and OS
  administrators. Reject absent/null DACLs (which allow access), foreign owners,
  untrusted allow ACEs, and ACE types or inheritance rules the validator cannot
  safely interpret. An empty DACL denies access and is different from a null
  DACL. Deny ACEs must not be treated as proof that a broad allow ACE is safe.
- Atomically create the dedicated directory and database with a private security
  descriptor, rather than create broadly and then repair permissions. Validate
  inherited permissions for newly created files and subdirectories, including
  creator-owner substitution and inherit-only ACEs. SQLite-created WAL, SHM,
  rollback journals and engine lock files must be private from creation, not
  merely checked after writes. Never rewrite existing user ACLs silently.
- Inspect opened handles to reject reparse points, junctions, nonregular files
  and multiple hard links, including unsafe ancestors. Ancestor policy must
  account for delete-child, create-child, write-DACL and owner-changing rights,
  not just file read/write permissions. Reject unsupported filesystems, network
  paths or security-query failures rather than assuming local NTFS semantics.
- Verify Windows SQLite path/URI handling, file sharing, engine locking and
  crash-release behavior. A new ACL validator alone does not establish these.

The acceptance suite must exercise real Windows ACLs under distinct user
accounts, permissive inherited ACLs, protected private ACLs, null and empty
DACLs, inheritance-only grants, broad groups, foreign owners, links/reparse
points, sidecar creation and recreation, process races, and crash recovery.
Portable unit tests of a future ACL policy should additionally cover malformed
or unsupported ACEs. Until that implementation and runtime evidence exist, the
unsupported-platform regression tests require rejection without creating a
new database/directory or changing existing database and sidecar contents.

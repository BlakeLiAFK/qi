# Isolated worker policy prototype

Status: library-only, **not a runnable isolated backend**. `internal/isolation`
implements a constrained protocol, trusted policy compiler, and lifecycle
supervisor with fake-runtime tests. It neither launches containers nor exposes a
runner endpoint. Public-preview worker/MCP rejection remains unchanged. Existing
`internal/workers` subprocess execution remains a separate, unsafe development
facility, not this backend. No host settings, accounts or packages were changed.

## Implemented boundary

- A request contains only `task_id`, `agent` (`codex` or `claude`) and `prompt`.
  Strict JSON rejects extra/duplicate keys, non-string values, trailing content,
  invalid UTF-8, oversized input and path-like task IDs. Prompts remain opaque
  data, never shell input or host command-line arguments.
- Launcher-owned config chooses preinstalled image digests, distinct non-root
  daemon/runner UIDs, concurrency, and mandatory CPU/memory/PID/wall/disk/scratch/
  output limits. No request-defined environment, credentials, executable, host
  path, mounts, capabilities or arbitrary runtime options exist.
- Every launch has a random allocation ID. Runtime-owned task-only storage must
  be created fresh; no existing host workspace is mounted. The profile denies
  all network. It does not inherit the daemon environment or descriptors.
- Supervisor provisions a stopped allocation, asks the trusted runtime to inspect
  effective controls and actual image digest, and refuses to run if any evidence
  is missing or mismatched.
  Evidence is not a worker event or permission grant. It cannot establish truth
  against a dishonest or incorrectly implemented runtime adapter.
- Output is bounded jointly across streams and treated as untrusted text.
  Overflow cancels the job. Cancellation/timeout require complete workload
  termination by the runtime, not just killing a CLI parent.
- Cleanup is attempted after partial provisioning and all later failures with a
  fresh 10-second deadline. Cleanup failures are surfaced and quarantine this
  supervisor against new launches. Operator reconciliation is required before
  creating a replacement. Quarantine is rechecked after inspection immediately before launch admission.
  Jobs that already passed that admission point retain their own cancellation
  and cleanup lifecycle; quarantine does not cancel already admitted jobs.
  Restart reconciliation is not implemented.
- Bounded admission rejects excess concurrent launches. All runtime calls must
  honor deadlines. Go contexts cannot force an uncooperative adapter to return.

The runtime interface is deliberately trusted and in-process for now. Do not
implement it by running a container engine under the daemon UID. A future
separate runner service must authenticate the daemon, authorize task ownership,
validate protocol versions, bound connections, and use a fixed trusted adapter.
No untrusted code may run in Provision or Inspect, including image hooks.
No runtime implementation or fallback is supplied, and constructing a supervisor
without a runtime fails closed. This library is not a user-selectable enable flag.

## Linux prerequisites still required, not discovered or verified

1. Administrator-approved distinct daemon and runner accounts. Runner must have
   no sudo, daemon groups/ACL access, daemon secrets, DB, or provider credentials.
   Protect daemon directories (0700), DB/WAL/backups/secrets (0600), and sockets.
2. Reviewed pinned rootless runtime and worker images, user namespaces, working
   subordinate UID/GID mappings and any required newuidmap/newgidmap helpers.
   Use non-root container identities and no host UID/namespace shortcuts.
3. Working private mount/PID/IPC/network namespaces; read-only root, no host bind
   mounts/sockets/devices, dropped capabilities, no-new-privileges, effective
   seccomp and active host AppArmor/SELinux protections.
4. cgroup v2 with verified delegated memory, pids and CPU controllers. Inspect
   effective limits, not launch flags. Independently enforce workspace disk quotas
   and bounded home/scratch volumes. Aggregate host capacity needs provisioning
   beyond this process's bounded concurrency.
5. A trusted separate-identity runner transport, atomic fresh-volume lifecycle,
   symlink-safe import/export, bounded logs, complete-container kill/removal,
   task-capability revocation and orphan recovery across crash/reboot.
6. Model gateway and approval broker retaining credentials outside the worker.
   Current offline profile has no gateway, no credentials, and no general egress.
   Real Codex/Claude model tasks therefore are **not supported yet**. Supporting
   either requires pinned CLI compatibility tests and a new reviewed transport/
   network policy; proxy variables alone are not enforcement.
7. Adversarial integration tests on the chosen Linux distribution/runtime using
   synthetic daemon/DB canaries: filesystem/proc/FD/socket isolation, cross-task
   attacks, egress/IPv6/metadata, effective resource exhaustion, cancellation,
   and restart cleanup. For hostile multi-tenancy, evaluate gVisor or worker VMs.

## Verification

`go test -race ./internal/isolation` and `go vet ./internal/isolation` exercise
only the library and fake runtime. Tests cover hostile path/env/runtime fields,
image/resource policy, each missing control, evidence mismatch, lifecycle errors,
timeouts, concurrent output, admission and cleanup quarantine. A decoder fuzz
entrypoint is provided. Passing these tests is **not proof of actual isolation**.

See [the isolation design](worker-isolation-design.md) for the threat model and
release-blocking end-to-end tests. Public execution must stay disabled until the
production adapter, authorization transport, gateway and actual containment
checks are implemented and verified.

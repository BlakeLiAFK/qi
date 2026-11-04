# Worker isolation design

**Status: proposed and unimplemented.** This document is a design recommendation, not a claim that qi currently enforces these controls. No deployment or host configuration changes are part of this document.

## Decision

Separate the trusted control plane from untrusted execution. Use a separate runner account and one rootless container per task. Same-UID subprocess execution is an explicitly unsafe development mode and must not be available as a fallback in public-preview mode.

For hostile public multi-tenant workloads, evaluate gVisor or separate worker VMs before release. Ordinary containers share the host kernel; they do not eliminate kernel-escape risk.

## Assumptions and threat model

- Linux deployment with an administrator available to approve any prerequisite setup. No existing runtime, kernel feature, UID allocation or administrator permission is assumed.
- Codex, Claude, MCP servers, dependency scripts, repository instructions and generated commands may execute arbitrary malicious code.
- The daemon and a small trusted launcher/broker remain trusted. The worker does not.
- The daemon database, long-lived credentials, approval authority, other tasks and private-network services must remain outside the worker's authority.
- A task may freely modify its explicitly granted workspace. Such access is not equivalent to approval for external actions.
- Availability limits must be enforced outside the worker, including disk and output limits.

## Proposed components

### Control plane

A dedicated daemon UID owns the database and long-lived credentials. Its private directory is mode 0700 and secret files are mode 0600, with no worker-readable group or ACL grants. Database sidecars, backups and logs receive equivalent protection.

The daemon never executes model-generated commands or untrusted MCP packages. It exposes no database, administration or container-runtime socket to workers. Worker output is untrusted input, with bounded frames, output quotas, schema validation and safe rendering.

### Trusted runner

A separate runner UID has no sudo, daemon-group membership or access to daemon storage. A small supervisor accepts a constrained job description over a dedicated authenticated channel. It selects the image digest, mounts, environment and resource limits from trusted configuration, rather than accepting arbitrary runtime options or host paths.

The runner owns a rootless container runtime. Do not run that runtime under the daemon UID. Never expose its socket inside a task. This account is a second boundary if a task escapes its container; it must not hold control-plane credentials.

One runner account can manage multiple task containers, but this does not provide a distinct host UID boundary between tasks after a container escape. Stronger multi-tenant separation requires distinct runner identities or worker VMs, alongside the container boundary.

### Per-task sandbox

- Pin a reviewed execution image by digest; do not pull arbitrary task-supplied images.
- Use a read-only root filesystem, one task-only writable workspace, and bounded ephemeral home/scratch storage.
- Use private mount, PID, IPC and network namespaces; run as a non-root container user.
- Drop all capabilities, enable no-new-privileges and retain runtime seccomp and host AppArmor/SELinux protections.
- Prohibit privileged mode, host PID/network modes, device access, runtime sockets, host home directories, host `/proc`, SSH agents and shared credential caches.
- Set and verify memory, process, CPU, wall-clock, disk and output limits. A memory limit does not bound workspace disk consumption.
- Terminate the complete task process/container group on cancellation and timeout, and revoke task capabilities.

These are supported categories of controls in [Podman's run interface](https://docs.podman.io/en/stable/markdown/podman-run.1.html). Exact flags, UID mapping and compatibility must be tested against the selected runtime and distribution.

## Approval boundary

The daemon is the sole approval authority. CLI flags, worker events and model text cannot create grants.

Store each external-action approval against the task, action type, destination, canonical arguments or content hash, expiry and a single-use nonce. A trusted broker verifies that grant at execution time. Reject altered payloads, stale grants, replays and grants belonging to another task. Keep approvals and audit records outside worker storage.

An unrestricted shell inside a writable sandbox can run commands without asking a broker. Therefore the product must describe workspace execution as a granted sandbox capability; it must not claim that every shell command requires reliably enforced approval. Any operation outside that capability must be technically inaccessible except through an enforcing broker.

Run untrusted MCP servers inside the task sandbox. Credential-bearing integrations belong in separate trusted services exposing narrowly defined operations. Giving the worker a raw privileged MCP endpoint defeats this boundary.

## Credentials and network

Prefer a model gateway that retains provider credentials and accepts task-scoped, quota-limited inference requests. Never inherit the daemon environment wholesale. Credentials deliberately given to an untrusted process must be considered readable by that process.

Default to no general worker network access. Select a gateway transport that the pinned CLI supports, and integration-test it before claiming support. If network tools are needed, enforce access through controlled egress. Proxy environment variables alone are not an enforcement mechanism.

Block direct routes to host services, private and link-local networks and cloud metadata. Cover IPv6, redirects and DNS rebinding. Only the intended gateway or broker endpoints should remain reachable. External write-capable integrations stay behind approval brokers even when model inference is permitted.

## systemd alternative

A system service under a separate UID is a useful intermediate boundary, but needs explicit filesystem and network restrictions. Candidate controls include `NoNewPrivileges`, empty capability bounds, `PrivateTmp`, `PrivateDevices`, `ProtectHome`, `ProtectProc`, and a read-only filesystem with a narrowly writable workspace. Verify each against the installed systemd/kernel and workload.

`ProtectSystem=strict` restricts writes, not reads. Read-only paths do not prevent connecting to filesystem Unix sockets. Namespace controls depend on platform support and differ between system and user services. A hardening checklist or security score is not proof of containment. See [systemd.exec](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec).

## Deployment stages

1. Perform read-only discovery of runtime versions, user namespaces, service-account options, subordinate UID/GID mappings, cgroup v2/controllers, storage quotas and LSM support.
2. Obtain administrator approval for account creation, package installation, subordinate mappings, lingering, controller delegation and network changes where required. This document does not authorize those actions.
3. Implement the constrained launcher, task workspace lifecycle, approval broker and explicit unsafe-development-mode guard.
4. Validate actual isolation and effective resource limits with adversarial fixtures before enabling preview execution.
5. If prerequisites are unavailable, leave public execution disabled or use a separate worker VM without control-plane secrets. Never silently downgrade to subprocess execution.

Rootless Docker still requires `newuidmap`/`newgidmap` and subordinate UID/GID ranges; installation is not universally administrator-free. See [rootless prerequisites](https://docs.docker.com/engine/security/rootless/).

Rootless Docker resource flags can be ignored without cgroup v2/systemd, and controllers may require delegation. Verify effective cgroup values rather than launch arguments. Docker documents rootless management through a user service, not a system service merely containing `User=`. See [rootless deployment and limits](https://docs.docker.com/engine/security/rootless/tips/).

## Release-blocking tests

Use synthetic canary secrets, never production secrets.

- Reading daemon/database canaries fails through direct paths, symlinks, environment, inherited descriptors and `/proc`.
- Signaling or tracing daemon processes fails; database, administration and runtime sockets are unreachable.
- Another task's files, processes, credentials and broker grants are inaccessible.
- Path traversal, symlink swaps and archive extraction cannot escape task storage. Artifact collection rejects escaping links and special files and remains safe under concurrent mutation.
- Direct internet, host-loopback, private-network and metadata access fails; intended broker traffic succeeds. Include IPv6 and DNS/redirect variants.
- Fake approval events, modified payloads, expired grants, replay and cross-task grants fail.
- Process, memory, disk and output exhaustion are bounded without exhausting the control plane.
- Cancellation and timeout kill every descendant and revoke task tokens.
- Restart and reboot leave no orphan worker, exposed secret mount or reusable approval grant.
- Representative tasks complete with each supported pinned Codex, Claude and MCP configuration under the actual profile.
- Startup refuses public execution when required isolation or resource controls are missing or ineffective.

## Residual risk

Containment does not make model output trustworthy or guarantee safety of a user-approved external action. Containers still share a kernel. The trusted launcher, broker and runtime remain attack surfaces and require patching, minimal interfaces and security review.

[gVisor's security model](https://gvisor.dev/docs/architecture_guide/security/) explains its additional host-kernel isolation layer and the fact that allowed files and network access remain available to sandboxed applications. Adding it is a separate deployment and compatibility decision, not an implemented guarantee here.

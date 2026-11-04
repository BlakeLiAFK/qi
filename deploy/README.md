# Restricted development-preview templates

Local preparation only. Nothing here has been installed, started, exposed, or
validated on a VPS. No actual destination, account, credential or hostname is
included. These files are not a production-security certification. Do not run
installation or exposure steps without the necessary authorization.

## Security boundaries

- The daemon binds only to `127.0.0.1:8686`. Never expose that port or bind it to
  a public interface. Only a trusted same-host TLS proxy may reach it.
- `-public-preview` is mandatory. Application startup rejects workers and MCP
  in this mode. Do not remove that flag, add workers/MCP, or pass arbitrary flags.
  No artifact root, skill root or workspace is enabled in this template.
- The systemd unit uses a non-root dynamic service identity, a private 0700 state
  directory, 0077 umask, read-only system paths, no capabilities, no home access,
  and resource limits. A real `data` subdirectory avoids using systemd's dynamic-user
  state symlink as the final database directory, which qi correctly rejects.
  It is defense in depth, not worker/container isolation.
  Outbound network access remains possible for DNS and the model provider.
- Keep the environment file root-owned 0600, parent directory 0700. The system
  service manager reads it before dropping privileges. Secrets are still in the
  daemon environment and accessible to privileged administrators. Do not commit
  them, paste them into chat, or put them in command arguments or debug logs.
- Keep binaries and release directories owned by an administrator and not
  writable by the service identity. SQLite state and backups contain private
  conversation data. Never serve them from Caddy or a static web root.
- This is a single shared-login development preview, not multi-user isolation.
  Limit access to intended testers, use non-sensitive test data, restrict model
  spending, and rotate the unique login token after disclosure. Restart revokes
  browser sessions. Basic authentication and `qi attach` are disabled.

## Prerequisites and TLS choices

Choose the destination, exact HTTPS origin, operating system, trusted Caddy
package, model and credential delivery separately. Confirm DNS and any IPv6
AAAA record resolve to the intended host. Review existing services before any
changes; never replace an unrelated Caddyfile or firewall policy.

Normal Caddy automatic certificate issuance needs public ACME HTTP-01 on TCP 80
or TLS-ALPN-01 on TCP 443, with correct forwarding/firewall rules and no conflicting
listener. An application served on another port still normally needs 80/443 for
these challenges. If those ports cannot be used, choose DNS-01 with an appropriate
reviewed Caddy DNS module and narrowly scoped DNS credentials, or provision a
trusted certificate separately. Neither alternative is configured here. A local
self-signed/internal certificate is not a publicly trusted certificate; never
bypass browser certificate warnings. ACME issuance and renewal must both work.
Certificate transparency can reveal the chosen hostname.

Stock Caddy has no built-in general request-rate limiter. Before activating the
proxy, arrange a separately reviewed upstream rate limiter/access gateway or a
reviewed plugin. Protect login POSTs and expensive authenticated API requests;
application concurrency limits do not prevent brute force or denial of service.
Block direct-origin bypass when using an edge gateway, and trust forwarded client
identity only from that gateway's verified addresses. Do not blindly trust
X-Forwarded-For. Account for SSE/long-lived connections and avoid buffering them.
The sample deliberately returns 503 and comments out the entire proxy block until
this independent protection is ready. No active proxy exists in the template,
regardless of Caddy directive ordering.

## Manual installation checklist (not an executed deployment)

1. Run local checks below. Build a reviewed, versioned Linux binary for the target
   architecture; record its checksum and source revision. Never build as root on
   the destination or download an unverified executable.
2. On an authorized target, have the operator create administrator-owned release
   directories under `/opt/qi-preview/releases/` and a `current` link to the chosen
   release. The unit expects `/opt/qi-preview/current/qi`. Do not place secrets in
   release directories. Preserve the previous binary and configuration for rollback.
3. Privately provision `/etc/qi-preview/preview.env` using the example's keys and
   permissions above. The example deliberately leaves model, token and origin
   empty, so copying it unchanged cannot start a working preview. Supply a random
   token of at least 32 characters, not a memorable password. The origin must
   exactly match the final HTTPS browser origin. Keep the model API key separate
   from the browser login token. Do not add worker credentials.
4. Review/install the unit as `/etc/systemd/system/qi-preview.service`. Use
   `systemd-analyze verify` on that target and resolve all warnings. DynamicUser,
   namespace, syscall and filesystem restrictions depend on the host's systemd
   and kernel. Test rather than silently weakening hardening. The service manager
   creates state storage; do not recursively loosen `/var/lib` permissions.
5. Only with deployment authorization, reload systemd and start the service.
   Check its identity, state permissions, loopback-only socket and startup logs
   without printing environment values. Check local unauthenticated requests
   reveal no task content. Inspect resource usage and syscall-denial logs.
6. Review a dedicated Caddy site using the sample; replace the placeholder with
   the verified host. Preserve existing sites. Validate/adapt the final config
   with the installed Caddy version. Keep the 503 guard in place while arranging
   access control, rate limiting, certificate issuance and renewal. Changes to
   listeners, DNS, firewall, access permissions and credentials require separate
   applicable authorization; this package performs none of them.
7. Only during authorized activation, remove the 503 guard, uncomment the entire proxy block, and reload the
   validated Caddy config. Verify trusted HTTPS, exact-origin enforcement,
   secure session cookies, failed login behavior, logout, invalid-token rejection,
   denied cross-origin writes and SSE reconnect. Verify external requests cannot
   reach port 8686 and unintended hosts cannot reach task content. Do not claim
   success until external checks pass. A real model call may incur charges and
   transmit test prompts; use only approved data/provider and budget.

## Rollback / emergency closure

Comment out the proxy block and restore the Caddy 503 guard (or remove only this
site's routing), then validate and
reload, preserving unrelated sites. Stop this service to revoke sessions and end
model work. Do not delete data or uninstall unrelated software. Take a private,
consistent database backup only after stopping the service, including any SQLite
WAL/SHM files present; restrict backup access and never put it in a release/web
folder. Keep the backup until a restore is verified. Switch `current` to the last
reviewed binary and restore the matching configuration. Do not run an older binary
against an incompatible schema: use the matching backed-up state if needed,
under explicit authorization because restoring state discards newer changes.
Repeat local and external acceptance checks before reopening access. If credential
exposure is suspected, rotate affected credentials through their approved flow.

## Local checks and limitations

From the repository root:

```sh
python3 deploy/check_templates.py
go test ./cmd/qi ./internal/webui
go build -o /tmp/qi-preview-check ./cmd/qi
/tmp/qi-preview-check serve -h
```

These do not install a service or contact a model. Dependency downloads may be
needed for Go tests/builds. The static checker checks template invariants only;
it is not a Caddy parser, systemd verifier, firewall test, penetration test or
TLS issuance check. Do not run a placeholder config against a real Caddy service.
On a prepared authorized target, use `systemd-analyze verify` and `caddy validate`
against the final paths/configuration. Some Caddy validation modes can provision
modules or touch certificate storage: use an isolated validation context first.

### Preparation verification (2026-11-04)

- Python template invariant checks: passed.
- `go test ./cmd/qi ./internal/webui`: passed with the workspace Go toolchain.
- Binary build and `serve -h`: passed with `-buildvcs=false`; shared-workspace VCS
  stamping was unavailable. This flag does not substitute for release provenance.
- systemd verification: **blocked**, this environment cannot write `/run/systemd`.
  No unit was installed or started. The real `data` subdirectory addresses qi's
  final-directory symlink rejection, but dynamic-user ownership, mounts and
  syscall restrictions still need an actual target integration test.
- Caddy parsing/runtime/TLS verification: **not run**, Caddy is not installed.
- External reachability, firewall, gateway/rate limiting and provider calls:
  **not tested**; no VPS access or public exposure was performed.

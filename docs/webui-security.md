# Web UI security and deployment

The embedded Web UI is a **single-user interface**, not a multi-user service or
execution sandbox. Anyone possessing its password can read every task, submit
messages, cancel runs and act on the available cards. Keep the model API key
separate from the Web UI password. Do not expose real credentials in demos.

## Optional public preview browser sessions

`qi serve -public-preview -origin https://qi.example` enables a distinct browser-session
mode. It is opt-in; the default remains the local Basic mode described below. Set
`QI_WEB_TOKEN` in the process environment to a freshly generated high-entropy
credential (at least 32 characters). This is a reusable login bearer secret, not a one-time bootstrap token or a
human password. Never use a guessable password or commit an environment file.
Only a SHA-256 digest is retained by the web handler. The environment remains
part of the trusted host and must not be exposed to untrusted processes.

- `/login`, `/login.js`, and POST `/api/login` are the only public login routes.
  No task, artifact, event, app asset, or diagnostic API becomes anonymous.
- POST `/api/login` accepts JSON `{token: ...}` and requires exact Origin plus
  `X-Qi-Request: 1`. Basic authentication is disabled, including for `qi attach`.
- Successful login sets a random 256-bit `__Host-qi-session` cookie with Secure,
  HttpOnly, SameSite=Strict, Path=/ and no Domain. Only its digest is stored
  server-side. The browser never stores the login credential in localStorage.
- Sessions expire absolutely after eight hours, with at most 64 live records.
  Login rotates an existing session. POST `/api/logout` with JSON `{all:false}`
  revokes the current session; `{all:true}` revokes all sessions. Both require
  the authenticated session, exact Origin and the custom request header. Logout
  cancels existing event-stream contexts; reconnects must authenticate again.
  Expiry also cancels streams. Restart revokes every session. Logout-all does
  not revoke the reusable login credential: someone holding it can sign in again.
  To revoke that credential, replace QI_WEB_TOKEN and restart qi. Revocation does
  not undo already accepted actions or cancel background task execution.
- Authentication failures use the bounded TCP-peer limiter described below.
  Only login guesses consume its budget in preview mode; missing/expired cookies
  do not. Existing valid sessions remain usable during a proxy-shared login
  lockout. Caddy is still a shared peer: deploy independent edge protections
  if per-client throttling is needed. No client-IP forwarding header is trusted.
- HTTPS origin is mandatory. Every request must arrive over TLS or from a
  literal loopback TCP peer with exactly `X-Forwarded-Proto: https`. This selects
  a trusted local proxy boundary; it does not make arbitrary proxies trusted.
  Caddy must preserve Host, overwrite X-Forwarded-Proto, terminate TLS, redirect
  HTTP before the app, and keep the qi upstream inaccessible off-host. Local
  processes can impersonate the proxy, so use an isolated OS account/container.
  Never log request bodies, cookies, or Authorization headers. Standard Caddy
  has no built-in per-client login rate limiter; do not assume one is installed.

Example proxy shape (placeholder hostname; configuration only, not deployed):

```caddyfile
qi.example {
    reverse_proxy 127.0.0.1:8686 {
        header_up Host qi.example
        header_up X-Forwarded-Proto https
    }
}
```

Before exposure, independently verify DNS/TLS, HTTPS redirect, upstream firewall,
external host/origin rejection, cookie behavior and sign-out/stream termination.
Keep workers and credentialed MCP off for an unreviewed preview. This remains a
single-owner application, not an account system or a multi-user authorization
boundary. No deployment or public availability is implied by these local tests.

## Default Basic authentication and transport

- Use a cryptographically random password with at least 32 characters. The server
  requires HTTP Basic username `qi` and this password for **every** route, including
  HTML, JavaScript, CSS, unknown routes and API reads. The password is not embedded
  in the frontend, stored in browser localStorage, or returned by an API.
- Basic authentication is safe only over a trusted transport. Public origins must
  use HTTPS. Cleartext HTTP is accepted only for literal loopback origins such as
  `http://127.0.0.1:8080`, not arbitrary hostnames.
- Terminate TLS at a trusted reverse proxy. Bind qi's upstream listener to
  loopback, preserve the public `Host` header, and block remote upstream access.
  Configure the exact external origin, without a trailing slash. Reject public
  HTTP at the proxy; do not encourage entering credentials before HTTPS.
- Forwarded headers are not trusted. The proxy and its local network connection
  are part of the trusted deployment boundary. Do not log Authorization headers.
- Login attempts are limited per actual TCP peer IP after 20 failures in one
  minute. The budget is checked before inspecting credentials; even a correct
  password is blocked until that peer's window expires. Different source ports
  do not create new budgets. Forwarded/X-Forwarded-For headers are ignored.
- Peer records are capped at 4,096 and expire after one minute. At capacity, new
  peers are temporarily rejected rather than growing memory without bound.
  An attacker on a different peer cannot consume an existing peer's budget.
- **Reverse-proxy requirement:** enforce per-client login/connection rate limits
  at the trusted edge, stripping caller-supplied forwarding headers. qi sees the
  proxy as its TCP peer, so its fallback bucket is shared by proxied clients.
  A malicious client behind the same proxy or NAT can temporarily block new
  requests from that shared peer. The application does not claim to eliminate
  shared-IP denial of service; use edge authentication/client-specific limits
  and high-entropy passwords before public exposure. There is no trusted-proxy
  client-IP setting in this version.
- Browsers cache Basic credentials. There is no reliable application logout;
  close the private browser session or rotate the password and restart qi.
  Prefer a dedicated private window on a trusted device. Do not embed passwords
  in URLs, screenshots, shell history, issue reports or shared preview links.

## Request and rendering boundaries

All requests check the exact configured Host and reject mismatched Origin and
cross-site Fetch Metadata. Mutations require the exact Origin, a custom request
header and JSON content type. There is no CORS allowlist and no mutation by GET.
Bodies are limited to 32 KiB, prompts to 16,000 bytes and titles to 160 bytes.
Unknown JSON fields and trailing JSON values are rejected. Use server timeouts,
header limits and graceful shutdown in the executable hosting this handler.

Assets are embedded; no third-party JavaScript is loaded. A restrictive CSP,
frame denial, nosniff, no-referrer and no-store protect all responses. Message
and card content uses DOM textContent, never HTML interpolation or model-supplied
executable code. HTTPS origins also receive HSTS.

Card buttons are only a view: the task backend must check the authoritative card,
revision and action state atomically. Client-side disabled buttons are not an
approval boundary. A confirmation card cannot grant general tool privileges.

## Current scope

The UI refreshes authoritative task snapshots on SSE progress notifications and
periodic polling. `qi serve -model-stream` optionally enables bounded provisional
model text; the default remains buffered. Preview snapshots are task/run scoped,
limited to 64 KiB, rendered as text, and cleared on a final message, error or
terminal run. Reconnection replaces snapshots rather than appending repeated
deltas. Partial tool arguments are never shown or dispatched. Persisted delta
events are progress records, not conversation history; they are never replayed
as execution after restart. Switching task tabs
does not cancel other runs. Concurrent task execution and durable history are
owned by the task engine. The interface does not itself constrain a model,
plugin, worker process, filesystem access or network access. The optional coding
adapter uses the approval boundary described below; OS isolation remains the
operator's responsibility. MCP tools are disabled by default and require a separate
explicit static capability configuration (see below).

Before any public preview, test authentication, origin/host rejection, replayed
cards, cancellation, concurrent tasks and restart recovery; verify TLS and proxy
configuration from outside the host. An authenticated local fixture test does
not establish that a public deployment is secure.

## Optional coding execution

Coding tools are disabled by default. `qi serve -enable-workers -work-dir /repo`
adds a run-scoped `coding_run` tool. The model chooses only the supported CLI and
its prompt, never a directory, executable, approval ID, or an approval decision.
Each invocation waits for an authenticated, exact-action approval with a default
five-minute expiry. The approval includes the full prompt, executable, workspace,
runtime limit and access warning. Informational cards do not authorize execution.
The durable approval is atomically consumed before dispatch and cannot be reused.

The operator selects one existing canonical workspace. qi detects replacement
of that directory before dispatch, but this is **not filesystem confinement** or
a security sandbox. CLIs retain their own permissions and network access. Workers receive only an
allowlisted runtime environment, a temporary HOME and CLI configuration directories,
and explicitly configured CODEX_API_KEY/ANTHROPIC_API_KEY worker credentials.
QI_WEB_TOKEN, OPENAI_API_KEY and other parent secrets are not inherited. This
reduces accidental exposure but does not stop same-user code from reading host
files, parent process information or the qi database. Run qi in an isolated account/container
with only intended secrets and files when processing untrusted content. Approved jobs sharing a canonical workspace are serialized within one qi process.
The workspace slot is acquired after approval; queued approval expiry and run
cancellation remain enforced. Separate daemon processes are not coordinated,
and qi does not provide worktrees or OS isolation. No model-callable arbitrary shell command or path override is exposed.

Codex runs with explicit `--ask-for-approval never` and `--sandbox workspace-write`:
the CLI cannot request escalated permissions interactively. Claude runs with
`--bare --permission-mode default`; qi does not pass permission-bypass flags.
CLI-enforced permissions can still cause a request to fail. The adapter has fake
CLI lifecycle tests; installed CLI version compatibility and provider execution
must be validated separately before using real credentials.

The run owns the subprocess lifetime. On supported Unix hosts qi kills the
worker process group after every parent exit, including normal exits, and on
cancellation. Processes that deliberately escape the group are not confined. Output is capped at 64 KiB, retaining a bounded prefix and the latest tail.
Truncated output includes an explicit gap marker and a truncation flag; runtime defaults to 15 minutes, capped at one
hour for daemon coding tasks. Completion means the CLI exited successfully, not
that its changes are correct or have passed tests. A process that fails or is
cancelled may have changed files; its approval becomes `unknown`, and the model
turn stops instead of retrying. Restart recovery marks in-flight outcomes unknown
and never launches the CLI again. It does not reconcile files or reattach orphaned
processes after an abrupt host failure. Inspect the workspace and running processes
before authorizing another attempt. Cancelling or stopping qi is not rollback.

## Optional MCP execution

`qi serve -mcp-tools /path/to/tools.json` opts into static operator-selected MCP
capabilities. No process is started for discovery. Every call requires a fresh
exact-action durable approval covering server executable, startup arguments,
directory, tool name, tool arguments, runtime and an access warning. Startup itself
can have side effects. A fresh process and disposable home are created for each
approved call, never shared across tasks; the process group is stopped before
releasing the workspace. MCP and coding jobs share the same per-process canonical
workspace gate, acquired after approval and subject to expiry and cancellation.

This initial slice supports Unix process groups only. It supplies no credentials
or environment overrides, filters ambient application secrets, discards server
stderr, and removes temporary home/config directories after exit. Keep secrets out
of command arguments and tool configuration: approval payloads and tool results
are persisted, and tool results are supplied to the task's model. Trusted servers
still have same-user host filesystem and network access. Processes can deliberately
escape process groups; isolation requires a dedicated OS account/container.
Executable paths are resolved at startup but executable contents are not pinned:
a same-user change after approval is outside this boundary. Directory identity
checks likewise do not eliminate filesystem races or constrain descendants.

After startup, transport failures, timeouts, cancellation and MCP error results
are conservatively recorded as unknown outcomes; no automatic replay occurs.
Restart recovery never replays calls or resumes server processes. Inspect external
state before approving a retry. Do not use credentialed or tool-enabled public
previews without separately reviewing OS isolation, authorization and deployment.

## Execution approvals

Informational task cards and execution approvals use separate routes. The Web UI
polls `GET /api/tasks/{id}/approvals` and displays each approval's complete action
payload, target, expiry, revision and digest. Explicit Allow/Deny submits only
`{id, revision, digest, allow}` to `POST /api/tasks/{id}/approvals`. Missing decisions
or digests are rejected. The engine verifies current state and action binding;
replays or expired/stale decisions return conflict. The coding and MCP execution adapters,
not the UI card renderer, claim an approved action before dispatch.

Tests: `go test -race ./internal/webui` and
`node --test internal/webui/app_test.cjs`. Node tests verify DOM behavior with a
minimal test document; they are not browser-layout or end-to-end execution tests.

## Explicit skills

`GET /api/skills` returns only configured metadata and requires authentication.
Sending a message may include `skills: [name, ...]`; absent or empty selection
uses the ordinary turn. The engine resolves exact configured names and validates
bounds before starting a run. The browser starts with every checkbox unchecked,
clears selection after a successful send or task navigation, and does not load
skill bodies itself. Labels identify these as untrusted local instructions, not
permission to execute tools. Selected snapshots are sent to the model and saved
in that task's history; clearing the next-turn selection does not erase earlier
conversation content or create a new security boundary.

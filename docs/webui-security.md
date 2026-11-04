# Web UI security and deployment

The embedded Web UI is a **single-user interface**, not a multi-user service or
execution sandbox. Anyone possessing its password can read every task, submit
messages, cancel runs and act on the available cards. Keep the model API key
separate from the Web UI password. Do not expose real credentials in demos.

## Authentication and transport

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

The UI polls task state; token streaming is not implemented. Switching task tabs
does not cancel other runs. Concurrent task execution and durable history are
owned by the task engine. The interface does not itself constrain a model,
plugin, worker process, filesystem access or network access. Tool execution needs
its own reviewed approval and isolation implementation.

Before any public preview, test authentication, origin/host rejection, replayed
cards, cancellation, concurrent tasks and restart recovery; verify TLS and proxy
configuration from outside the host. An authenticated local fixture test does
not establish that a public deployment is secure.

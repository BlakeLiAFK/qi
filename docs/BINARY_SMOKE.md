# Actual-binary smoke validation

Date: 2026-11-04 (UTC)

## Scope and identity

An actual Linux amd64 `qi` executable was built and exercised in the assistant's
cloud workspace. This was a locally tested workspace snapshot, **not a verified
Git commit or release build**. No binary was published.

- Binary SHA-256: `970fd1cffa75dc63e2cd4dbf937c323b0b7c5f7575f54cb8a7fd71f84bd324f7`
- Observed format: ELF 64-bit x86-64, dynamically linked, with debug information.
- Go-source snapshot SHA-256, recorded after testing:
  `62eeaa913b5db09741d3e56ee700fca5903ae1312a5f7fb5edb73c230e8b232c`
- Snapshot includes 156 files: all `*.go` files plus `go.mod` and `go.sum`, sorted
  by relative POSIX path. The digest concatenates each path, a NUL byte, its raw
  contents, and another NUL byte. This is an informational source fingerprint,
  not a claim of reproducible binary identity or exact Git provenance.

## Results

| Check | Result |
| --- | --- |
| Actual binary starts on a literal loopback listener | Passed |
| Unauthenticated task API request | Rejected with HTTP 401 |
| Authenticated WebUI root and task API | HTTP 200 |
| Write with a different Origin | Rejected with HTTP 403 |
| Create two independent tasks | HTTP 202 for each task creation |
| Submit one synthetic turn per task | HTTP 200 with acceptance response |
| Both tasks receive synthetic model answers | Passed |
| Model request context isolation | Each request contained only its own unique task marker |
| Persisted task history isolation | Each task contained only its own marker and answer |
| Default tool availability | Neither model request advertised tools |
| Stop and restart against the same SQLite database | Both completed histories retained |
| Model replay after restart | None; model request count remained two |
| Public preview with coding workers enabled | Startup rejected |
| Public preview with MCP configuration enabled | Startup rejected before reading the missing configuration |
| Private storage permissions | Data directory 0700; database and engine lock 0600 |

All processes launched for the smoke test were stopped. The model fixture used
only loopback HTTP, a synthetic model name, and dummy credentials. The daemon was
launched with a minimal explicit environment rather than inherited credentials.
No external model service, user data, user computer, or production deployment was
used.

## Build and focused-test commands

Run from the repository root. Directory variables below replace environment-
specific temporary paths; create them under a private, writable parent. Use the
installed Go binary on `PATH`.

```sh
export TMPDIR="$PRIVATE_ROOT/build-tmp"
export GOPATH="$PRIVATE_ROOT/go-path"
export GOCACHE="$PRIVATE_ROOT/go-cache"
export SMOKE_ROOT="$PRIVATE_ROOT/binary-smoke"
mkdir -p "$TMPDIR" "$GOPATH" "$GOCACHE" "$SMOKE_ROOT"
chmod 700 "$SMOKE_ROOT"
GOOS=linux GOARCH=amd64 go build -buildvcs=false -o "$SMOKE_ROOT/qi" ./cmd/qi
sha256sum "$SMOKE_ROOT/qi"
go test -buildvcs=false ./internal/webui ./cmd/qi \
  -run 'TestPreview|TestPublicPreview' -count=1 -v
```

The initial build without `-buildvcs=false` failed because VCS status lookup exited
128. Disabling VCS stamping allowed compilation. This does not establish a Git
revision.

The focused test command passed all ten matching tests:

- `TestPreviewAuthenticationAndCookie`
- `TestPreviewLogoutRevocationAndExpiry`
- `TestPreviewLoginCSRFAndProxyBoundary`
- `TestPreviewLimiterDoesNotBlockExistingSession`
- `TestPreviewSessionCapacityRotationAndRestart`
- `TestPreviewCookieMutationCSRF`
- `TestPreviewLogoutClosesEventStream`
- `TestPublicPreviewDaemonWiring`
- `TestPublicPreviewRejectsProcessToolsBeforeSideEffects`
- `TestPublicPreviewAllowsAuthenticatedArtifactsAndQuestions`

## Actual-binary reproduction procedure

The actual run used a temporary Python harness controlling the binary, a
`ThreadingHTTPServer` bound to `127.0.0.1` on an ephemeral port, and HTTP requests.
The harness was not added to the repository. The steps below specify its tested
requests and assertions so the check can be reproduced without its machine-
specific paths.

1. Create an existing private directory with mode 0700 for `tasks.db`.
2. Start a loopback-only model fixture handling `POST /v1/chat/completions`.
   Record each JSON request, locate its last user message, and respond with:
   `{"choices":[{"message":{"role":"assistant","content":"synthetic answer <last user message>"},"finish_reason":"stop"}]}`.
3. Launch the binary with the following argument/environment shape, substituting
   free loopback ports and locally generated synthetic values. Do not use real
   API credentials.

```sh
env -i PATH="$PATH" HOME="$SMOKE_ROOT" \
  QI_WEB_TOKEN="$SYNTHETIC_TOKEN" OPENAI_API_KEY="$SYNTHETIC_API_KEY" \
  "$SMOKE_ROOT/qi" serve \
  -listen "127.0.0.1:$APP_PORT" \
  -db "$PRIVATE_DATA/tasks.db" \
  -model synthetic-local \
  -base-url "http://127.0.0.1:$MODEL_PORT/v1"
```

4. Wait for authenticated `GET /api/tasks` to return 200. Authenticate using Basic
   username `qi` and the synthetic token, which must be at least 32 characters.
5. Assert unauthenticated `GET /api/tasks` returns 401 and authenticated `GET /`
   returns 200. Assert `POST /api/tasks` with a different Origin returns 403.
6. For writes, send `Content-Type: application/json`,
   `Origin: http://127.0.0.1:<APP_PORT>`, and `X-Qi-Request: 1`.
7. Create two tasks using `POST /api/tasks` with bodies
   `{"title":"ALPHA_PRIVATE"}` and `{"title":"BETA_PRIVATE"}`. Capture their
   returned IDs and assert HTTP 202.
8. Submit `{"prompt":"ALPHA_PRIVATE"}` and `{"prompt":"BETA_PRIVATE"}` to
   their respective `/api/tasks/<id>/messages` endpoints. Assert HTTP 200.
9. Poll each `/api/tasks/<id>` until its synthetic answer appears. Assert its JSON
   contains its own marker and does not contain the other task's marker. Assert
   exactly two model requests, each containing only its task marker and no tools.
10. Terminate the daemon with SIGTERM, wait for exit, and start it again with the
    same arguments and database. Fetch both tasks and verify both answers remain.
    Assert the fixture still recorded exactly two requests.
11. Stop the daemon. Run the same binary separately with `-public-preview
    -origin https://qi.example -enable-workers`, and with `-public-preview
    -origin https://qi.example -mcp-tools <nonexistent-file>`. Both invocations
    must exit 1 with the OS-isolation-not-implemented rejection. These startup
    checks do not contact the example origin.
12. Stop the model fixture and inspect storage modes. Do not leave test listeners
    running.

An initial harness assertion incorrectly expected HTTP 202 for message submission;
it was corrected to the implemented HTTP 200 acceptance contract, after which the
complete actual-binary sequence passed. This was a test-harness correction, not an
application-code change.

## Limits

- Public-preview login/session behavior was tested through in-process HTTP
  handlers. These tests are **not evidence of real TLS, certificate validation,
  reverse-proxy configuration, or internet deployment**.
- WebUI coverage fetched authenticated HTML and exercised APIs; it did not drive
  an interactive browser or verify visual layout.
- The restart check covered completed turns and graceful process shutdown. It
  did not simulate power loss or validate every crash-recovery path.
- Task isolation here means separate model context and stored histories for a
  single-user service. It is not multi-user authorization or OS process isolation.
- This report covers the listed checks, not an independent full security audit or
  full-suite result. Real model compatibility, paid requests, worker execution,
  MCP execution, and production deployment were outside this smoke test.

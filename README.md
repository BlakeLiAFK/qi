# qi

A small Go assistant with a persistent task WebUI and OpenAI-compatible or native Anthropic model/tool loops,
explicitly configured MCP stdio plugins, and optional Codex/Claude coding workers.
This is an early implementation, not feature parity with Pi or a production sandbox.

`qi isolation-preflight` prints a read-only JSON prerequisite report and exits
nonzero when isolation is not verified (currently always). It does not install or
enable workers. See [the prototype and remaining Linux prerequisites](docs/worker-isolation-prototype.md).

## Run

Requires Go 1.22+ and access to an OpenAI-compatible Chat Completions API or
the native Anthropic Messages API.

```sh
go build -o qi ./cmd/qi
export OPENAI_MODEL='your-model'
export OPENAI_API_KEY='your-key'
# Optional for another compatible provider:
# export OPENAI_BASE_URL='http://127.0.0.1:8080/v1'
./qi -p 'Explain this project in one paragraph'
./qi
```

Interactive mode accepts one line per turn and `/quit` to exit. Conversation history
is retained only in memory until exit in standalone CLI mode. One-shot `-p` prints the final assistant text.
Responses are buffered, not token-streamed. Standalone CLI mode has no sessions on disk.
Automatic retries, context compaction, scheduling, and a TUI are not implemented.
`-model` and `-base-url` override their environment variables. The API key may be
omitted for compatible local servers. The model name must be explicitly supplied.
Run `./qi -h` for flags. Diagnostics and permission prompts use stderr; answers use stdout.

## Model providers

`-provider openai` is the default and preserves `OPENAI_MODEL`, `OPENAI_API_KEY`,
and `OPENAI_BASE_URL`. It uses the OpenAI-compatible Chat Completions protocol,
with official API root `https://api.openai.com/v1` when no base URL is configured.

For the native Anthropic Messages API:

```sh
export ANTHROPIC_MODEL='your-anthropic-model'
export ANTHROPIC_API_KEY='your-key'
./qi -provider anthropic -p 'Explain this project'
# Or use the same provider for persistent WebUI tasks:
./qi serve -provider anthropic
```

Set `QI_WEB_TOKEN` as described below before starting the server. The Anthropic
provider uses only `ANTHROPIC_MODEL`, `ANTHROPIC_API_KEY`, and
`ANTHROPIC_BASE_URL`; it never falls back to OpenAI model names, endpoints, or keys.
Its official default root is `https://api.anthropic.com/v1`. Both providers accept
`-model` and `-base-url` overrides. API keys are environment-only, with no key flag;
an absent key is permitted for local fixtures. Base URLs are API roots, not the
`/messages` or `/chat/completions` endpoint itself. No environment file is loaded
automatically. Provider choice belongs to the standalone process or daemon;
`qi attach` uses the daemon’s provider.

Anthropic requires an output-token cap. qi defaults it to **4096 tokens**;
`ANTHROPIC_MAX_TOKENS` sets a positive integer default, and
`-max-output-tokens 8192` overrides it. That option is rejected with the OpenAI
adapter rather than silently ignored. Model-specific output/context limits still
apply. This output-token setting is separate from `QI_MAX_CONTEXT_BYTES`, which
bounds serialized input bytes rather than tokens.

Native Anthropic supports buffered text and client tool calls/results. Native
streaming is not implemented: `qi serve -provider anthropic -model-stream` fails
before opening the server or issuing a model request. Unsupported content blocks
and stop modes fail closed rather than being silently discarded. Native thinking
blocks, server-side tools, mid-conversation system messages, and text appearing
after a tool-use block are not supported. The adapter pins the stable
`anthropic-version: 2023-06-01` header. This native API
transport is separate from the optional `claude` coding subprocess and its
`QI_CLAUDE_API_KEY` credential. Tests use local HTTP fixtures only; no live Anthropic
requests or paid calls were made to validate this implementation.

## Persistent task WebUI

```sh
# Generate a private token locally; do not commit it or put it in a URL.
export QI_WEB_TOKEN="$(openssl rand -hex 32)"
export OPENAI_MODEL='your-model'
export OPENAI_API_KEY='your-key'
./qi serve
```

Open `http://127.0.0.1:8686` and sign in with username `qi` and your
`QI_WEB_TOKEN` as the password. The token must have at least 32 characters and
is read only from the environment, never a command-line flag. The browser may
cache Basic Auth credentials; close its session when using a shared computer.
Treat the token as full access to all tasks and model usage. This is single-user
authentication, not multi-user accounts or role-based access.

The WebUI creates independent tasks, posts asynchronous model turns, displays
persisted conversation/run state, and cancels active turns. Each task gets its own
history; concurrent tasks do not share model context. SQLite stores task messages,
run records, and events, with WAL and committed-write recovery. One engine owns a
database at a time. After an unclean restart, abandoned runs become interrupted;
work is not silently replayed. Queued turns also become interrupted on restart and
require explicit resubmission after review. The default database is `qi/tasks.db` within the OS
user config directory. For this default, the OS configuration parent directory
must already exist and pass the ancestor safety checks. qi validates it **before**
creating only the final `qi` directory as mode 0700. An existing `qi` directory is
validated without changing its mode or ownership. Unsafe ancestors are rejected
without creating child directories. Use `qi serve -db /private/path/tasks.db` to
change the database location; its private parent directory must already exist. Stored prompts and outputs are plaintext,
not encrypted: keep the database, WAL, and backups private and outside your repository.
The task list marks unread terminal results; [read-state semantics](docs/unread-results.md)
explain exact-run acknowledgments and the shared single-owner watermark.
Use explicit `qi backup` and `qi backup-verify` commands for consistent local snapshots;
see [database backup and verification](docs/database-backup.md).

Persistent storage currently supports Linux, macOS, and the BSD Unix variants
covered by the permission checks. The dedicated data directory must belong to
the current user with mode `0700` or stricter. Existing database, WAL, SHM,
journal, and engine-lock files must be regular files owned by that user with
mode `0600` or stricter and a single hard link. Unsafe existing paths are rejected
without changing permissions or contents. Data-directory/file symlinks and
untrusted writable ancestors are rejected; root-managed ancestor aliases such
as macOS `/tmp` are supported. This is local Unix filesystem protection, not
encryption, a distributed lock, or protection from another process using the
same OS account. Review and relocate old data into a private directory yourself
before retrying; qi does not silently chmod user data.

**Windows persistent storage is currently unsupported:** database open fails
closed until native ACL validation is implemented. A successful Windows
cross-compilation does not imply runtime storage support. The same restriction
applies to other platforms without implemented permission validation. See
[storage privacy](docs/storage-privacy.md) for API and recovery details.

The server has no tools enabled by default. Opt-in coding actions use durable,
action-specific WebUI approval (see below). Static, approval-gated MCP tools can also be explicitly configured (see below). Informational cards do not execute work. Use `qi attach` to share daemon
tasks from the terminal; bare `qi` remains a separate, in-memory session.

The listener accepts only literal loopback IP addresses. For another local port,
use `-listen 127.0.0.1:9000`. `-origin` sets one exact browser origin, without a
trailing slash; it defaults to the actual HTTP listen address. Host and origin
must match, and writes need the same-origin request marker. Remote access needs
an independently configured HTTPS reverse proxy on the same machine, forwarding
the original Host header, with `-origin https://your-private-host`. Do not expose
cleartext HTTP or log Authorization headers. Proxy setup, TLS, public deployment,
and firewall rules are not automated by qi. Authentication rate limiting uses the
actual peer address, never forwarded client-IP headers; reverse-proxy users share
that bucket. Configure additional rate limiting at the proxy edge.

`qi serve -h` lists server options. Ctrl-C or SIGTERM shuts down the server and
cancels active and queued runs. The UI uses SSE progress notifications and snapshot
polling. With the OpenAI-compatible provider, opt in using `qi serve -model-stream`
for provisional model text updates;
this remains off by default. Live previews are bounded, task/run scoped, and may
pause at size or event limits. A completed message replaces its preview. Partial
tool arguments are never rendered or executed. Interrupted preview text is not
a completed answer and is not replayed into model history.

## Restricted public-preview mode

`qi serve -public-preview -origin https://your-private-host` enables the separate
HTTPS browser-session login/logout mode. Configure the trusted TLS reverse proxy
and access controls described in the deployment/security documentation before
exposing it. Basic authentication and `qi attach` are disabled in this mode.

**Public-preview mode rejects `-enable-workers` and `-mcp-tools` at startup.**
The check occurs before configuration files, executable paths, listeners, or the
database are used. The daemon constructor also enforces the restriction. Explicit
per-call approval and environment filtering do not replace real OS isolation;
there is no flag to override this restriction while the isolated backend remains
unimplemented. Standalone/local development opt-ins are unchanged.

Authenticated question cards and explicit-root deliverables may still be enabled
with `-question-cards` and `-artifact-dir`. They retain their normal permission,
storage-isolation, and non-execution boundaries. Authentication is still required
for all protected task content and deliverables.

## Archive and restore tasks

Use **Archive task** to hide an idle or finished task from the default task list.
Use **Show archived** to inspect retained tasks and **Restore task** to bring one
back. Archiving is reversible, not deletion: conversation history, runs, audit
records, deliverable registrations, and idempotency records remain on disk and
accessible through the authenticated task view. Archive changes have their own
transactional audit records. No permanent-deletion endpoint is provided.

Queued or running tasks cannot be archived or restored. An archived task cannot
start a new turn until restored; retrying a previously accepted idempotency key
still returns its original acceptance without executing work again. Archived tasks
continue to count toward the maximum retained-task capacity. Archiving does not
free disk space or reset quotas.

The authenticated API uses `POST /api/tasks/<id>/archive` with JSON
`{"archived":true}` or `{"archived":false}` and the normal same-origin write
headers. `GET /api/tasks` excludes archived tasks;
`GET /api/tasks?archived=true` lists archived tasks only. Direct task history and
registered-deliverable downloads remain authenticated and available.

## Task question cards

```sh
./qi serve -question-cards
```

This explicit flag exposes only the task-local `ask_user_question` and
`get_question_answers` tools. Without it, they are not available to the model.
Questions can request a bounded choice or short text response in the WebUI.
Answers are recorded with the task, not treated as permission to run coding or
MCP actions. Answering does **not** automatically resume or start a model turn;
use the saved card’s **Continue task** button to explicitly start a new turn. The
Question expiry applies to submitting an answer, not to an already saved answer.
The button consumes that card once, atomically with the new run and its input.
Duplicate clicks cannot create duplicate turns, including after restart. A crash
after acceptance marks the run interrupted; it never automatically replays
external work. Cards from older, cancelled, failed or interrupted runs require a
new message instead. There is no automatic background continuation queue or
exactly-once guarantee for external actions. Do not enter passwords, API keys, or other secrets
in question cards. Question features are separate from execution approvals.

## Registered deliverables

```sh
mkdir -m 700 /private/path/qi-deliverables
./qi serve -artifact-dir /private/path/qi-deliverables
```

Use a dedicated existing directory, separate from credentials, repositories,
and the SQLite database. Nothing is exposed by default. Files must already exist
under `<artifact-dir>/<task-id>/<relative-path>`. The directory and all opened
path components must not be symlinks. Only regular files up to 32 MiB are allowed;
dotfile segments, absolute paths, traversal, and backslash paths are rejected.
Each task allows at most 100 registered versions; registering the same task/path/hash
again returns the existing record. Exceeding the quota returns HTTP 409. Reads/hashes
and active registration/download responses are each capped at four concurrently;
a saturated web transfer limit returns HTTP 429 for a later retry.

Explicit registration is available through the authenticated API:
`POST /api/tasks/<task-id>/artifacts` with JSON `{"path":"report.pdf"}`.
It registers metadata and a content hash for an existing task-scoped file, not an
upload or a new file creation. Enabling this root also exposes the task-scoped
`artifact_publish` model tool. It requests an exact user approval showing the
relative filename, size, content hash, and task-only destination before registering.
The bytes are rechecked after approval; changed files fail closed. Rejection,
cancellation, expiry, and restart never publish automatically. A successful tool
result means registered for download, not downloaded or externally delivered.
There is no automatic workspace scanning, worker-output import, or WebUI upload.
The WebUI lists registered deliverables and offers downloads.
`GET /api/tasks/<task-id>/artifacts` lists metadata, and app-provided download links
point to authenticated artifact IDs, never raw filesystem paths.

Downloads recheck size and hash, refusing changed or missing files. They are served
as attachments with an octet-stream type and sandbox policy, so generated HTML/SVG
is not rendered at the authenticated app origin. Registration does not copy files
or preserve old bytes. Keep the files available and use a new registration for a
changed version. All holders of the single-user daemon token can access registered
tasks and files; this is not recipient-specific sharing. Never place secrets in
the deliverable tree, and do not enable it on a public preview without access controls.

Path checks and hashes are **not a hostile same-UID security boundary**. For untrusted
outputs, use storage owned by a separate daemon identity that workers cannot write;
a trusted operator should review and copy outputs into that storage. Merely naming
a directory “private” does not isolate same-user processes. Files with multiple
hardlinks are rejected, but link/unlink races and hostile same-UID replacement
cannot be fully prevented by this layer.

## Attach a terminal to daemon tasks

In another terminal, set `QI_WEB_TOKEN` to the **same** token used by `qi serve`:

```sh
./qi attach -list
./qi attach -title 'Investigate a bug'       # Creates a persistent task
./qi attach -task TASK_ID                   # Continues an existing WebUI task
./qi attach -task TASK_ID -p 'Summarize progress'
```

The attached REPL and WebUI use the same history and run state through the daemon
API. No database file is opened by the client. A task permits one outstanding turn;
submitting while it is already running or queued is rejected. Independent tasks
wait in a durable FIFO queue once execution slots are full. The serve flags
`-max-concurrent 8 -max-queued 64` control execution slots and waiting capacity.
Queue-full rejects the new message without saving it. `/history`
refreshes messages, `/wait` polls the active turn, `/cancel` requests cancellation,
and `/quit` disconnects. Normal lines submit messages and wait for a result.
The terminal polls at 300 ms while waiting; it does not stream tokens or receive
updates automatically while sitting at its prompt. `/skills` lists configured
references; `/use-skills name1,name2` selects references for the next accepted
message only, and `/use-skills` clears that pending selection.

Pending approvals are shown as a notice. Review the exact action in the WebUI,
then use `/wait` to follow completion. One-shot `attach -p` returns a nonzero exit
while approval is pending, rather than claiming the action completed. Exiting,
EOF, or Ctrl-C disconnects without cancelling daemon work. Cancel explicitly in
the WebUI or with `/cancel`. Ctrl-C interrupts idle terminal input and approval prompts without requiring
Enter; attached clients still leave daemon work running.

Use `-url http://127.0.0.1:9000` for another local daemon or an explicitly trusted
HTTPS origin for a configured proxy. The URL must not contain credentials, paths,
queries, or a trailing slash. Plain HTTP is restricted to literal loopback IPs.
The client never follows redirects and does not use proxy environment variables.
Its token is read only from `QI_WEB_TOKEN`, never CLI arguments or printed output.
`qi attach -h` lists options. It does not need model credentials; the daemon does.

## Context byte budget

`QI_MAX_CONTEXT_BYTES` sets a positive integer budget for serialized model-input
messages and tool definitions, default **262144 bytes (256 KiB)**. It applies to
standalone `qi` and `qi serve`; `qi attach` uses the daemon’s setting. This is a byte
budget, **not a token limit**, and excludes provider request metadata. Provider
limits can still reject a request.

Before each model request, the wrapper drops oldest complete conversation turns
from the model view until it fits, preserving system/developer instructions and
the latest whole turn, including complete tool-call/result groups. It never
summarizes or abbreviates messages. If required context cannot fit, the request
fails locally without contacting the provider. Full history stays unchanged in
memory or SQLite. This is input selection, not compaction or summarization, and
instructions in omitted older user turns are no longer visible to the model.
Standalone CLI requests print a diagnostic when earlier messages are omitted,
including a warning that earlier user instructions may no longer be visible.
Attached terminals display the saved notice once per affected run; the notice
describes a recorded model-input change, not deletion of conversation history.

## Explicit local skill references

```sh
./qi serve -skill-root /private/path/skills -skill-root /another/skills-root
./qi attach -task TASK_ID -p 'Review this change' -skills review-style
```

No skill directories are discovered automatically. Each explicit root may contain
`SKILL.md` directly and/or immediate child directories with `SKILL.md`. Roots are
searched in command-line order; the first name wins collisions. Missing or invalid
roots fail startup instead of being silently ignored. Example document:

```text
---
name: review-style
description: A short review checklist
---
Check correctness, error handling, and tests. Explain uncertainty explicitly.
```

Configuring a root only exposes metadata. Select references explicitly in the
WebUI or with `qi attach`. For interactive terminals, `/skills` lists names and
`/use-skills review-style` applies to the next accepted message only. Selection
resets after submission; an empty `/use-skills` clears it. The `-skills` option is
accepted only with one-shot `-p`, not as an automatic session default. Bare
standalone `qi` currently has no skill-root or skill-selection support.

Selected document snapshots are stored as labeled **untrusted local reference
content** in that task’s user turn. They are not system policy, never execute
scripts, never enable tools, and never grant permission. The snapshot remains in
that task’s ordinary history, so clearing a selection does not erase earlier
references. Combined prompt/reference limits apply; oversized or unknown
selections are rejected before starting a run. Skill contents are sent to the
configured model and stored in SQLite with that turn; do not select documents
containing secrets. All coding/MCP execution still requires its separate approval.

## Approval-gated MCP tools in the daemon

Enable only a static allowlist of trusted local tools:

```sh
./qi serve -mcp-tools /private/path/mcp-tools.json
```

Example file (replace the executable and directory with real trusted paths):

```json
[
  {
    "name": "example",
    "command": "/absolute/path/to/trusted-mcp-server",
    "args": [],
    "dir": "/path/to/workspace",
    "tools": [
      {
        "name": "lookup",
        "description": "Look up a local item",
        "inputSchema": {
          "type": "object",
          "properties": {"query": {"type": "string"}},
          "required": ["query"],
          "additionalProperties": false
        }
      }
    ]
  }
]
```

The file is limited to 1 MiB and 1–16 servers. Unknown fields are rejected;
`env`, credentials, and dynamic discovery are unsupported. Each server needs a
unique alphanumeric/underscore name starting with a letter (up to 24 characters),
an executable, an existing explicit directory, and a nonempty static tool list.
Tool names follow the same naming rule. Executables and directories are checked
at daemon startup, but **no MCP process is launched during configuration loading**.
Relative paths are resolved from qi’s working directory, not the JSON file.

The model sees `mcp_example__lookup`. Each invocation requires a new exact-action
WebUI approval covering the command, startup arguments, directory, tool name,
arguments, timeout, and side-effect warning. Only after approval does qi launch a
fresh server, initialize it, and make that call. Starting an executable can itself
have side effects, even before the tool call. No approval means no process start.
Defaults are 30 seconds per call and five minutes to approve; this first JSON
format does not expose timeout overrides. Configure accurate tool descriptions
and schemas yourself; qi does not fetch a remote catalog or grant extra tools.

Scoped MCP currently requires a Unix platform with process-group cleanup support.
Scoped MCP processes use a disposable home and minimal system environment without
credential overrides. Do not embed secrets in commands, startup arguments,
descriptions, schemas, or tool arguments: configuration and approvals are durable
and model-visible where applicable. No hosted/OAuth/credential-bearing MCP setup
is supported in this daemon mode. Same-UID filesystem, process, and network access
remain possible, so this is **not a sandbox** and is unsafe for untrusted plugins
or any public tool-enabled deployment without actual isolation. The explicit
`-public-preview` mode rejects `-mcp-tools`. MCP remains off unless
`-mcp-tools` is explicitly supplied. Cancellation/recovery cannot roll back external
changes or guarantee exactly-once effects. Tests use fixtures, not live services.

## Standalone MCP plugins

Create a trusted JSON configuration file:

```json
[
  {
    "name": "example",
    "command": "/absolute/path/to/trusted-mcp-server",
    "args": [],
    "env": ["EXAMPLE_SETTING=value"],
    "dir": "/path/to/workspace"
  }
]
```

```sh
./qi -plugins ./plugins.json
```

Passing `-plugins` explicitly authorizes starting those programs. No configuration
is auto-discovered. Servers receive routine process settings (including the user’s HOME), plus only
explicit `env` entries, and run with the user’s privileges. Ambient API tokens are
not copied by default, but same-user files and stored logins may remain accessible. Review commands before enabling them. Startup
can have side effects before any tool approval. Only newline-delimited MCP stdio
is supported, with initialization and paginated tool discovery. Tool names are
prefixed `example__` to avoid collisions. Relative command paths and working
directories resolve from qi's current working directory, not the config file.

Each tool invocation displays its name and JSON arguments and requires entering
`y`. Empty input, EOF, and other answers deny the call. Use `-yes-tools` only to
explicitly authorize *all* model-requested tool calls for that session. Plugin
code is not sandboxed; confirmations are a call gate, not protection against a
malicious server. Plugin descriptions and tool results also reach the model.

## Approval-gated coding tasks in the daemon

```sh
./qi serve -enable-workers -work-dir /absolute/path/to/repo
```

The workspace must be explicitly supplied. This enables `coding_run`, whose
model-controlled inputs are the CLI choice (`codex` or `claude`) and prompt.
The workspace and runtime are operator-configured. Executable paths are resolved
at startup; missing CLIs remain unavailable. Runtime defaults to 15 minutes and
`-worker-timeout` allows a positive duration up to one hour.

Before dispatch, the WebUI presents a durable decision bound to the task, exact
prompt, executable, workspace, timeout, revision, and expiry. Approving that action
allows its waiting turn to proceed; rejecting it does not run the command. The
claim is single-use. The default approval expiry is five minutes. An approval is
not broad permission for later commands. Informational cards remain nonexecuting.

The daemon waits for child completion and records bounded prefix-plus-tail output (64 KiB, with an explicit gap marker when truncated) and
run/approval status. Cancelling the turn or stopping the daemon terminates the
worker, but cannot undo file or network changes. A failed, cancelled, or interrupted
process can have an unknown outcome and requires checking the workspace before
retrying. Restart recovery does not replay dispatched actions. This is durable
bookkeeping, not exactly-once external effects or filesystem rollback.

The workspace is checked for path replacement before dispatch, but remains **not
an OS security boundary**. Use an isolated account/container for untrusted projects.
Workers receive a minimal system environment and an ephemeral HOME/config directory,
not the daemon environment or the user’s stored CLI login. Ordinary `OPENAI_API_KEY`,
`CODEX_API_KEY`, `ANTHROPIC_API_KEY`, and `QI_WEB_TOKEN` are not inherited. To provide
credentials deliberately, set dedicated variables before starting qi:

- `QI_CODEX_API_KEY` is passed to workers as `CODEX_API_KEY`.
- `QI_CLAUDE_API_KEY` is passed to workers as `ANTHROPIC_API_KEY`.

These settings apply only when workers are explicitly enabled, in daemon or standalone
mode. qi does not log configured values or include them in approval records;
worker output can still disclose secrets, so only use trusted tools. Do not put
credentials in command-line flags, prompts, repository files, or committed scripts.
Use your private shell/session or secret manager to set them. Without dedicated
credentials, authenticated CLI operations may fail; stored login sessions are not
copied into the ephemeral home. No live paid calls were made to test this mapping.

Environment filtering is **not hostile-code isolation**. Workers still run as the
same OS user and may be able to read daemon credentials, inspect processes, access
SQLite state, or modify other files. Public-preview mode rejects worker enablement entirely. Other worker-enabled
public deployments are unsafe without actual process/user/container isolation
and access controls. These are
not automatically configured by qi. Jobs for one canonical workspace are serialized
within one qi process only; separate daemon instances are not coordinated.

## Standalone coding workers

```sh
./qi -enable-workers -work-dir /path/to/repo
```

This exposes `coding_start`, `coding_status`, and `coding_cancel`. Install and
configure the official `codex` and/or `claude` CLI separately. qi does not install
them or verify CLI version compatibility. Use the dedicated worker credential
variables described above if authentication is needed. The model chooses
`codex` or `claude` and the prompt, but cannot choose a different working directory.
Current invocations are:

- `codex --ask-for-approval never exec --sandbox workspace-write -- <prompt>`
- `claude --bare --permission-mode default -p --output-format text -- <prompt>`

Workers use the same minimal environment, ephemeral HOME, and explicit credential
settings described above. They can edit files and may access the network under
their own CLI permissions. A fixed working directory is **not** a
security boundary. Codex runs with its workspace-write sandbox and interactive
escalation disabled (`--ask-for-approval never`). Claude uses its default permission
mode. Commands needing interactive permission or input may fail in this
noninteractive mode. These invocation forms are used by both standalone and
daemon workers; the daemon additionally requires its action-specific approval.

Worker starts return task IDs. Status reads return up to 64 KiB of combined output, retaining the beginning
and latest ending rather than discarding final results. Truncation is indicated
by a gap marker in the output and a separate truncation flag. Runtime defaults to 15 minutes (`-worker-timeout` changes it).
Up to 100 tasks are retained per process. Tasks and their output are in memory only;
all active workers are cancelled when qi exits, including after a one-shot answer.
For work that needs multiple follow-up prompts, use interactive mode. There is no
automatic background polling or guarantee that a model waits for task completion.

## Development

For a complete, runnable Go MCP plugin and configuration examples, see [examples/textstats](examples/textstats/README.md).


```sh
go test ./...
go test -race ./...
go vet ./...
```

Tests use local HTTP fixtures and simulated coding CLI processes; they do not need
API credentials. No real Codex/Claude adapter invocation, installed-version
compatibility, or live provider end-to-end operation has been verified. CLI tests
cover standalone and attached conversations, shared daemon history, persistence,
authentication, redirects, approvals pending, and worker opt-in validation. Internal
packages cover storage, approval lifecycle, recovery, agent loops, transport, MCP,
WebUI, and worker lifecycle behavior.

## Boundaries

- Prompts, history, tool definitions, and results are transmitted to the configured
  model endpoint. Only use providers and plugins you trust with those contents.
- No tools are enabled by default. Plain chat cannot read or modify local files.
- Standalone tool approvals use terminal input; daemon coding approvals use the
  authenticated WebUI. Neither is a multi-user permission system. Do not feed
  untrusted input into the standalone approval stream.
- `-max-steps` bounds model calls per turn (default 20). Tool errors are returned to
  the model; model errors and exhausted limits end the session with a nonzero exit.
- Ctrl-C in standalone/server mode cancels their work; attached clients disconnect
  without cancelling daemon work. Idle stdin and approval waits are cancellable;
  their input watcher is stopped and joined on exit.

### Model transport security

Remote `OPENAI_BASE_URL` and `ANTHROPIC_BASE_URL` endpoints must use HTTPS. Plain HTTP is permitted only
when the configured hostname is a literal loopback IP, such as `127.0.0.1` or
`[::1]`, for a local model server. `localhost`, DNS aliases, and other hostnames
do not qualify. URL credentials, query strings, and fragments are rejected.
Model requests never follow redirects, including redirects from HTTPS, to avoid
forwarding API keys or conversation contents to another endpoint. Configure the
final API root directly. HTTPS does not establish provider trust: choose a
provider you trust with the full conversation and tool outputs.

## Optional saved memory

The WebUI’s **Manage saved memory** link opens authenticated local management.
Choose one task or explicitly choose global, then create, inspect revisions, edit
or delete your own records. Nothing is selected by default or sent to models.
The composer can explicitly select individual current revisions for one turn
after you confirm that those facts will be sent to the configured model provider.
Selection grants no tools or access to other tasks and resets after acceptance.
Run audit snapshots remain available after source deletion; model replies may
quote facts into ordinary conversation history. Do not save credentials or
sensitive personal information. Records are plaintext; deletion removes live
curated revisions, not run snapshots, backups or SQLite remnants. See [boundaries and API](docs/explicit-memory.md).

### Inspect registered plugins and tools

The authenticated WebUI’s **Plugins & tools** link opens a read-only catalog of
this daemon’s built-ins and static MCP registrations, including enabled/disabled
state, declared capabilities, configuration source, and sandbox limitations.
It never launches plugins. Changes remain operator-managed startup configuration
and require a restart; there are no WebUI installation, credential, or enablement
controls. See [the plugin catalog guide](docs/plugin-catalog.md).

# qi

A small Go assistant with a persistent task WebUI and an OpenAI-compatible model/tool loop,
explicitly configured MCP stdio plugins, and optional Codex/Claude coding workers.
This is an early implementation, not feature parity with Pi or a production sandbox.

## Run

Requires Go 1.22+ and access to an OpenAI-compatible Chat Completions API.

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
work is not silently replayed. The default database is `qi/tasks.db` within the OS
user config directory. Use `qi serve -db /private/path/tasks.db` to change it;
the parent directory must already exist. Stored prompts and outputs are plaintext,
not encrypted: keep the database, WAL, and backups private and outside your repository.

The server supports **model chat only**. MCP plugins and coding workers are not
exposed over the WebUI. Card decisions are recorded state, not executable tool
approval or an automatic resume mechanism. The standalone CLI/REPL currently
runs separately and does **not** attach to the daemon or share WebUI tasks.

The listener accepts only literal loopback IP addresses. For another local port,
use `-listen 127.0.0.1:9000`. `-origin` sets one exact browser origin, without a
trailing slash; it defaults to the actual HTTP listen address. Host and origin
must match, and writes need the same-origin request marker. Remote access needs
an independently configured HTTPS reverse proxy on the same machine, forwarding
the original Host header, with `-origin https://your-private-host`. Do not expose
cleartext HTTP or log Authorization headers. Proxy setup, TLS, public deployment,
and firewall rules are not automated by qi.

`qi serve -h` lists server options. Ctrl-C or SIGTERM shuts down the server and
cancels active runs. The UI polls state; it does not stream model tokens.

## MCP plugins

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
is auto-discovered. Servers inherit the qi process environment (including secrets)
and run with the user's privileges. Review commands before enabling them. Startup
can have side effects before any tool approval. Only newline-delimited MCP stdio
is supported, with initialization and paginated tool discovery. Tool names are
prefixed `example__` to avoid collisions. Relative command paths and working
directories resolve from qi's current working directory, not the config file.

Each tool invocation displays its name and JSON arguments and requires entering
`y`. Empty input, EOF, and other answers deny the call. Use `-yes-tools` only to
explicitly authorize *all* model-requested tool calls for that session. Plugin
code is not sandboxed; confirmations are a call gate, not protection against a
malicious server. Plugin descriptions and tool results also reach the model.

## Optional coding workers

```sh
./qi -enable-workers -work-dir /path/to/repo
```

This exposes `coding_start`, `coding_status`, and `coding_cancel`. Install and
configure the official `codex` and/or `claude` CLI separately. qi does not install
them, provide credentials, or verify CLI version compatibility. The model chooses
`codex` or `claude` and the prompt, but cannot choose a different working directory.
Current invocations are:

- `codex exec --sandbox workspace-write -- <prompt>`
- `claude --bare -p --output-format text -- <prompt>`

Workers inherit the process environment. They can edit files and may access the
network under their own CLI permissions. A fixed working directory is **not** a
security boundary. qi does not bypass their permission controls or approval prompts;
commands that need interactive input may fail in this noninteractive mode.

Worker starts return task IDs. Status reads return up to 64 KiB of combined output
and a truncation flag. Runtime defaults to 15 minutes (`-worker-timeout` changes it).
Up to 100 tasks are retained per process. Tasks and their output are in memory only;
all active workers are cancelled when qi exits, including after a one-shot answer.
For work that needs multiple follow-up prompts, use interactive mode. There is no
automatic background polling or guarantee that a model waits for task completion.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
```

Tests use local HTTP fixtures and fake processes; they do not need API credentials.
The CLI tests cover one-shot and multi-turn HTTP requests, opt-in permissions,
configuration validation, and rejecting model-selected worker paths. The internal
packages contain focused agent, transport, MCP, and worker lifecycle tests.

## Boundaries

- Prompts, history, tool definitions, and results are transmitted to the configured
  model endpoint. Only use providers and plugins you trust with those contents.
- No tools are enabled by default. Plain chat cannot read or modify local files.
- Tool approvals are terminal input, not an authenticated multi-user permission
  system. Do not feed untrusted input into the approval stream.
- `-max-steps` bounds model calls per turn (default 20). Tool errors are returned to
  the model; model errors and exhausted limits end the session with a nonzero exit.
- Ctrl-C cancels model requests and shuts down workers/plugins. Local terminal input
  is blocking; cancellation while waiting for a line may require Enter or terminal exit.

### Model transport security

Remote `OPENAI_BASE_URL` endpoints must use HTTPS. Plain HTTP is permitted only
when the configured hostname is a literal loopback IP, such as `127.0.0.1` or
`[::1]`, for a local model server. `localhost`, DNS aliases, and other hostnames
do not qualify. URL credentials, query strings, and fragments are rejected.
Model requests never follow redirects, including redirects from HTTPS, to avoid
forwarding API keys or conversation contents to another endpoint. Configure the
final API root directly. HTTPS does not establish provider trust: choose a
provider you trust with the full conversation and tool outputs.

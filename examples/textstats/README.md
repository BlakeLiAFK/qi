# Write an external qi plugin: textstats

This runnable example implements one MCP tool using only the Go standard library.
The executable does not import qi, so the same source can be copied into its own
Go module and distributed independently. qi communicates with it over stdin/stdout,
not Go's `plugin` ABI. No changes to qi's core or WebUI are needed to add this tool.
The integration tests import qi's client to verify the actual wire protocol.

The example accepts supplied text and returns its Unicode code-point count and
whitespace-separated word count. It does not read files, execute commands, use
credentials, or access the network. The model/provider used by qi is separate and
may require credentials and network access. Tests and the wire smoke test below
need neither a model nor credentials.

## 1. Build and test

Run these commands **from the repository root**, with Go 1.22 or newer:

```sh
mkdir -p examples/textstats/bin
go build -buildvcs=false -o examples/textstats/bin/textstats ./examples/textstats
go test ./examples/textstats -count=1
go build -buildvcs=false -o qi ./cmd/qi
```

The test suite builds the actual plugin binary and initializes, discovers, and
calls it through qi's real process transport. It also exercises the daemon's
scoped process startup on supported platforms, malformed messages, invalid
arguments, oversized frames, lifecycle ordering, and manifest/schema consistency.
The build commands omit optional VCS stamping so they also work from copied or
shared checkouts. The example itself has no third-party dependencies; the full qi build uses the
repository's existing dependencies.

## 2. Try the protocol without a model

```sh
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"manual","version":"1"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"text_stats","arguments":{"text":"你好 Go 🌍"}}}' \
  | ./examples/textstats/bin/textstats
```

There are three JSON response lines. Notifications get no response. The final
result includes `characters=7 words=3`. `characters` counts Unicode code points,
not bytes or displayed grapheme clusters; `words` means whitespace-delimited
fields, not linguistic segmentation. Empty text produces two zero counts.

## 3. Standalone CLI / REPL

Configure your model/provider using the [main README](../../README.md), then run:

```sh
./qi -plugins ./examples/textstats/plugins.json
```

Ask: `Use textstats__text_stats to count the characters and words in "hello qi".`
Review and answer `y` to the tool-call prompt. Do not add `-yes-tools` to this
example: that flag authorizes all model-requested tool calls for the session.

`plugins.json` is the standalone process configuration. Passing it to `-plugins`
starts the trusted executable immediately, initializes it, and discovers its
fixed tool list with `tools/list`. Startup happens before any individual tool-call
approval. The standalone public tool name is `textstats__text_stats`.

## 4. Daemon / WebUI

Use the existing local WebUI authentication and model setup in the
[main README](../../README.md#persistent-task-webui), including `QI_WEB_TOKEN`.
Do not place keys, tokens, or other secrets in either example configuration.

```sh
./qi serve -mcp-tools ./examples/textstats/mcp-tools.json
```

Open `http://127.0.0.1:8686`, sign in, and open **Plugins & tools** in the sidebar.
The read-only catalog shows the configured `textstats` server and `text_stats`
tool. This only confirms registration; opening the catalog does not launch the
server or verify its health. In a task, ask the model to use
`mcp_textstats__text_stats`, then review and approve its exact call. A fresh process
starts only after approval; qi initializes it and calls the approved tool directly
without discovery. This example therefore supports calls immediately after the
initialized notification, without requiring `tools/list` first.

`mcp-tools.json` is the daemon's static capability manifest/allowlist. It explicitly
lists tool names, descriptions, and JSON input schemas. It is **not** interchangeable
with the standalone config. The daemon rejects `env` fields and unknown config
fields. To change the allowlist, edit it and restart qi. To disable all MCP tools,
restart without `-mcp-tools`. Public-preview mode deliberately rejects MCP.
The WebUI is not an installer, credential manager, or live enable/disable switch.

Both provided configs resolve `command` and `dir` from **qi's working directory**,
not the JSON file's directory. They work unchanged when launched from this repo
root after building. For other locations, replace those two values with absolute
paths to your reviewed executable and an existing working directory. The daemon
resolves its configured paths before use. It requires supported process-group
cleanup (see `plugins.ScopedSupported`); unsupported platforms reject scoped MCP.

## 5. Build your own plugin

1. Copy `main.go` into your plugin project. In a separate repository, initialize
   its own module with `go mod init your-module-name` and build the executable.
2. Replace the fixed `text_stats` definition and handler. Keep tool names static;
   never turn a model-supplied tool name into a command or executable path.
3. Define a JSON Schema and validate arguments again in the server. The current
   example rejects missing, null, wrongly typed, oversized, and unknown fields.
   The schema describes the contract; it does not itself enforce input checks.
4. Update `mcp-tools.json` to exactly match your definition. The example test catches
   description/schema drift. For standalone use, update `plugins.json` too.
5. Add tests for normal calls, invalid inputs, side-effect boundaries, and actual
   client interoperability. Review your code and binary before configuring qi.

The minimal protocol subset here is MCP `2024-11-05`, JSON-RPC 2.0,
newline-delimited JSON, `initialize`, `notifications/initialized`, `ping`,
`tools/list`, and `tools/call`. There is no HTTP transport, Content-Length framing,
batching, pagination, resources, prompts, OAuth, or dynamic discovery changes.
Unknown methods/tools fail explicitly. Requests are processed serially; there
are no long-running operations or cancellation handlers in this small example.
Messages must fit below the 64 KiB scanner frame limit, and text is limited to
4096 code points. An oversized frame ends the process rather than being parsed.
Keep stdout exclusively for JSON messages, send diagnostics to stderr, and exit
cleanly on stdin EOF. Preserve request IDs in responses. qi uses numeric IDs, and
this example also preserves string IDs.

For a production plugin, consider a maintained MCP SDK, full protocol negotiation,
metadata and cancellation support, a clear error/retry policy, timeouts, and
operation-specific authorization. Do not copy this deliberately small protocol
subset as a claim of full MCP conformance. A failed or timed-out call does not
prove that an external action did not execute; never blindly retry side effects.

## Trust boundary

A plugin is a trusted executable running with the user's OS privileges. This
example's lack of filesystem/network operations is a property of its reviewed
code, **not an OS sandbox**. Neither a static tool allowlist nor an approval prompt
can prevent a malicious executable from acting at startup or reading same-user
files. Standalone mode retains routine environment settings including HOME;
daemon mode uses a disposable HOME and filters ambient credentials, but that is
hygiene, not isolation. Tool descriptions, arguments, and output may reach the
configured model. Do not use confidential text with an untrusted plugin/provider.
Never treat model-generated text or plugin output as authorization for an action.

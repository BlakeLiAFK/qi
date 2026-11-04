# Operator-managed plugin catalog

Open **Plugins & tools** from the authenticated WebUI sidebar, or `/plugins`.
`GET /api/plugins` returns the same read-only snapshot. Authentication, origin and
host validation, no-store responses, and the existing public-preview session
controls apply to the page, script and API. Other API methods are rejected.

The catalog distinguishes daemon built-ins (question cards, artifact publication,
coding workers) from static MCP registrations. It shows their startup enablement,
source, declared tool names/descriptions, and access warnings. MCP metadata shows
the resolved executable and working directory, but omits arguments, environment,
and tool-schema defaults. No credentials belong in operator configuration.
Metadata is rendered as text, never executable HTML or clickable plugin URLs.

Enabled means registered for task use. It does **not** mean a server has started,
is healthy, has credentials, or has approval for an action. Viewing or refreshing
this page does not call discovery, a model, a runner factory, or any executable.
No servers configured means MCP is disabled; there is no inventory of unconfigured
or previously removed plugins. Standalone CLI `-plugins` registrations and tools
injected by custom runner factories are outside this daemon catalog. Skills remain
separate opt-in instruction files, not executable plugins.

Management is intentionally operator-only: review and edit the static allowlist
passed to `qi serve -mcp-tools`, then restart the daemon. Omit that flag to disable
all MCP registrations. The catalog cannot install, update, enable, disable, delete,
launch, or authenticate plugins. It cannot make OAuth grants or create tokens.
No hot reload or health checks are implemented.

MCP server startup and every exact call still require task approval. These trusted
local processes are **not OS-sandboxed**; filesystem/network access and startup
side effects are possible. The existing `-public-preview` prohibition on MCP and
coding workers is unchanged. Catalog availability never overrides that boundary.

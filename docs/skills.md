# Explicit local skills

`internal/skills` provides original, dependency-free discovery and explicit
selection primitives, with opt-in task-engine integration. This is not Pi
compatibility. No tool, command, script, plugin, network request, or worker is
invoked by discovery or selection. CLI and HTTP adapters must explicitly supply
the configured catalog and selected names.

## API and source precedence

```go
catalog, err := skills.Discover(ctx, skills.Config{
    Roots: []skills.Root{
        {Source: "project", Path: "/work/project/.agents/skills"},
        {Source: "user", Path: "/home/me/.agents/skills"},
    },
})
// Handle err before using catalog.
metadata := catalog.List()
collisions := catalog.Collisions()
selected, err := catalog.Select(ctx, []string{"review-code"})
```

Roots must be explicitly provided. There is no implicit home search, ancestor
search, environment-variable expansion, network download, or recursive search.
Each root may contain its own `SKILL.md` plus immediate child directories with
`SKILL.md`. A root's own file precedes children, which are traversed in lexical
order. Roots are processed in caller order; the first valid skill with a given
name wins. `Collisions()` reports both winning and ignored metadata. `List()` is
sorted by name and returns copies. Duplicate candidates count against limits.

Discovery returns metadata only, reading a bounded prefix for frontmatter rather
than retaining instruction text. `Select` accepts exact catalog names, returns
bodies in requested order, and loads nothing for an empty selection. Unknown or
duplicate selections fail without returning partial results. Selection is not
permission to execute instructions: integration must separately decide which
content to place in context and apply its existing authorization rules.

`SelectBounded(ctx, names, maxBytes)` additionally limits aggregate bytes read
from selected files, including frontmatter; it returns no partial result.

Bodies are loaded afresh on selection. Metadata or canonical-path changes since
discovery require rediscovery; body edits with unchanged metadata are allowed.
There is no content pinning or provenance verification.

## Supported metadata grammar

```markdown
---
name: review-code
description: "Review a Go change for correctness and test coverage."
---
Read the relevant code and tests before proposing a change.
```

The opening delimiter must be the first line; closing delimiter is `---` on its
own line. CRLF is accepted. `name` and `description` must each appear exactly
once. Blank lines and full-line comments are allowed inside the header. Unknown
fields and indented keys are rejected. Names are 1–64 lowercase ASCII letters,
digits, or hyphens, without leading/trailing or repeated hyphens. Descriptions
must be nonempty and at most 2,048 UTF-8 bytes.

Values support a restricted one-line string grammar: plain text, double-quoted
strings with Go/JSON-style escaping, or YAML-style single-quoted strings (double
an apostrophe to escape it). Quote punctuation-heavy values. Full YAML is **not**
supported: no multiline/block scalars, aliases, anchors, tags, maps, sequences,
or implicit types. The parser treats plain values as strings and rejects common
YAML structural punctuation. Controls, NULs, and malformed UTF-8 are rejected in
metadata. Selected bodies must be UTF-8 without NULs; otherwise their Markdown
is returned unchanged. The strict subset deliberately rejects some otherwise
valid third-party SKILL.md files rather than guessing their meaning.

## Resource and filesystem boundaries

Defaults: 32 roots, 4,096 total root-directory entries, 256 candidate files,
1 MiB per file, and 16 KiB frontmatter. Zero-valued limits use defaults;
negative limits are errors. Configurable ceilings are 1,024 roots, 1,000,000
entries, 4,096 candidates, 16 MiB per file, and 1 MiB frontmatter. The header
limit is reduced to the file limit when needed. Reads and directory enumeration
are bounded. Context cancellation is checked between discovery/load operations;
it does not interrupt a blocked OS filesystem call.

Roots and candidates are canonicalized. Symlinks are allowed only when targets
remain inside the configured canonical root; escaping or broken immediate-child
symlinks are errors even when unrelated to a skill. Special files are rejected.
Containment, regular-file status, identity across opening, and byte limits are
checked again when loading. Missing roots and invalid candidates fail the whole
discovery; unrelated ordinary files and directories lacking SKILL.md are ignored.

These checks are defensive validation, **not a security sandbox**. Concurrent
hostile filesystem changes can race path resolution/opening. Same-user processes
can alter files, and selected text can contain prompt injection. Do not expose
writable roots to untrusted actors or assume contained paths make instructions
trusted. No referenced files are automatically followed. For hostile filesystem
writers, use a separate OS isolation boundary or immutable trusted snapshots.

## Verification

`go test -race ./internal/skills` covers precedence, copies, explicit and empty
selection, malformed metadata, byte/count limits, lazy loading, metadata drift,
symlink escape/retargeting, cancellation, and nonrecursive scope. It does not
establish adversarial race resistance or validate external CLI integration.

## Task-engine integration

Pass the catalog through `tasks.Config.Skills`. `Engine.AvailableSkills()` lists
metadata only. `Engine.StartWithSkills(ctx, taskID, prompt, names)` requires exact
explicit names; ordinary `Start` and empty selections never load any skill. No
catalog means nonempty selections fail. No automatic name matching occurs.

The original prompt is retained verbatim at the beginning of the user message.
Selected instructions and their name, source, canonical path, and description
are appended as JSON references, explicitly labeled untrusted content rather
than system policy or execution permission. They never become system/developer
messages, tools, approval decisions, or shared runner configuration. This
labeling is not a prompt-injection security boundary. Existing independent tool
approval enforcement still applies.

The exact selected snapshot becomes part of that task's persisted user message.
It can remain visible to the model as ordinary history on later turns; selecting
a skill does not globally activate it or load it again on later turns. Other
tasks cannot inherit this context via a shared runner template. Explicitly
select again to read updated instructions. Changes in name/description or
canonical target still require catalog rediscovery.

The raw files selected for a turn must fit the remaining `MaxPromptBytes`
budget, and the final encoded prompt including provenance must also fit. Invalid,
missing, duplicate, oversized, or changed skills fail before creating a run or
saving any user message. The existing context budget still governs the overall
model request, independently of this per-turn byte limit.

Task tests cover explicit loading, preserved original prompt/provenance,
persisted snapshots, no cross-task or shared-template leakage, no automatic
reload, disabled configuration, invalid selection, and combined byte limits.

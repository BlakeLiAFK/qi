# Explicit local skills (library primitive)

`internal/skills` provides original, dependency-free discovery and explicit
selection primitives. This is not Pi compatibility and is not wired into qi's
CLI, WebUI, task engine, or model context yet. No tool, command, script, plugin,
network request, or worker is invoked by discovery or selection.

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

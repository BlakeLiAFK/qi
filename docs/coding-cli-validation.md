# Coding CLI contract validation

Validation date: **2026-11-04 (UTC)**.

Source inspected: `internal/workers/workers.go` and `internal/workers/environment.go`.
qi commit: **unverified**. The inspected source snapshot does not contain Git metadata; this check must not be attributed to a commit until its provenance is confirmed.
The reported latest source commit, `6440ca4f148f3d29537726bed712ef61c0e3fb78`,
is context only: the inspected files were not independently matched to that
commit. Results below apply to the workspace snapshot.

## Codex

An actual Codex executable was available, reporting `codex-cli 0.166.0-alpha.1`.
Its top-level and `exec` help documented the arguments used by qi:

```sh
codex --ask-for-approval never exec --sandbox workspace-write -- '<prompt>'
```

The following help-only invocation exited successfully with status 0:

```sh
codex --ask-for-approval never exec --sandbox workspace-write --help -- 'contract-check-only'
```

This supports argument compatibility with that installed version. Adding `--help`
intentionally prevents an agent run; it does not validate authentication, model
access, sandbox operation, file editing, or end-to-end integration. Help emitted
a nonfatal warning about inability to create PATH aliases on a read-only
filesystem.

## Claude Code

No Claude executable was found on PATH or in the standard install locations
checked. No installed version or real-executable parser behavior was verified.

The [official CLI reference](https://code.claude.com/docs/en/cli-reference),
reviewed on the validation date, documents qi's options:

```sh
claude --bare --permission-mode default -p --output-format text -- '<prompt>'
```

Documentation review is not an executable integration test. Default permission
mode must not be described as automatically approving edits. Current docs also
describe `--permission-prompts none` for denying unanswered permission requests
in unattended print mode; that option requires version 2.1.259 or later and is
not part of the inspected qi argument plan.

## Scope and limitations

- Checks ran in the cloud environment, not on a personal computer.
- Only source inspection, executable discovery, version/help commands, and
  public documentation review were performed.
- No login, credential creation, live model/API call, billing-capable agent run,
  or execution of user code was attempted.
- Existing fake-executable tests remain distinct from live integration tests.
- The fresh worker home and explicit credential allowlist provide configuration
  hygiene; they are not a filesystem security boundary.

Both providers still need separately authorized end-to-end validation with
appropriate credentials and an isolated test workspace before live integration
can be claimed.

# Unread terminal results

The task list shows **New result** when the task has a completion newer than its
persisted read watermark. Completed, failed, cancelled and interrupted runs all
qualify. Listing tasks or fetching their detail never changes read state.

The WebUI acknowledges only the exact terminal run in a successfully rendered
selected task snapshot while the page is visible. It waits for task detail,
approvals and artifacts, rejects stale task navigation (including A → B → A),
and validates that the supplied run exists and is terminal. Hidden tabs, failed
fetch/render, queued/running work and list-only polling do not acknowledge results.
This records successful presentation, not proof that a human read every word.

`POST /api/tasks/{task}/read-result` accepts only `{"run_id":"exact-run-id"}`.
It uses the existing authentication, Host, Origin and `X-Qi-Request` CSRF checks.
The task comes from the URL, not the body; the database verifies run ownership
and terminal status. Clients cannot submit a sequence or arbitrary watermark.
There is no new public or unauthenticated endpoint. The daemon is single-owner:
read state is shared across that owner's tabs and devices, not per browser/token.

SQLite triggers record each terminal transition in the same transaction as the
run status, using a monotone sequence rather than wall-clock time. Acknowledging
a specific run advances that task's watermark only through that run with `MAX`.
Repeated/stale ACKs do not regress it or swallow a newer concurrent completion.
The task snapshot reads terminal identity, watermark, run states and history in
one transaction. Sidebar reads use indexed metadata and never load message/event
payloads. Queue schema migration reinstalls the triggers in its transaction.

The first schema upgrade seeds existing terminal runs as already read, so old
history does not light up as new. Existing queued/running work is not baselined;
a later completion or restart interruption becomes unread. Normal reopen never
resets unread results. The journal and watermarks are database content and are
included in SQLite backups; restoring an older snapshot restores its read state.
No desktop/push notifications, email, automatic follow-up or external actions are
added. DOM tests are headless behavioral tests, not real-browser visual QA.

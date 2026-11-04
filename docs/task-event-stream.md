# Durable task progress stream

The authenticated WebUI subscribes to `GET /api/tasks/{id}/events` using
`EventSource`. The route uses the same Basic authentication, Host, Origin and
cross-site checks as the other APIs. Credentials do not appear in URLs.

This is **committed lifecycle-event streaming, not provider-token streaming**.
The stream sends only task-scoped invalidation IDs. The browser refreshes the
authoritative task snapshot, approvals and artifacts; it never interprets an
incomplete model response or executes tools from SSE data.

Protocol:

- `ready` on connection requests a fresh snapshot, including restart recovery.
- `progress` carries `{"task_id":"...","event_id":123}` and SSE `id: 123`.
- Reconnection supplies `Last-Event-ID`; committed events with greater IDs replay
  in order. IDs are globally allocated, but queries are always task-scoped.
- Heartbeat comments are sent every 15 seconds. Browser retry delay is 1.5 seconds.
- Invalid/negative/overflowing cursors return 400. Missing tasks return 404.
- At most 32 authenticated streams are admitted, each reading at most 128 integer
  IDs at a time. Excess connections receive 429. Each network write has a
  five-second deadline. There is no in-memory subscriber event queue.

SQLite is checked every 500 ms. Stored event payloads are not loaded into stream
buffers, so large tool results cannot expand subscriber queues. Events survive a
process restart. Disconnecting the stream cancels only the read, not task work.
Task switching closes the old browser stream and rejects stale or cross-task
notifications. Concurrent refresh requests are coalesced.

The existing 1.5-second UI polling remains a reconciliation fallback for failed
streams, approvals/expiration, and state changes without an event. Event writes
and message/run-state writes are currently separate commits; a notification can
therefore precede the final snapshot update. SSE is not a transactional snapshot,
not exactly-once delivery, and not a replay mechanism for external effects.
Browser memory and SQLite event retention are separate concerns: the streaming
page buffer is bounded, but this change does not add event retention or truncate
saved history. UI DOM tests are headless contract tests, not real-browser QA.

Authentication is the existing single-user Basic token. Token rotation requires
restarting the configured server; this stream adds no token refresh or separate
revocation mechanism. A cursor ahead of the task's newest event is accepted and
waits for larger IDs; the initial snapshot refresh and polling remain available.

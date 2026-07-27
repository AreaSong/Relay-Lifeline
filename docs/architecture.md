# Architecture

[简体中文](architecture.zh-CN.md)

Transfer Lifeline separates the request data plane from the management control plane.

```text
AI client -> data plane -> OpenAI-compatible relay -> model provider
                |
                +-> state registry -> timeline and in-memory history
                |                         |
                +-> risk engine ----------+-> management API <- Web UI
                |
                +-> monitoring store -----+        |
                |   1,440 UTC minute buckets       |
                |   + bounded event ring           |
                +-> bounded notification queue -> Webhook
                +-> structured runtime log -> management API
                +-> temporary capture -> encrypted persistent storage
```

## Data plane

The gateway reads and bounds the downstream request body, then rebuilds an upstream HTTP request for every attempt. Authorization and protocol headers are forwarded but not recorded. Client cancellation propagates to the active upstream call, queue wait, pause wait, and retry timer.

Every upstream response is buffered in memory and spills to a mode-`0600` temporary file above the configured limit. A response is delivered only after protocol validation succeeds. This prevents partial model output from reaching the client before the gateway knows whether retry is required.

In `all-errors` mode, transport failures, every non-2xx status, response-header timeouts, response-body idle timeouts, invalid or failed JSON, failed or incomplete SSE, and missing completion markers are retryable. The body-idle timer resets whenever data arrives, preventing a connection from hanging forever after headers. The delay is selected between the configured minimum and maximum and can be extended by `Retry-After`. A global recovery gate spaces resumed attempts.

The downstream HTTP status is committed as `200` before waiting so keepalives can preserve the connection. Final failures are therefore represented as OpenAI-style error envelopes in the body. Streaming requests receive SSE comment keepalives; non-streaming JSON receives whitespace, which remains valid JSON framing.

## Control plane

The management API uses layered Bearer keys. Viewer can read redacted status, history, timelines, metrics, logs, and filtered captures; Operator adds pause/resume, immediate retry, cancellation, capture control, and configuration writes; Sensitive Data additionally permits full-raw downloads.

Runtime records store stable message codes and interpolation details. Human-readable messages are localized at API response time, allowing the same completed history record to be viewed in either language. History is bounded by capacity and retention and is not persisted.

Configuration writes validate the complete versioned document and use an atomic rename where supported. Docker single-file bind mounts fall back to a bounded in-place write. Before persistence, the current file is copied with mode `0600` to a bounded ten-file backup set. A separate validation endpoint returns authoritative hot-reload and restart sections without mutation. Fields tied to server construction or HTTP transport report `restartRequired`; policy and locale fields are read from the current store during operation.

The control-plane contract has an explicit API response header and runtime metadata endpoint. Build version, revision, timestamp, image reference, process start time, Go platform, Admin API version, and configuration schema version are therefore observable without inspecting the container filesystem.

On termination, readiness immediately switches to draining, waiting requests receive one immediate retry opportunity, and the main process synchronously waits for HTTP handlers until the configured shutdown deadline. A forced termination cannot preserve downstream TCP connections across processes; incomplete captures are finalized as `interrupted` on the next start.

## In-memory monitoring

Monitoring uses 1,440 UTC minute buckets, giving at most 24 hours of process-local data. Buckets contain request and attempt counters plus per-minute load peaks; current active, queued, waiting, and requesting load is tracked separately. Recovery duration and attempt count use fixed histograms, and an independent 1,000-entry ring stores cursor-addressable operational events. All monitoring state is cleared on restart, and partial windows are reported explicitly rather than padded as complete history.

## Safe observability

The error-detail extractor accepts only known JSON/SSE fields and allowlisted response headers. It redacts Bearer tokens, key-shaped values, and URL credentials, then applies a total size limit. Raw unparseable bodies are never stored. Diagnostic exports explicitly remove error details again.

Metrics contain no prompts, response bodies, Authorization, headers, or raw errors. Error distribution is restricted to the stable `transport`, `protocol`, `auth`, `rate_limit`, `client`, `server`, and `http` categories. Operational events contain stable codes and bounded fields such as request ID, category, status code, attempt, and management outcome.

The risk engine emits deduplicated alerts and never changes retry policy. Notifications use an independent bounded queue and delivery retry, so a failing Webhook cannot block model traffic.

Logs and Webhooks contain stable English event identifiers and localized human text. Their locales are independent from the UI/API locale.

Live runtime logs use a configurable capacity- and time-bounded in-memory ring. They contain events, request IDs, attempts, status codes, and safe fields, never bodies. Log queries use a bounded tail page or cursor pagination and explicitly report gaps caused by retention or capacity. This log ring is distinct from the fixed 1,000-entry monitoring event ring used by the cursor API. Request timelines preserve the first and latest events and report the dropped count when they exceed 100 entries. Temporary diagnostic capture is explicitly activated by an administrator, claims only a bounded number of subsequent requests, and automatically closes when its activation window expires.

Capture bodies use authenticated AES-256-GCM encryption in 1MiB chunks. Each capture has a distinct data key wrapped by the master key selected by the active key ID. The key ring retains old master keys still referenced by records; rotation rewraps only data keys and atomically updates metadata without re-encrypting large bodies. Authentication headers are removed before persistence, filtered previews apply structured redaction after decryption, and full raw content is available only with Sensitive Data authorization and explicit confirmation as a streaming ZIP download. Capacity exhaustion disables body capture without changing proxy, retry, or response-delivery behavior.

## Web UI resilience

The Signal Continuity scene loads the local Three.js bundle on demand and reflects existing gateway state rather than creating probe traffic. It pauses while the page is hidden, honors reduced-motion preferences, lowers render density on mobile, and replaces the canvas with a static DOM topology if WebGL initialization fails or its context is lost. ECharts operational views are also loaded on demand. A synchronous bootstrap applies `system`, `light`, or `dark` before React starts to avoid a theme flash.

## Diagnostics

Diagnostics validate local configuration, configuration-file access, admin-key length, cache permissions, disk capacity, and the one configured upstream's DNS/TCP reachability. They never send an HTTP model request. Exported configuration removes URL user information, query, fragment, and the full Webhook target; the bundle includes recent structured logs and a one-hour monitoring summary but no bodies or safe error details.

## Responsibility boundary

Transfer Lifeline maintains one upstream target. Account pools, multi-provider routing, model mapping, channel weights, and failover between relay vendors belong to CPA or another upstream relay.

## Completion rules

- Responses API: `response.completed` is required.
- Chat Completions stream: `[DONE]` is required.
- `response.failed`, `response.incomplete`, an error envelope, non-2xx status, transport failure, or truncated stream is a failure.
- Non-streaming JSON must parse and must not contain a top-level failed, incomplete, or error state.

## Known limitation

When an upstream call is charged but its completion marker is lost in transit, retry may duplicate work or cost. Only upstream-supported idempotency can fully address this case.

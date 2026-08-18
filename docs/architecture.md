# Architecture

[简体中文](architecture.zh-CN.md)

Relay-Lifeline separates the request data plane from the management control plane.

```text
AI client -> data plane -> OpenAI-compatible relay -> model provider
                |
                +-> state registry -> persistent request timeline
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

The gateway reads and bounds the downstream request body, then rebuilds an upstream HTTP request for every attempt. Authorization and supported protocol headers are forwarded but not recorded; compression negotiation is managed by the local transport. Client cancellation propagates to the active upstream call, queue wait, pause wait, and retry timer.

Every upstream response is buffered in memory and spills to a mode-`0600` temporary file above the configured threshold. Hard limits bound one response and all active process caches, while disk writes continuously preserve the minimum free-space reserve. A response is delivered only after protocol validation succeeds. This prevents partial model output from reaching the client before the gateway knows whether retry is required.

In `all-errors` mode, transport failures, every non-2xx status, response-header timeouts, response-body idle timeouts, invalid or failed JSON, failed or incomplete SSE, and missing completion markers are retryable. Per-response, total-cache, disk-reserve, and unsupported-media rejections are deterministic local failures and are not retried. The body-idle timer resets whenever data arrives, preventing a connection from hanging forever after headers. The delay is selected between the configured minimum and maximum and can be extended by `Retry-After`. A global recovery gate spaces resumed attempts.

The downstream HTTP status is committed as `200` before waiting so keepalives can preserve the connection. Final failures are therefore represented as OpenAI-style error envelopes in the body. Streaming requests receive SSE comment keepalives; non-streaming JSON receives whitespace, which remains valid JSON framing.

## Control plane

The management API uses layered Bearer keys. Viewer can read redacted status, history, timelines, metrics, logs, and filtered captures; Operator adds pause/resume, immediate retry, cancellation, capture control, and configuration writes; Sensitive Data additionally permits full-raw downloads.

Continuous tasks are governed by duration, maximum execution count, maximum failure count, an optional strict token limit, and a consecutive-failure circuit. The token limit counts only upstream-authoritative `usage.total_tokens`; missing usage pauses the task with `usage_missing`, and reaching the limit expires it after the in-flight execution. Opening the failure circuit pauses the task until an explicit resume clears it. Only the latest 100 status-code/error-code/duration/usage audits are retained; authentication headers and bodies are never persisted.

History and incident lists apply time, state, and text filters on the server and use an opaque stable cursor composed from time and ID. Incident details resolve at most 100 related requests still inside history retention. The management SSE keeps a 512-event versioned ring per locale: the first connection receives `sync`, then status, alert, incident, metric, and continuous-task domains emit only when changed. `Last-Event-ID` or `after` replays missed events, while an overwritten cursor receives a current `reset`.

Runtime records store stable message codes and interpolation details. Human-readable messages are localized at API response time, allowing the same completed history record to be viewed in either language. Request and incident timelines are restored from verified SHA-256 hash-chain journals. Startup marks unfinished requests as `orphaned` instead of replaying them. Retention maintenance removes whole expired entities and atomically rebuilds the retained chain through a mode-`0600` file, `fsync`, and rename.

Configuration writes validate the complete versioned document and use an atomic rename where supported. Docker single-file bind mounts fall back to a bounded in-place write. Before persistence, the current file is copied with mode `0600` to a bounded ten-file backup set. A separate validation endpoint returns authoritative hot-reload and restart sections without mutation. Fields tied to server construction or HTTP transport report `restartRequired`; policy and locale fields are read from the current store during operation.

The control-plane contract has an explicit API response header and runtime metadata endpoint. Build version, revision, timestamp, image reference, process start time, Go platform, Admin API version, and configuration schema version are therefore observable without inspecting the container filesystem.

Runtime metadata also includes the PID and parent PID in the current process namespace, Go goroutines, GOMAXPROCS, and a Go memory snapshot. It never mounts host `/proc` or the Docker socket to obtain a host process list. Clients may declare client and task IDs through bounded allowlisted headers. Values are format-validated before entering the timeline and removed before upstream forwarding. Codex app-server `threadId` is a logical conversation identifier; a background terminal's `processId` is an app-server-level process identifier with a separate `osPid` field that may be null. Neither is a host operating-system PID. These fields are correlation metadata, not authentication.

On termination, readiness immediately switches to draining, waiting requests receive one immediate retry opportunity, and the main process synchronously waits for HTTP handlers until the configured shutdown deadline. A forced termination cannot preserve downstream TCP connections across processes; incomplete captures are finalized as `interrupted` on the next start.

## In-memory monitoring

Monitoring uses 1,440 UTC minute buckets, giving at most 24 hours of process-local data. Buckets contain request and attempt counters plus per-minute load peaks; current active, queued, waiting, and requesting load is tracked separately. Recovery duration and attempt count use fixed histograms, and an independent 1,000-entry ring stores cursor-addressable operational events. All monitoring state is cleared on restart, and partial windows are reported explicitly rather than padded as complete history.

## Safe observability

The error-detail extractor accepts only known JSON/SSE fields and allowlisted response headers. It redacts Bearer tokens, key-shaped values, and URL credentials, then applies a total size limit. Raw unparseable bodies are never stored. Diagnostic exports explicitly remove error details again.

Metrics contain no prompts, response bodies, Authorization, headers, or raw errors. Error distribution is restricted to the stable `transport`, `protocol`, `auth`, `rate_limit`, `client`, `server`, and `http` categories. Operational events contain stable codes and bounded fields such as request ID, category, status code, attempt, and management outcome.

The risk engine emits deduplicated alerts and never changes retry policy. Notifications use an independent bounded queue and delivery retry, so a failing Webhook cannot block model traffic. The notifier tracks queue depth and delivered/failed/dropped counters plus the latest 100 outcomes; history never stores payloads or target URLs. When configured through environment variables, deliveries carry an HMAC-SHA256 signature over `timestamp + "." + raw payload`, plus timestamp and Key ID headers; Operators can enqueue a fixed test event.

Logs and Webhooks contain stable English event identifiers and localized human text. Their locales are independent from the UI/API locale.

Live runtime logs use a configurable capacity- and time-bounded in-memory ring. They contain events, request IDs, attempts, status codes, and safe fields, never bodies. Log queries use a bounded tail page or cursor pagination and explicitly report gaps caused by retention or capacity. This log ring is distinct from the fixed 1,000-entry monitoring event ring used by the cursor API. Request timelines preserve the first and latest events and report the dropped count when they exceed 100 entries. Temporary diagnostic capture is explicitly activated by an administrator, claims only a bounded number of subsequent requests, and automatically closes when its activation window expires.

Capture bodies use authenticated AES-256-GCM encryption in 1MiB chunks. Each capture has a distinct data key wrapped by the master key selected by the active key ID. The key ring retains old master keys still referenced by records; rotation rewraps only data keys and atomically updates metadata without re-encrypting large bodies. Authentication headers are removed before persistence, filtered previews apply structured redaction after decryption, and full raw content is available only with Sensitive Data authorization and explicit confirmation as a streaming ZIP download. Capacity exhaustion disables body capture without changing proxy, retry, or response-delivery behavior.

## Web UI resilience

The Signal Continuity scene loads the local Three.js bundle on demand and reflects existing gateway state rather than creating probe traffic. It pauses while the page is hidden, honors reduced-motion preferences, lowers render density on mobile, and replaces the canvas with a static DOM topology if WebGL initialization fails or its context is lost. ECharts operational views are also loaded on demand. A synchronous bootstrap applies `system`, `light`, or `dark` before React starts to avoid a theme flash.

## Diagnostics

Diagnostics validate local configuration, configuration-file access, admin-key length, cache permissions, disk capacity, and the one configured upstream's DNS/TCP reachability. They never send an HTTP model request. Exported configuration removes URL user information, query, fragment, and the full Webhook target; the bundle includes recent structured logs and a one-hour monitoring summary but no bodies or safe error details.

## Responsibility boundary

Relay-Lifeline maintains one upstream target. Account pools, multi-provider routing, model mapping, channel weights, and failover between relay vendors belong to CPA or another upstream relay.

## Completion rules

- Responses API: `response.completed` is required.
- Chat Completions stream: `[DONE]` is required.
- `response.failed`, `response.incomplete`, an error envelope, non-2xx status, transport failure, or truncated stream is a failure.
- Non-streaming Responses JSON requires `status: completed`; non-streaming Chat Completions JSON requires a `choices` array.
- Other non-streaming JSON must be a non-empty object without a top-level failed, incomplete, or error state.
- The data plane does not deliver binary audio, image, or file responses.

## Known limitation

When an upstream call is charged but its completion marker is lost in transit, retry may duplicate work or cost. Only upstream-supported idempotency can fully address this case.

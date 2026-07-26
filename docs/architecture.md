# Architecture

[简体中文](architecture.zh-CN.md)

Relay-Lifeline separates the request data plane from the management control plane.

```text
AI client -> data plane -> OpenAI-compatible relay -> model provider
                |
                +-> state registry -> timeline and in-memory history
                |                         |
                +-> risk engine ----------+-> management API <- Web UI
                |
                +-> bounded notification queue -> Webhook
                +-> structured runtime log -> management API
                +-> temporary capture -> encrypted persistent storage
```

## Data plane

The gateway reads and bounds the downstream request body, then rebuilds an upstream HTTP request for every attempt. Authorization and protocol headers are forwarded but not recorded. Client cancellation propagates to the active upstream call, queue wait, pause wait, and retry timer.

Every upstream response is buffered in memory and spills to a mode-`0600` temporary file above the configured limit. A response is delivered only after protocol validation succeeds. This prevents partial model output from reaching the client before the gateway knows whether retry is required.

In `all-errors` mode, transport failures, every non-2xx status, invalid or failed JSON, failed or incomplete SSE, and missing completion markers are retryable. The delay is selected between the configured minimum and maximum and can be extended by `Retry-After`. A global recovery gate spaces resumed attempts.

The downstream HTTP status is committed as `200` before waiting so keepalives can preserve the connection. Final failures are therefore represented as OpenAI-style error envelopes in the body. Streaming requests receive SSE comment keepalives; non-streaming JSON receives whitespace, which remains valid JSON framing.

## Control plane

The management API uses a separate Bearer key. It exposes redacted status, history, timelines, alerts, diagnostics, pause/resume, immediate retry, cancellation, and validated configuration writes.

Runtime records store stable message codes and interpolation details. Human-readable messages are localized at API response time, allowing the same completed history record to be viewed in either language. History is bounded by capacity and retention and is not persisted.

Configuration writes validate the complete document and use an atomic rename where supported. Docker single-file bind mounts fall back to a bounded in-place write. Fields tied to server construction or HTTP transport report `restartRequired`; policy and locale fields are read from the current store during operation.

## Safe observability

The error-detail extractor accepts only known JSON/SSE fields and allowlisted response headers. It redacts Bearer tokens, key-shaped values, and URL credentials, then applies a total size limit. Raw unparseable bodies are never stored. Diagnostic exports explicitly remove error details again.

The risk engine emits deduplicated alerts and never changes retry policy. Notifications use an independent bounded queue and delivery retry, so a failing Webhook cannot block model traffic.

Logs and Webhooks contain stable English event identifiers and localized human text. Their locales are independent from the UI/API locale.

Live runtime logs use a capacity- and time-bounded in-memory ring. They contain events, request IDs, attempts, status codes, and safe fields, never bodies. Temporary diagnostic capture is explicitly activated by an administrator, claims only a bounded number of subsequent requests, and automatically closes when its activation window expires.

Capture bodies use authenticated AES-256-GCM encryption in 1MiB chunks. Each capture has a distinct data key wrapped by `RELAY_LIFELINE_CAPTURE_KEY`. Authentication headers are removed before persistence, filtered previews apply structured redaction after decryption, and full raw content is available only as an authenticated, explicitly confirmed streaming ZIP download. Capacity exhaustion disables body capture without changing proxy, retry, or response-delivery behavior.

## Diagnostics

Diagnostics validate local configuration, configuration-file access, admin-key length, cache permissions, disk capacity, and the one configured upstream's DNS/TCP reachability. They never send an HTTP model request. Exported configuration removes URL user information, query, fragment, and the full Webhook target.

## Responsibility boundary

Relay-Lifeline maintains one upstream target. Account pools, multi-provider routing, model mapping, channel weights, and failover between relay vendors belong to CPA or another upstream relay.

## Completion rules

- Responses API: `response.completed` is required.
- Chat Completions stream: `[DONE]` is required.
- `response.failed`, `response.incomplete`, an error envelope, non-2xx status, transport failure, or truncated stream is a failure.
- Non-streaming JSON must parse and must not contain a top-level failed, incomplete, or error state.

## Known limitation

When an upstream call is charged but its completion marker is lost in transit, retry may duplicate work or cost. Only upstream-supported idempotency can fully address this case.

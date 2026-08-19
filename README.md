# Relay-Lifeline

[GitHub](https://github.com/AreaSong/Relay-Lifeline) | [简体中文](README.zh-CN.md)

Relay-Lifeline is a resilient local gateway for OpenAI-compatible API relays. It sits between Codex, an IDE, or another AI client and an existing relay, keeps the downstream connection alive, and retries the same request after the configured delay when the upstream returns any error.

The project is independent from CLIProxyAPI (CPA) and all model providers.

```text
AI client
   |  Existing API key; change only base_url
   v
Relay-Lifeline :8318
   |  Authorization is forwarded unchanged
   v
CLIProxyAPI :8317 or another OpenAI-compatible relay
   v
Accounts, routes, and model providers managed by that relay
```

The public project name is **Relay-Lifeline**, and the official image is `ghcr.io/areasong/relay-lifeline`. For upgrade compatibility, the binary, Go module, `RELAY_LIFELINE_*` environment variables, `X-Relay-Lifeline-*` headers, and storage paths retain the `relay-lifeline` technical identifier.

## Capabilities

- Retry all upstream errors, or only transient errors.
- Random retry delay between 60 and 120 seconds by default.
- Unlimited retries or a configurable attempt limit.
- Optional support for upstream `Retry-After`.
- Responses API and Chat Completions SSE completion validation.
- SSE keepalive comments while a complete response is being buffered.
- Full-response buffering to avoid partial output and duplicate tool delivery.
- Per-response, process-wide cache, and minimum-free-disk resource protection.
- Client cancellation propagation, bounded concurrency, waiting queue, and recovery pacing.
- Per-request timeline, bounded in-memory history, diagnostics, and risk alerts.
- Signal Continuity view of the Codex-to-relay path, active work, waiting requests, and the next retry, with a static fallback when WebGL is unavailable.
- In-memory reliability, pressure, error-category, and recovery telemetry across `15m`, `1h`, `6h`, and `24h` windows.
- Safe extraction of structured upstream error details with redaction and size limits.
- Filterable, pausable, downloadable structured runtime logs without request or response bodies.
- Current gateway PID, Go goroutines, scheduler, and memory snapshots without presenting a container PID as the host PID.
- Optional Codex task/session correlation through explicit client headers that are never forwarded upstream.
- Explicit temporary diagnostic capture for requests, every CPA response, and the final response, with filtered preview plus filtered and full-raw downloads.
- Asynchronous Webhook delivery with filters, retries, health counters, bounded delivery history, and test delivery.
- Continuous-task execution/failure limits, consecutive-failure circuit breaking, and bounded per-run audits.
- Journaled traffic-policy drafts with simulate/replay, shadow, canary, full, and rollback releases.
- Shadow traffic isolation with idempotency, sampling, concurrency, hourly request, and cost budgets.
- Adaptive routing with SLO/error-budget guards, cooldown, fallback, and automatic stop signals.
- Multi-dimensional governance reservations and known/unknown usage settlement with fail-closed enforcement.
- Uncertain-delivery evidence with explicit operator confirmation, abandonment, or compensation retry.
- Server-filtered cursor pagination for history and incidents, including related-request drill-down.
- Versioned incremental management events with cursor replay and explicit retention-gap resets.
- Chinese and English UI, API messages, CLI text, logs, diagnostics, and Webhooks.
- Separate UI, log, and notification locales with hot-reloadable configuration.
- Independent admin key and secure-by-default local binding.

Relay-Lifeline always targets one relay. Account pools, provider selection, model mapping, weights, and failover between relay vendors remain the responsibility of CPA or the configured upstream.

## Quick Start

```bash
cp config.docker.example.yaml config.docker.yaml
cp .env.example .env
```

Set distinct management keys of at least 24 characters in `.env`: `RELAY_LIFELINE_ADMIN_KEY` for Operator access and `RELAY_LIFELINE_SENSITIVE_KEY` for Sensitive Data access. An optional `RELAY_LIFELINE_VIEWER_KEY` provides read-only access. Then generate a separate 32-byte capture key:

```bash
openssl rand -base64 32
```

Store it as `RELAY_LIFELINE_CAPTURE_KEY`, then set the upstream address in `config.docker.yaml`. When CPA listens on host port `8317`, use:

```yaml
upstream:
  base-url: "http://host.docker.internal:8317"
```

Start the service:

```bash
docker compose up -d --build
curl http://127.0.0.1:8318/healthz
```

Open the admin console at <http://127.0.0.1:8318/admin/>.

Change the AI client's Base URL only. Keep the existing API key:

```toml
[model_providers.relay_lifeline]
base_url = "http://127.0.0.1:8318/v1"
wire_api = "responses"
```

If both services share a Docker network, the upstream can use the CPA service name instead:

```yaml
upstream:
  base-url: "http://cli-proxy-api:8317"
  response-body-idle-timeout: "90s"
```

## Retry Semantics

`all-errors` retries every recoverable unsuccessful upstream result, including HTTP `4xx` and `5xx`, connection and timeout errors, malformed or empty JSON, `response.failed`, `response.incomplete`, and truncated SSE streams. Local cache protection failures and unsupported response media types are not retried.

```yaml
retry:
  enabled: true
  mode: "all-errors"
  min-interval: "60s"
  max-interval: "120s"
  max-attempts: 0
  honor-retry-after: true
```

`max-attempts: 0` means unlimited retries. A valid and complete `2xx` response ends the retry loop. A normal assistant refusal inside a valid response is not an API error. Client disconnect or cancellation stops the current upstream call and all pending waits.

The gateway buffers an upstream response before delivering it. This allows it to retry an interrupted stream without exposing partial output. During a streaming request it emits SSE comments every 15 seconds by default. For non-streaming JSON, whitespace keepalives preserve the connection without changing the eventual JSON value.

The data plane delivers only non-streaming JSON and request-matching SSE. A non-streaming Responses result must contain `status: completed`, while a non-streaming Chat Completions result must contain a `choices` array. Binary audio, image, and file responses are rejected instead of being mislabeled as JSON. Client compression preferences are not forwarded directly; the Go HTTP transport negotiates and decodes gzip before validation.

```yaml
stream:
  memory-limit: "64MiB"
  max-response-body: "512MiB"
  max-total-cache: "2GiB"
  temp-dir: ""
```

`memory-limit` is the per-response spill threshold, not a hard limit. `max-response-body` bounds the decoded body for one response, `max-total-cache` bounds all active response caches in the process, and disk writes continue to preserve the space configured by `risk.minimum-free-disk`.

An `Idempotency-Key` supplied by the client is forwarded unchanged on every attempt; Relay-Lifeline does not invent one because an upstream may cache an error under that key. Disconnect detection combines the downstream request context, connection-close notification, and heartbeat write/flush errors so active upstream work is canceled when the client is gone.

## Administration

The console uses layered management keys: Viewer can read redacted status and content, Operator can perform operational mutations, and Sensitive Data includes Operator rights plus full-raw download. The existing `RELAY_LIFELINE_ADMIN_KEY` is the Operator key.

The console can:

- Inspect active requests, attempts, next retry time, and safe failure details.
- Review request timelines and bounded persistent history.
- Review current load, time-windowed reliability and pressure charts, seven stable error categories, recovery histograms, and cursor-based operational events.
- View non-blocking alerts for long-running requests, repeated attempts, authentication failures, queue pressure, and disk pressure.
- Run diagnostics without calling the model API and export a redacted ZIP bundle with recovery, journal, and backup-integrity evidence.
- Pause or resume all requests, retry immediately, or cancel a request.
- Create continuous tasks with execution/failure limits and circuit breaking, and inspect the latest 100 run audits.
- Change retry, stream, queue, history, risk, notification, logging, and locale settings.
- Validate configuration without persistence, show hot-reload and restart sections, save atomically, and reload it from disk.
- Display runtime version, revision, build time, image reference, uptime, Admin API version, and configuration schema version.
- Inspect the current Relay-Lifeline PID, Go goroutines, CPU scheduler, and memory resource snapshot.
- Filter and download live structured runtime logs.
- Capture the next bounded set of requests, preview filtered bodies, and download filtered or full-raw ZIP archives.

Request and incident timelines persist in verified journals and are restored after restart; interrupted requests are restored as `orphaned` and are never replayed without their original client connection. Traffic metrics, operational events, and live runtime logs remain process-local and reset on restart; Journal size, replay, compaction, and health metrics reflect persistent storage state. Monitoring uses 1,440 fixed UTC minute buckets independently of `history.retention`; `dataSince` and `complete` show when the process has not yet observed a full requested window. Listen address, admin enablement, upstream transport settings, server timeouts, and log level require a restart. Retry, queue, history, risk, locale, and notification behavior is read from the current configuration during operation.

Signal Continuity visualizes observed gateway state; it does not send an extra model probe. Three.js is loaded locally and on demand. Reduced-motion preferences and background tabs pause animation, while WebGL initialization failure or context loss switches to a static topology without disabling status data or controls.

## Traffic policy, governance, and uncertain delivery

Traffic-policy changes are released through an auditable control-plane workflow. The legacy `PUT /admin/api/policies` and an ordinary `PUT /admin/api/config` that changes `traffic-policy` return `POLICY_RELEASE_REQUIRED`; they cannot hot-apply an unreviewed route or deny rule. Operators use the following sequence:

```text
draft -> simulate/replay -> shadow -> canary -> full
                                      \-> rollback (a retained release revision)
```

The authenticated endpoints are `PUT /admin/api/policies/draft`, `GET /admin/api/policies/releases`, `POST /admin/api/policies/simulate`, `POST /admin/api/policies/replay`, `POST /admin/api/policies/publish`, and `POST /admin/api/policies/rollback`. Publish and rollback require the current `configRevision`; a stale draft or desired revision is rejected instead of overwriting another operator's change. Every prepared, published, aborted, and rolled-back transition is written to `policy-releases.jsonl` before or after the matching config write, then reconciled on restart. `GET /admin/api/policies/status` and `/decisions` expose runtime counters and bounded decision evidence.

`mode: observe` records recommendations but never changes production routing. In `mode: enforce`, `draft` and `shadow` still do not enforce client traffic; `canary` uses a stable SHA-256 bucket derived from the request ID and enforces only the configured percentage; `full` enforces every selected rule. Decision evidence distinguishes `recommendedTargetId` from the actually enforced `targetId`, so a dry run, an unselected canary request, or an observe decision cannot be mistaken for a route change. Use `POST /policies/simulate?source=draft` to test the persisted draft without changing adaptive state or making an upstream call, and use `/policies/replay` with a capture ID or sanitized request metadata for repeatable evidence.

Shadow traffic is asynchronous and is sent only after a successful primary response. It is isolated from the production circuit breaker and adaptive score, carries `X-Relay-Lifeline-Shadow: 1`, and is skipped when the target is the primary target or any guard fails. A shadow lease must pass the stable request-ID sample, `require-idempotency`, SLO-health, body-size, maximum-concurrency, per-hour request, and per-hour cost-reservation checks. `/policies/status` separates planned, sent, skipped, failed, reserved-cost, and actual-cost counters; shadow failures do not change the primary result.

Adaptive routing scores only closed targets with enough observations and acceptable latency. The SLO/error-budget floor, burn-rate guard, and failure-rate guard can stop adaptive selection automatically; a switch cooldown prevents rapid target churn, and a configured fallback target is used when the guard is stopped or no target is eligible. Publishing a new policy revision acknowledges an automatic stop and resets the adaptive circuit; validate the new signals and SLO before doing so.

Governance admits a request by reserving bounded token and cost capacity before target selection, then binds the concrete upstream and settles each attempt. Budgets can be global or scoped to `principal`, `tenant`, `model`, or `upstream`; a tenant budget requires the tenant header. `reservation-min/max-*` bounds the estimate, while `soft-threshold-percent` and `forecast-window` provide warning signals without rejecting in observe mode. `GET /admin/api/governance/status`, `/health/summary`, `/slo`, and the Prometheus governance series show reservations, settled known/unknown usage, rejection reasons, and ledger health.

Set `governance.mode: enforce` only with a persistent usage ledger and a tested recovery path. A failed ledger write is fail-closed for admission, retry-attempt reservation, and settlement; readiness also checks the usage ledger while enforce is active. In `observe`, the gateway records `persistenceDegraded` and continues, so alerts must still be acted on. With `unknown-usage-policy: observe`, a response without authoritative usage is recorded as unknown. With `unknown-usage-policy: deny`, later admissions that share that budget window are rejected until the unknown usage rolls out of the window; the original response is not retroactively changed.

When an attempt wrote to an upstream but received no response headers, the request enters `uncertain` rather than silently retrying (the default `lifecycle.allow-uncertain-retry` is false). The timeline stores bounded evidence: attempt phase, target, status/category, whether bytes were written, an idempotency-key hash, request size/latency, and any upstream request ID; it never stores the raw body. An operator first previews a decision, then submits a short reason with the same authenticated actor:

```bash
curl -H "Authorization: Bearer $RELAY_LIFELINE_ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"action":"confirm_success"}' \
  "http://127.0.0.1:8318/admin/api/requests/$REQUEST_ID/uncertain/preview"

curl -H "Authorization: Bearer $RELAY_LIFELINE_ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"action":"confirm_success","confirmationToken":"TOKEN_FROM_PREVIEW","reason":"Verified the provider request ID in the upstream audit."}' \
  "http://127.0.0.1:8318/admin/api/requests/$REQUEST_ID/uncertain/resolve"
```

The preview token expires after two minutes and is bound to the request, action, and actor. The supported actions are `confirm_success` (business success, no retry), `abandon` (terminal failure), and `request_compensation` (resume through the normal retry path). Do not use a blind retry when the provider may have charged the request. `/admin/api/health/summary`, `/admin/api/slo`, Webhooks, and `relay_lifeline_uncertain_*` Prometheus metrics expose open count, oldest age, resolution target, SLO breach, and resolution outcomes.

Request, incident, repeat-task, usage-ledger, and policy-release journals are verified hash chains. If `RELAY_LIFELINE_JOURNAL_HMAC_KEY` is configured, an external HMAC anchor is checked as well. Startup refuses a corrupt or truncated chain; `-journal-verify` is read-only. Hourly compaction atomically rebuilds the retained chain and keeps active entities alive. On restart, unfinished requests become `orphaned` and are never replayed; governance reservations are reconciled, and prepared policy releases are finalized only when the active revision matches, otherwise explicitly aborted. Keep the persistence directory on durable storage and investigate `readyz`, `/admin/api/persistence/status`, and journal metrics before forcing a repair.

## Monitoring API

The authenticated management API exposes:

- `GET /admin/api/metrics?window=15m|1h|6h|24h` for totals, minute series, current load, and recovery histograms. The default window is `1h`.
- `GET /admin/api/metrics/errors?window=15m|1h|6h|24h` for the stable error distribution. The default window is `24h`.
- `GET /admin/api/events?after=<cursor>&limit=<1-200>` for the bounded operational event ring. Responses include `nextAfter`, `oldestAfter`, `hasMore`, and `hasGap` so clients can resume or detect overwritten events.
- `GET /admin/api/runtime-logs?tail=true&limit=<1-500>` for the latest structured log entries, or use an `after` cursor for incremental reads. Responses include `entries`, `nextAfter`, `oldestAfter`, `hasMore`, and `hasGap`, with strict bounds on levels and filters.
- `GET /admin/api/history?cursor=<cursor>&limit=<1-200>&from=<RFC3339>&to=<RFC3339>&state=<state>&q=<text>` for server-side filters and stable cursor pagination.
- `GET /admin/api/incidents` accepts the same pagination fields; `GET /admin/api/incidents/{id}` returns the incident and at most 100 retained related requests.
- `GET /admin/api/notifications/status` and `/notifications/deliveries` expose queue and outcome counters plus recent deliveries; the status includes only HMAC signing state and Key ID, never the secret. Operators can call `POST /notifications/test`.
- `GET /admin/api/stream?after=<cursor>` sends one `sync` followed by changed-domain `update` events. Each event carries `version`, `sequence`, `type`, and `data`; an overwritten cursor produces `reset`.
- The Prometheus endpoint includes `relay_lifeline_journal_*` gauges for entry count, bytes, startup replay, latest compaction, write health, and compaction health. `relay_lifeline_process_*` exposes the PID, Go goroutines, heap memory, system memory, and GC cycles.

Clients may optionally send `X-Relay-Lifeline-Client-ID` and `X-Relay-Lifeline-Task-ID`. `X-Codex-Session-ID` and `X-Codex-Thread-ID` are accepted for Codex wrappers. Values must be no longer than 128 bytes and contain only safe identifier characters. They are retained as unverified client-declared correlation metadata in active requests, history, and runtime logs, but are never forwarded upstream. Never put a key or token in these headers. Responses include `X-Relay-Lifeline-Request-ID` for correlation with the gateway timeline. In Codex app-server, `threadId` is a logical conversation ID, not an operating-system PID; a background terminal's `processId` is an app-server-level process identifier, with a separate `osPid` field that may be null, and neither should be treated as a host process number. If Codex does not explicitly send these headers, the gateway cannot infer a task ID from HTTP traffic or reliably obtain a host Codex PID from inside the Docker container.

Diagnostic ZIP exports include separate redacted configuration, diagnostics, timeline, runtime log, metric, incident, recovery-check, journal-summary, and configuration-backup metadata files. They never include request/response bodies, safe error details, backup contents, or secrets. Each request timeline retains at most 100 events; on overflow it preserves the first and most recent events and reports `eventsTruncated` and `droppedEvents`.

The browser exchanges a management key once for a short-lived HttpOnly SameSite session cookie. Mutating calls require the per-session CSRF token. Bearer authentication remains available for CLI compatibility.

Every management response includes the compatibility header `X-Relay-Lifeline-API-Version`. `GET /admin/api/meta` returns the running build identity. Configuration documents use `schema-version: 5`; schemas 1 through 4 migrate in memory without overwriting the source file, while unknown future schemas are rejected. `POST /admin/api/config/validate` returns the exact change plan without modifying runtime or disk state.

## Localization

The Web UI switches language immediately and stores the choice in `localStorage`. Management API calls send `Accept-Language`; responses include `Content-Language`.

The UI also supports `system`, `light`, and `dark` themes on both the login screen and console. `system` follows the operating-system preference; an explicit light or dark choice is stored in `localStorage`.

```yaml
localization:
  default-locale: "zh-CN"
  fallback-locale: "en-US"

logging:
  locale: "zh-CN"

notifications:
  locale: "zh-CN"
```

Stable JSON fields, status values, event codes, and message codes remain in English. Human-readable messages are localized. See [Localization](docs/localization.md) for contributor rules.

## Safety Model

- Docker publishes only `127.0.0.1:8318` by default.
- Client Authorization is forwarded in memory and is never intentionally logged.
- Request bodies, response bodies, and Authorization logging are rejected by configuration validation.
- Safe error details include only allowlisted structured fields and are redacted before entering history.
- Monitoring metrics never contain prompts, response bodies, Authorization, or raw upstream errors. Errors use only the stable `transport`, `protocol`, `auth`, `rate_limit`, `client`, `server`, and `http` categories; operational events contain only stable codes and bounded metadata.
- Temporary response files use `0600` permissions and are deleted after delivery or failure.
- Diagnostic exports redact URL credentials, query strings, Webhook targets, and error details.
- Request and incident timelines use SHA-256 hash-chain journals. Startup refuses corrupted, truncated, or modified journals; expired entities are removed by an atomic, mode-`0600` compaction that rebuilds and verifies the retained chain.
- Browser management keys are never stored in `sessionStorage` or `localStorage`; HttpOnly sessions enforce CSRF and login cooldowns.
- The admin console has strict security headers and no third-party CDN dependency.
- Temporary capture is idle by default. Bodies use chunked AES-256-GCM encryption and authentication headers are never persisted.
- Full raw content cannot be previewed online. It is streamed through decryption into a download ZIP without a plaintext ZIP on disk.
- Captures expire after 72 hours by default. Capacity or free-disk exhaustion stops body capture without blocking proxy traffic.
- Successful and terminal failed attempts are both represented as final responses. An active capture recovered after a service restart is finalized as `interrupted` instead of remaining permanently active.
- Capture encryption supports an active key ID and a historical key ring. `RELAY_LIFELINE_CAPTURE_KEY` is the backward-compatible `legacy` key; configure a new key with `RELAY_LIFELINE_CAPTURE_ACTIVE_KEY_ID` and the JSON object `RELAY_LIFELINE_CAPTURE_KEYRING`. Keep old and new keys during restart, use the capture page to rewrap records, and remove an old key only after its record count and the unresolved count are both zero.

Do not expose the admin endpoint directly to the public internet. Put TLS, access control, and a trusted network boundary in front of it when remote access is required.

## Diagnostics and Notifications

Diagnostics verify configuration, file access, admin-key length, CPA DNS/TCP reachability, cache permissions, and disk capacity. The upstream check opens a TCP connection only; it does not send a model request or consume tokens.

Webhooks can report stalled, recovered, long-running, many-attempt, authentication-error, queue-pressure, and disk-pressure events. Payloads contain stable `eventCode` and numeric `elapsedSeconds` fields alongside localized text. Delivery uses a bounded queue and never blocks the model request path; the management plane retains only the latest 100 outcomes, never payloads or target URLs. Every configured Webhook must also set `RELAY_LIFELINE_WEBHOOK_SIGNING_KEY_ID` and `RELAY_LIFELINE_WEBHOOK_SIGNING_SECRET`; each delivery includes `X-Relay-Lifeline-Signature-Key-ID`, `X-Relay-Lifeline-Signature-Timestamp`, and `X-Relay-Lifeline-Signature`. Verify `v1=<hex HMAC-SHA256(timestamp + "." + raw payload)>` before accepting it. The secret is supplied only through the process environment, and a configured Webhook refuses to start with a partial or short secret.

Continuous tasks support a strict `maxTokens` limit. The counter advances only from an upstream-authoritative `usage.total_tokens`; missing usage pauses the task with `usage_missing` instead of estimating input/output tokens or currency. Reaching the limit expires the task after the current execution completes; Relay-Lifeline does not implement cost estimation.

## Development

Requirements: Go 1.25+, Node.js 22+, and Docker for integration verification.

```bash
make check
make race
./scripts/ci-integration.sh
make docker-build
./scripts/container-smoke.sh relay-lifeline:dev
```

Some macOS/Xcode and older Go combinations require external linking for test binaries. Use `go test -ldflags=-linkmode=external ./...` if the internal linker reports a missing `LC_UUID` load command; releases and CI use Go 1.25.x.

See the [Operations Guide](docs/operations.md), [Architecture](docs/architecture.md), [Contributing](CONTRIBUTING.md), and [Security](SECURITY.md).

## Rollback

Relay-Lifeline does not modify upstream accounts or API keys. Change the client's `base_url` back to CPA or the original relay to bypass the gateway immediately. Before every persisted configuration change, the current file is copied with mode `0600` to `server.config-backup-dir` (or `.relay-lifeline-backups` beside the config) and the newest 10 copies are retained. Keep the previous image when upgrading a deployed instance.

Docker builds accept `VERSION`, `REVISION`, and `BUILD_TIME` arguments. The runtime image reference can be supplied through `RELAY_LIFELINE_IMAGE_REF`, allowing the Settings page and `/admin/api/meta` to identify the exact deployment being rolled back.

## Known Risk

If an upstream request completed and incurred a charge but the connection failed before the gateway received the completion marker, a transparent retry can duplicate a call or charge. A middle gateway cannot eliminate this risk unless the upstream implements a reliable idempotency key.

## License

[Apache-2.0](LICENSE)

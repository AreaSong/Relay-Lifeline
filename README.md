# Transfer Lifeline

[GitHub](https://github.com/AreaSong/transfer-lifeline) | [简体中文](README.zh-CN.md)

Transfer Lifeline is a resilient local gateway for OpenAI-compatible API relays. It sits between Codex, an IDE, or another AI client and an existing relay, keeps the downstream connection alive, and retries the same request after the configured delay when the upstream returns any error.

The project is independent from CLIProxyAPI (CPA) and all model providers.

```text
AI client
   |  Existing API key; change only base_url
   v
Transfer Lifeline :8318
   |  Authorization is forwarded unchanged
   v
CLIProxyAPI :8317 or another OpenAI-compatible relay
   v
Accounts, routes, and model providers managed by that relay
```

The public project name is **Transfer Lifeline**, and the official image is `ghcr.io/areasong/transfer-lifeline`. For upgrade compatibility, the binary, Go module, `RELAY_LIFELINE_*` environment variables, `X-Relay-Lifeline-*` headers, and storage paths retain the `relay-lifeline` technical identifier.

## Capabilities

- Retry all upstream errors, or only transient errors.
- Random retry delay between 60 and 120 seconds by default.
- Unlimited retries or a configurable attempt limit.
- Optional support for upstream `Retry-After`.
- Responses API and Chat Completions SSE completion validation.
- SSE keepalive comments while a complete response is being buffered.
- Full-response buffering to avoid partial output and duplicate tool delivery.
- Client cancellation propagation, bounded concurrency, waiting queue, and recovery pacing.
- Per-request timeline, bounded in-memory history, diagnostics, and risk alerts.
- Signal Continuity view of the Codex-to-relay path, active work, waiting requests, and the next retry, with a static fallback when WebGL is unavailable.
- In-memory reliability, pressure, error-category, and recovery telemetry across `15m`, `1h`, `6h`, and `24h` windows.
- Safe extraction of structured upstream error details with redaction and size limits.
- Filterable, pausable, downloadable structured runtime logs without request or response bodies.
- Explicit temporary diagnostic capture for requests, every CPA response, and the final response, with filtered preview plus filtered and full-raw downloads.
- Asynchronous Webhook delivery with event filters and retry.
- Chinese and English UI, API messages, CLI text, logs, diagnostics, and Webhooks.
- Separate UI, log, and notification locales with hot-reloadable configuration.
- Independent admin key and secure-by-default local binding.

Transfer Lifeline always targets one relay. Account pools, provider selection, model mapping, weights, and failover between relay vendors remain the responsibility of CPA or the configured upstream.

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

`all-errors` retries every unsuccessful upstream result, including HTTP `4xx` and `5xx`, connection and timeout errors, malformed or empty JSON, `response.failed`, `response.incomplete`, and truncated SSE streams.

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

An `Idempotency-Key` supplied by the client is forwarded unchanged on every attempt; Transfer Lifeline does not invent one because an upstream may cache an error under that key. Disconnect detection combines the downstream request context, connection-close notification, and heartbeat write/flush errors so active upstream work is canceled when the client is gone.

## Administration

The console uses layered management keys: Viewer can read redacted status and content, Operator can perform operational mutations, and Sensitive Data includes Operator rights plus full-raw download. The existing `RELAY_LIFELINE_ADMIN_KEY` is the Operator key.

The console can:

- Inspect active requests, attempts, next retry time, and safe failure details.
- Review request timelines and bounded persistent history.
- Review current load, time-windowed reliability and pressure charts, seven stable error categories, recovery histograms, and cursor-based operational events.
- View non-blocking alerts for long-running requests, repeated attempts, authentication failures, queue pressure, and disk pressure.
- Run diagnostics without calling the model API and export a redacted JSON bundle.
- Pause or resume all requests, retry immediately, or cancel a request.
- Change retry, stream, queue, history, risk, notification, logging, and locale settings.
- Validate configuration without persistence, show hot-reload and restart sections, save atomically, and reload it from disk.
- Display runtime version, revision, build time, image reference, uptime, Admin API version, and configuration schema version.
- Filter and download live structured runtime logs.
- Capture the next bounded set of requests, preview filtered bodies, and download filtered or full-raw ZIP archives.

Request and incident timelines persist in verified journals and are restored after restart; interrupted requests are restored as `orphaned` and are never replayed without their original client connection. Monitoring metrics, operational events, and live runtime logs remain process-local and reset on restart. Monitoring uses 1,440 fixed UTC minute buckets independently of `history.retention`; `dataSince` and `complete` show when the process has not yet observed a full requested window. Listen address, admin enablement, upstream transport settings, server timeouts, and log level require a restart. Retry, queue, history, risk, locale, and notification behavior is read from the current configuration during operation.

Signal Continuity visualizes observed gateway state; it does not send an extra model probe. Three.js is loaded locally and on demand. Reduced-motion preferences and background tabs pause animation, while WebGL initialization failure or context loss switches to a static topology without disabling status data or controls.

## Monitoring API

The authenticated management API exposes:

- `GET /admin/api/metrics?window=15m|1h|6h|24h` for totals, minute series, current load, and recovery histograms. The default window is `1h`.
- `GET /admin/api/metrics/errors?window=15m|1h|6h|24h` for the stable error distribution. The default window is `24h`.
- `GET /admin/api/events?after=<cursor>&limit=<1-200>` for the bounded operational event ring. Responses include `nextAfter`, `oldestAfter`, `hasMore`, and `hasGap` so clients can resume or detect overwritten events.
- `GET /admin/api/runtime-logs?tail=true&limit=<1-500>` for the latest structured log entries, or use an `after` cursor for incremental reads. Responses include `entries`, `nextAfter`, `oldestAfter`, `hasMore`, and `hasGap`, with strict bounds on levels and filters.

Diagnostic ZIP exports include separate redacted configuration, diagnostics, timeline, runtime log, metric, and incident files. They never include request/response bodies or safe error details. Each request timeline retains at most 100 events; on overflow it preserves the first and most recent events and reports `eventsTruncated` and `droppedEvents`.

The browser exchanges a management key once for a short-lived HttpOnly SameSite session cookie. Mutating calls require the per-session CSRF token. Bearer authentication remains available for CLI compatibility.

Every management response includes the compatibility header `X-Relay-Lifeline-API-Version`. `GET /admin/api/meta` returns the running build identity. Configuration documents use `schema-version: 2`; schema 1 files migrate in memory without overwriting the source file, while unknown future schemas are rejected. `POST /admin/api/config/validate` returns the exact change plan without modifying runtime or disk state.

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

Webhooks can report stalled, recovered, long-running, many-attempt, authentication-error, queue-pressure, and disk-pressure events. Payloads contain stable `eventCode` and numeric `elapsedSeconds` fields alongside localized text. Delivery uses a bounded queue and never blocks the model request path.

## Development

Requirements: Go 1.22+, Node.js 22+, and Docker for integration verification.

```bash
make check
make docker-build
```

On some recent macOS/Xcode combinations, Go 1.22 test binaries require external linking. Use `go test -ldflags=-linkmode=external ./...` if the internal linker reports a missing `LC_UUID` load command.

See the [Operations Guide](docs/operations.md), [Architecture](docs/architecture.md), [Contributing](CONTRIBUTING.md), and [Security](SECURITY.md).

## Rollback

Transfer Lifeline does not modify upstream accounts or API keys. Change the client's `base_url` back to CPA or the original relay to bypass the gateway immediately. Before every persisted configuration change, the current file is copied with mode `0600` to `server.config-backup-dir` (or `.relay-lifeline-backups` beside the config) and the newest 10 copies are retained. Keep the previous image when upgrading a deployed instance.

Docker builds accept `VERSION`, `REVISION`, and `BUILD_TIME` arguments. The runtime image reference can be supplied through `RELAY_LIFELINE_IMAGE_REF`, allowing the Settings page and `/admin/api/meta` to identify the exact deployment being rolled back.

## Known Risk

If an upstream request completed and incurred a charge but the connection failed before the gateway received the completion marker, a transparent retry can duplicate a call or charge. A middle gateway cannot eliminate this risk unless the upstream implements a reliable idempotency key.

## License

[Apache-2.0](LICENSE)

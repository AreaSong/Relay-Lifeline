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

The public project name is **Relay-Lifeline**. Executables, images, environment variables, and compatibility headers use the matching `relay-lifeline` technical identifier.

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

Relay-Lifeline always targets one relay. Account pools, provider selection, model mapping, weights, and failover between relay vendors remain the responsibility of CPA or the configured upstream.

## Quick Start

```bash
cp config.docker.example.yaml config.docker.yaml
cp .env.example .env
```

Set a long random `RELAY_LIFELINE_ADMIN_KEY` in `.env` and generate a separate 32-byte capture key:

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

## Administration

The console uses a separate `RELAY_LIFELINE_ADMIN_KEY`. It can:

- Inspect active requests, attempts, next retry time, and safe failure details.
- Review request timelines and bounded in-memory history.
- Review current load, time-windowed reliability and pressure charts, seven stable error categories, recovery histograms, and cursor-based operational events.
- View non-blocking alerts for long-running requests, repeated attempts, authentication failures, queue pressure, and disk pressure.
- Run diagnostics without calling the model API and export a redacted JSON bundle.
- Pause or resume all requests, retry immediately, or cancel a request.
- Change retry, stream, queue, history, risk, notification, logging, and locale settings.
- Save configuration atomically or reload it from disk.
- Filter and download live structured runtime logs.
- Capture the next bounded set of requests, preview filtered bodies, and download filtered or full-raw ZIP archives.

History, monitoring metrics, and operational events are memory-only and are cleared on restart. Monitoring uses 1,440 fixed UTC minute buckets independently of `history.retention`; `dataSince` and `complete` show when the process has not yet observed a full requested window. Listen address, admin enablement, upstream transport settings, server timeouts, and log level require a restart. Retry, queue, history, risk, locale, and notification behavior is read from the current configuration during operation.

Signal Continuity visualizes observed gateway state; it does not send an extra model probe. Three.js is loaded locally and on demand. Reduced-motion preferences and background tabs pause animation, while WebGL initialization failure or context loss switches to a static topology without disabling status data or controls.

## Monitoring API

The authenticated management API exposes:

- `GET /admin/api/metrics?window=15m|1h|6h|24h` for totals, minute series, current load, and recovery histograms. The default window is `1h`.
- `GET /admin/api/metrics/errors?window=15m|1h|6h|24h` for the stable error distribution. The default window is `24h`.
- `GET /admin/api/events?after=<cursor>&limit=<1-200>` for the bounded operational event ring. Responses include `nextAfter`, `oldestAfter`, `hasMore`, and `hasGap` so clients can resume or detect overwritten events.

These endpoints use the same admin Bearer authentication and localization rules as the rest of the management API.

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
- The admin console has strict security headers and no third-party CDN dependency.
- Temporary capture is idle by default. Bodies use chunked AES-256-GCM encryption and authentication headers are never persisted.
- Full raw content cannot be previewed online. It is streamed through decryption into a download ZIP without a plaintext ZIP on disk.
- Captures expire after 72 hours by default. Capacity or free-disk exhaustion stops body capture without blocking proxy traffic.
- Persist `RELAY_LIFELINE_CAPTURE_KEY` independently. The first release has no historical key ring; download or delete old captures before rotation.

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

See [Architecture](docs/architecture.md), [Contributing](CONTRIBUTING.md), and [Security](SECURITY.md).

## Rollback

Relay-Lifeline does not modify upstream accounts or API keys. Change the client's `base_url` back to CPA or the original relay to bypass the gateway immediately. Keep the previous image and configuration file before upgrading a deployed instance.

## Known Risk

If an upstream request completed and incurred a charge but the connection failed before the gateway received the completion marker, a transparent retry can duplicate a call or charge. A middle gateway cannot eliminate this risk unless the upstream implements a reliable idempotency key.

## License

[MIT](LICENSE)

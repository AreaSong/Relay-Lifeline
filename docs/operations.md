# Relay-Lifeline Operations Guide

[简体中文](operations.zh-CN.md)

## Prerequisites

- Docker Engine and Docker Compose.
- One reachable CPA or other OpenAI-compatible relay.
- Distinct management keys of at least 24 characters; Viewer is optional.
- A separate Base64-encoded 32-byte capture key.
- Keep the management endpoint on `127.0.0.1:8318` unless a trusted TLS and access-control boundary is present.

Generate local keys with `openssl rand -base64 36` and `openssl rand -base64 32`. Never place live values in the repository, issues, logs, or screenshots.

## Install

```bash
cp config.docker.example.yaml config.docker.yaml
cp .env.example .env
chmod 600 .env config.docker.yaml
docker compose up -d
curl -fsS http://127.0.0.1:8318/healthz
curl -fsS http://127.0.0.1:8318/readyz
```

Use `http://cli-proxy-api:8317` when both services share a Compose network, or `http://host.docker.internal:8317` when CPA runs on the host.

Change only the client Base URL to `http://127.0.0.1:8318/v1`. Keep the existing business API key.

## Recommended baseline

```yaml
server:
  shutdown-timeout: "3m"
upstream:
  connect-timeout: "10s"
  response-header-timeout: "30s"
  response-body-idle-timeout: "90s"
retry:
  enabled: true
  mode: "all-errors"
  min-interval: "60s"
  max-interval: "120s"
  max-attempts: 0
  honor-retry-after: true
stream:
  heartbeat-interval: "15s"
queue:
  max-active: 8
  max-waiting: 100
  recovery-spacing: "2s"
```

The body-idle timeout starts after the most recent upstream body data. It closes a stalled response and enters the retry loop instead of hanging forever after headers arrive.

## Roles

| Role | Key | Access |
|---|---|---|
| Viewer | `RELAY_LIFELINE_VIEWER_KEY` | Redacted status, logs, and filtered captures |
| Operator | `RELAY_LIFELINE_ADMIN_KEY` | Viewer plus configuration and operational actions |
| Sensitive Data | `RELAY_LIFELINE_SENSITIVE_KEY` | Operator plus full-raw downloads |

Raw downloads also require `X-Relay-Lifeline-Confirm: download-sensitive`. Management keys and the CPA business API key must have separate lifecycles.

## Capture key rotation

1. Generate a new 32-byte key and Key ID.
2. Put both old and new keys in `RELAY_LIFELINE_CAPTURE_KEYRING`; select the new active ID.
3. Recreate the container and verify both IDs are visible.
4. Rewrap through the capture page.
5. Require zero old-ID records and `unresolved: 0`.
6. Remove the old key and recreate the container.
7. Verify filtered preview and an authorized raw download.

Rewrap changes only small wrapped data keys. Never retire a key while records still reference it.

## Incidents

The normal recovery path is `queued -> requesting -> waiting -> requesting -> completed`. Use the Request ID across the Requests, Logs, History, and Capture views. Run diagnostics before changing policy; the upstream probe performs DNS/TCP checks without a model call.

Relay-Lifeline does not own account pools, quotas, model routing, or vendor failover. Diagnose those in CPA or the configured relay.

Useful commands:

```bash
docker compose ps
docker compose logs --tail=200 relay-lifeline
curl -fsS http://127.0.0.1:8318/healthz
curl -fsS http://127.0.0.1:8318/readyz
```

Readiness returns `503 unavailable` when an enabled request or incident journal is closed, unwritable, or has a recorded write failure. Prometheus exposes the `relay_lifeline_journal_*` gauges for entries, bytes, replay duration, latest compaction, and journal/compaction health.

## Migration, recovery, and drills

Run these commands against a stopped instance or a copy of its files. `-recovery-check` is read-only; `-config-migrate` first creates a mode-`0600` backup and then atomically writes schema 2.

```bash
relay-lifeline -config /etc/relay-lifeline/config.yaml -config-validate
relay-lifeline -config /etc/relay-lifeline/config.yaml -config-migrate
relay-lifeline -config /etc/relay-lifeline/config.yaml -recovery-check
relay-lifeline -journal-verify /var/lib/relay-lifeline/events/requests.jsonl
```

For an isolated retry drill, start `fault-upstream` on a test port and point a temporary Lifeline configuration at it. Never replace the production CPA address for this test.

```bash
go run ./cmd/fault-upstream -listen 127.0.0.1:18317 \
  -sequence 401,429,503,invalid-json,truncated-sse,success
./scripts/ci-integration.sh
```

The diagnostic ZIP adds `recovery-check.json`, `journal-summary.json`, and `config-backups.json`. Backup records contain only filename, modification time, size, SHA-256, source schema, and validation status. The bundle never contains raw request/response bodies or backup contents.

## Restart and drain

Keep Compose `stop_grace_period` longer than `server.shutdown-timeout`. On a termination signal, the server becomes unready, wakes waiting requests for one immediate retry, and synchronously waits for active handlers to drain.

A forced process termination cannot preserve an existing TCP connection. OpenAI-compatible clients expose no request handle that a new process can adopt. The client must reconnect; incomplete capture evidence is finalized as `interrupted` rather than reported as successful.

## Upgrade and rollback

Before upgrading, retain the old image and its matching configuration. After deployment, verify runtime metadata, all diagnostics, capture key resolution, role isolation, and a non-model connectivity request.

To roll back, first point clients directly at CPA if immediate bypass is needed. Restore both the old image and its matching config, recreate the container, then verify health, version, diagnostics, and capture readability. An old binary may reject fields introduced by a newer config.

Configuration writes retain the newest ten mode-`0600` backups. Encrypted captures persist across restarts and expire after 72 hours by default. Request and incident journals persist under `/var/lib/relay-lifeline/events`, are compacted hourly according to their retention settings, and must use a persistent volume. Metrics, operational events, and live runtime logs reset on restart.

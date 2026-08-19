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
  memory-limit: "64MiB"
  max-response-body: "512MiB"
  max-total-cache: "2GiB"
queue:
  max-active: 8
  max-waiting: 100
  recovery-spacing: "2s"
```

The body-idle timeout starts after the most recent upstream body data. It closes a stalled response and enters the retry loop instead of hanging forever after headers arrive.

`memory-limit` is the temporary-file spill threshold. A decoded response above `max-response-body`, process-wide active cache usage above `max-total-cache`, or a temporary directory unable to preserve `risk.minimum-free-disk` fails immediately without retry. Capacity fields hot-reload from Settings. Lowering the total budget does not remove existing caches; new attempts use the new budget, while an attempt already in progress uses the configuration snapshot from its start.

Before a production change, drill each boundary: a body just above the per-response limit attempts once; concurrent caches stop at the total budget; gzip JSON decodes and completes; and audio or file media types are explicitly rejected instead of mislabeled as JSON.

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

## Traffic-policy release runbook

Use an Operator credential for every write below. Record the current desired `configRevision` before starting; a conflict means another change landed and the draft must be re-read, not forced.

1. Inspect `GET /admin/api/config/state`, `GET /admin/api/policies`, `GET /admin/api/policies/releases`, `GET /admin/api/policies/status`, `GET /admin/api/slo`, and `GET /admin/api/health/summary`. Confirm the desired `configRevision`, upstream target IDs, current release stage, SLO health, and journal health.
2. Save a candidate with `PUT /admin/api/policies/draft` and the previous `draftRevision` when editing an existing draft. Run `POST /admin/api/policies/simulate?source=draft` for representative method/path/model/principal inputs, then use `POST /admin/api/policies/replay` for a sanitized capture or request sample. Confirm `dryRun: true`, `enforced: false`, and that no upstream request was created.
3. Publish `stage: shadow` first when the candidate has a shadow target. Watch `shadowPlanned`, `shadowSent`, `shadowSkipped`, `shadowFailed`, `shadowReservedCostMicros`, and `shadowActualCostMicros` in `/policies/status`; also verify that the primary target's circuit and adaptive counters do not change from shadow outcomes. Stop and roll back if shadow failure, skip, or cost behavior is outside the change window.
4. Publish a bounded `stage: canary` with an explicit `canaryPercent` (1-100). Check recent decisions for stable request-ID bucketing: `canarySelected` must match the intended proportion, and only selected decisions in enforce mode may have `enforced: true` and a non-empty `targetId`. A request with `recommendedTargetId` but no `targetId` was not routed by the policy.
5. Promote to `stage: full` only after the canary window meets the availability, recovery latency, error-budget, upstream circuit, and governance budgets. Keep the prior release revision available for rollback.

For an urgent rollback, pause or drain new work if the blast radius is still growing, then call `POST /admin/api/policies/rollback` with the current `configRevision` and the known-good `policyRevision` from release history. Verify `/policies/releases`, `/policies/status`, `/config/state`, `/health/summary`, and one non-model connectivity request before resuming traffic. The rollback is itself journaled; do not edit `policy-releases.jsonl` by hand.

Adaptive routing requires a separate watch. Review `adaptiveStopped`, `adaptiveStopReason`, `adaptiveSwitches`, `adaptiveLastTargetId`, and `adaptiveLastScore` together with `/slo` `errorBudgetRemaining` and `burnRate`. `slo_guard`, `burn_rate_guard`, `failure_rate_guard`, and `adaptive_auto_stopped` are stop signals, not transient target failures. A switch cooldown can intentionally keep the previous eligible target. Fix the underlying signal and publish a new policy revision to acknowledge an automatic stop; verify the fallback target and SLO before promotion.

## Governance ledger runbook

`GET /admin/api/governance/status` shows reservations, per-scope usage, reserved capacity, unknown usage, rejection reasons, and ledger state. `GET /admin/api/persistence/status`, `/health/summary`, `/readyz`, and Prometheus `relay_lifeline_governance_*` and `relay_lifeline_journal_*` metrics are the cross-checks.

- In `governance.mode: enforce`, stop or drain new traffic when the usage ledger is `degraded` or `/readyz` is `503`. Admission, retry-attempt reservation, and settlement are fail-closed when a configured ledger cannot be written. Preserve the volume, check ownership/permissions, free space, and the failed stage, then restart only after `-journal-verify` succeeds on a stopped copy.
- In `observe`, a ledger write failure is exposed as `persistenceDegraded` and the request path may continue. Treat this as a release-blocking incident if budgets are relied on for safety; changing to enforce without a tested ledger is unsafe.
- For `unknownUsagePolicy: deny`, investigate non-zero `unknownUsage` entries before admitting more work in that budget window. An unknown settlement is recorded after bytes were written but authoritative usage was absent; it is not converted into an estimated token or cost value. Resolve the provider usage issue or wait for the window to roll over, then confirm the counters and reservations have settled.
- Do not delete or truncate `usage-ledger.jsonl` to clear a budget. The ledger replays reservations and settlements on startup, reconciles interrupted reservations, and is compacted with active reservations protected by heartbeats.

## Uncertain-delivery runbook

An `uncertain` request means the gateway may have written bytes upstream but did not receive response headers. The default path blocks an automatic retry. Locate the Request ID in `/admin/api/status`, inspect `/admin/api/requests/{id}/timeline`, and compare the evidence with the provider audit before choosing an action.

```bash
curl -H "Authorization: Bearer $RELAY_LIFELINE_ADMIN_KEY" \
  "http://127.0.0.1:8318/admin/api/requests/$REQUEST_ID/timeline"
curl -H "Authorization: Bearer $RELAY_LIFELINE_ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"action":"confirm_success"}' \
  "http://127.0.0.1:8318/admin/api/requests/$REQUEST_ID/uncertain/preview"
```

The preview response is the evidence record and a two-minute, actor-bound confirmation token. Submit the token with a non-empty reason (maximum 500 Unicode characters) to `/uncertain/resolve`. Choose exactly one: `confirm_success` when the provider audit proves completion, `abandon` when the business operation should be treated as failed, or `request_compensation` when a retry is explicitly approved and the idempotency/charge risk is understood. A stale token, different action, different actor, or already-resolved request must be treated as a conflict. Do not reuse the token in scripts or logs.

Monitor `/admin/api/health/summary`, `/admin/api/slo`, the `uncertain_slo_breach` Webhook, and `relay_lifeline_uncertain_open`, `relay_lifeline_uncertain_oldest_seconds`, and `relay_lifeline_uncertain_slo_healthy`. Resolve the oldest records before the configured resolution target; the health component becomes degraded after that target. `orphaned` is different: it is a request left unfinished across a restart and is retained for history only, never replayed.

## Journal verification and recovery

The persistence directory contains `requests.jsonl`, `incidents.jsonl`, `repeat-tasks.jsonl`, `usage-ledger.jsonl`, and `policy-releases.jsonl`. Each is a hash chain; `RELAY_LIFELINE_JOURNAL_HMAC_KEY` additionally protects the external `.anchor` file. Verify a stopped instance or a read-only copy:

```bash
for journal in requests incidents repeat-tasks usage-ledger policy-releases; do
  relay-lifeline -journal-verify \
    "/var/lib/relay-lifeline/events/${journal}.jsonl" || exit 1
done
relay-lifeline -config /etc/relay-lifeline/config.yaml -recovery-check
```

Startup refuses a malformed line, sequence gap, hash mismatch, or anchor mismatch. Keep the original volume unchanged for forensics; restore a known-good volume/config backup rather than removing a line or rebuilding a chain manually. Hourly compaction is atomic and re-hashes retained entries. Request and incident records beyond retention are removed, while active entities remain; policy prepared intents are finalized only when the active policy revision matches, and otherwise written as aborted. After recovery, check `/readyz`, `/admin/api/persistence/status`, `/admin/api/governance/status`, `/admin/api/policies/releases`, and the Prometheus compaction-health gauges before accepting traffic.

## Migration, recovery, and drills

Run these commands against a stopped instance or a copy of its files. `-recovery-check` is read-only; `-config-migrate` first creates a mode-`0600` backup and then atomically writes schema 5. Schemas 1 through 4 are migratable; schema 5 adds OIDC management authentication while preserving local break-glass access for migrated configurations.

```bash
relay-lifeline -config /etc/relay-lifeline/config.yaml -config-validate
relay-lifeline -config /etc/relay-lifeline/config.yaml -config-migrate
relay-lifeline -config /etc/relay-lifeline/config.yaml -recovery-check
relay-lifeline -journal-verify /var/lib/relay-lifeline/events/requests.jsonl
```

For an isolated retry drill, start `fault-upstream` on a test port and point a temporary Relay-Lifeline configuration at it. Never replace the production CPA address for this test.

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

To roll back, first point clients directly at CPA if immediate bypass is needed. Restore both the old image and its matching pre-migration config, recreate the container, then verify health, version, diagnostics, and capture readability. An old binary may reject fields introduced by a newer config; do not point it at a schema 5 file.

Configuration writes retain the newest ten mode-`0600` backups. Encrypted captures persist across restarts and expire after 72 hours by default. Request and incident journals persist under `/var/lib/relay-lifeline/events`, are compacted hourly according to their retention settings, and must use a persistent volume. Metrics, operational events, and live runtime logs reset on restart.

## Control-plane cursors and notifications

- History and incident pages default to 100 items and permit at most 200. Automation should retain `nextCursor` as opaque data.
- Browsers resume the management stream with `Last-Event-ID`; non-browser clients may pass `after`. A `reset` means the cursor fell outside the 512-event ring and its complete payload must replace local state.
- `/admin/api/notifications/status` and `/admin/api/notifications/deliveries` expose current Webhook health and recent outcomes. Operator-only test delivery calls the configured endpoint; the 100-item history contains neither payloads nor target URLs.
- A continuous task with `circuitOpen` never resumes automatically. Inspect its bounded run audit and upstream health before an Operator resumes it. An `expired` task that reached an execution or failure limit cannot run again.

### Webhook signing and strict token budgets

Set `RELAY_LIFELINE_WEBHOOK_SIGNING_KEY_ID` and `RELAY_LIFELINE_WEBHOOK_SIGNING_SECRET` alongside the Webhook URL. The secret must be at least 32 bytes; partial configuration is rejected at startup. The sender signs the exact UTF-8 request body with HMAC-SHA256 over `<unix timestamp>.<raw payload>`, sends `v1=<hex digest>` in `X-Relay-Lifeline-Signature`, and includes the timestamp and Key ID in `X-Relay-Lifeline-Signature-Timestamp` and `X-Relay-Lifeline-Signature-Key-ID`. Receivers should reject stale timestamps, look up the secret by Key ID, and compare the digest in constant time. For rotation: first add the new Key ID/secret to the receiver while retaining the old key, then update the sender environment and restart, verify deliveries use the new Key ID, and only then remove the old receiver key. Secrets never enter YAML, the management API, logs, or diagnostics.

Continuous tasks accept `maxTokens`. The limit is enforced only with upstream `usage.total_tokens`; a missing authoritative usage pauses the task as `usage_missing`, and no token or currency estimate is made. Once the accumulated authoritative count reaches the limit, the task expires after the in-flight execution completes.

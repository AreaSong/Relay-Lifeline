# Release Checklist

Run every gate before publishing a tag.

## Source gates

```bash
export VERSION="$(node -p "require('./web/package.json').version")"
export REVISION="$(git rev-parse HEAD)"
export BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
git diff --check
make check
make race
cd web && npx playwright install chromium && npm run test:e2e && cd ..
CGO_ENABLED=0 go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
node scripts/check-release-version.mjs "v${VERSION}"
./scripts/ci-integration.sh
docker build \
  --build-arg VERSION="${VERSION}" \
  --build-arg REVISION="${REVISION}" \
  --build-arg BUILD_TIME="${BUILD_TIME}" \
  -t "relay-lifeline:${VERSION}" .
./scripts/container-smoke.sh "relay-lifeline:${VERSION}" "${VERSION}" "${REVISION}" "${BUILD_TIME}"
```

## Runtime gates

- `/healthz` returns `ok`; `/readyz` returns `ready`.
- `/admin/api/meta` reports the expected version, revision, build time, API version, and config schema.
- All diagnostic checks pass without issuing a model request.
- HTTP `4xx`/`5xx`, DNS, connection refusal, header timeout, body idle timeout, malformed JSON, incomplete SSE, cancellation, and queue saturation are covered.
- Gzip JSON is decoded before validation; client `Accept-Encoding` is not forwarded verbatim.
- Per-response and process-wide cache limits fail once without retry, preserve the disk reserve, and release budget after delivery or failure.
- Responses and Chat Completions use profile-specific JSON/SSE completion rules; unsupported binary media is rejected explicitly.
- Schema 1 through 4 configuration copies migrate to schema 5 with response-cache limits and compatible local break-glass authentication populated.
- A failed request enters `waiting`, emits heartbeats, retries, and delivers one complete response after recovery.
- Request and incident journals survive container recreation, verify successfully, compact expired entities, and restore unfinished requests only as `orphaned`.
- Continuous tasks stop at execution/failure limits, pause on the consecutive-failure circuit, and retain no more than 100 safe run audits.
- Webhook status, test delivery, queue counters, and the bounded delivery history work without storing payloads or target URLs.
- History and incident filters page through stable cursors; incident drill-down returns no more than 100 retained related requests.
- Realtime `sync`, changed-domain `update`, cursor replay, and retention-gap `reset` work; hidden pages suspend non-critical polling.
- Viewer cannot mutate, Operator cannot download raw content, and Sensitive Data still requires explicit raw-download confirmation.
- Completed captures remain readable after upgrade; key status reports zero unresolved records.
- The N-1 image starts with its matching configuration; the current digest migrates a copy to schema 5 and starts; rollback restores the pre-migration configuration before starting N-1 again.
- Scaled 4K (3840x2160), MacBook Pro 14-inch (1512x982), low-height desktop (1280x720), and mobile (390x844) views have no blank canvas, overlap, clipped controls, or role leakage.

For an offline verification from a checked-out release archive, set `RELAY_LIFELINE_USE_LOCAL_IMAGES=true`; the default path always pulls both immutable images before running `scripts/upgrade-smoke.sh`.

### Traffic-policy release gates

- An ordinary `PUT /admin/api/config` or legacy `PUT /admin/api/policies` that changes `traffic-policy` returns `POLICY_RELEASE_REQUIRED`; no route or deny rule is applied through the bypass path.
- `PUT /admin/api/policies/draft` persists a validated candidate, honors `draftRevision` conflicts, and does not change the active policy or desired config revision.
- `POST /admin/api/policies/simulate?source=draft` and `/policies/replay` return `dryRun: true`, never call an upstream, and leave adaptive cooldown/stop state and production routing unchanged.
- The release journal records prepare, publish, rollback, abort, and compensation paths; a crash between the journal and config writes reconciles to publish only when the active revision matches, otherwise records an abort.
- `shadow` and `draft` stages do not enforce client routing, even when policy mode is `enforce`; `observe` mode never changes routing.
- Shadow execution requires a stable sample, an idempotency key when configured, a healthy SLO, body-size limit, max-concurrency lease, hourly request budget, and hourly cost budget. Planned, sent, skipped, failed, reserved, and actual-cost counters are distinct; shadow outcomes do not affect primary circuit or adaptive scoring.
- Canary bucketing is stable for the same request ID and the measured selected proportion is within the release tolerance. Only selected enforce-mode decisions have `enforced: true` and a real `targetId`; `recommendedTargetId` alone cannot alter routing.
- Adaptive routing rejects open/under-observed/over-latency targets, honors the SLO/error-budget floor, burn-rate and failure-rate auto-stop guards, preserves the switch cooldown, and uses the configured fallback target when stopped or when no target is eligible. A new policy revision is required to resume after an automatic stop.
- Full promotion and rollback require the current `configRevision`, retain a known-good release revision, and verify health, SLO, upstream circuits, governance budgets, and one non-model connectivity request before resuming traffic.

### Governance and delivery gates

- Governance reserves bounded token/cost capacity before target selection, binds the actual selected upstream, reserves every retry attempt, and settles each attempt idempotently with authoritative usage when available.
- Principal, tenant, model, and upstream budgets are independently exercised; tenant-scoped budgets reject missing tenant context; soft-threshold and forecast signals are visible without rejecting observe-mode traffic.
- Both `unknown-usage-policy: observe` and `unknown-usage-policy: deny` are tested. Unknown settlements increment the unknown counter without estimated tokens/cost; deny blocks later admissions sharing the affected budget window.
- With governance enforce and a persistent usage ledger, admission, bind, retry reservation, settlement, and any pre-delivery release failure are fail-closed and `/readyz` is unavailable. Post-delivery cleanup failures are surfaced as degraded persistence. Observe mode exposes `persistenceDegraded` and does not silently report healthy persistence.
- An upstream write with no response headers enters `uncertain` and blocks automatic retry by default. Evidence contains attempt phase, target, write flag, status/category, idempotency-key hash, size/latency, and upstream request ID without raw bodies.
- Uncertain preview issues a request/action/actor-bound token with a two-minute TTL; resolve rejects expired, replayed, mismatched, or already-used tokens and requires a non-empty bounded reason. `confirm_success`, `abandon`, and `request_compensation` each produce the documented terminal or retry behavior.
- Uncertain open count, oldest age, resolution target, SLO breach, health degradation, Prometheus gauges, and Webhook notifications are all emitted. Restarted unfinished requests are `orphaned` and never replayed.

### Persistence and promotion gates

- Requests, incidents, repeat tasks, usage ledger, and policy releases all pass hash-chain verification; when configured, the external HMAC anchor also verifies. Startup refuses malformed, truncated, reordered, or modified entries.
- Hourly compaction is atomic, rebuilds a valid retained chain, preserves active entities/reservations, and reports removal count, duration, and health in the admin API and Prometheus. A failed compaction blocks release promotion until recovered.
- The immutable `sha-${GITHUB_SHA}` image is pushed first; container smoke, architecture checks, and N-1 upgrade/rollback smoke pass against that digest before any semver tag is created. Binaries and the GitHub Release depend on the smoke-tested image and archive artifacts.

## Release assets

- README, operations guide, security policy, changelog, examples, and license are current.
- No live API key, management key, capture key, prompt, response, or unredacted upstream error is present.
- The image contains OCI version, revision, created, source, and license labels.
- Linux amd64/arm64, Darwin amd64/arm64, and Windows amd64 release targets cross-build with `CGO_ENABLED=0`.
- The tag and image version are identical.
- The tag, `web/package.json`, lockfile root version, CHANGELOG section, Admin API v3, and image version are identical.
- CI and release builders use Go 1.25.x and `govulncheck` reports no reachable vulnerabilities.

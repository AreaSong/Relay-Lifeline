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
- Schema 1 and 2 configuration copies migrate to schema 3 with `max-response-body` and `max-total-cache` populated.
- A failed request enters `waiting`, emits heartbeats, retries, and delivers one complete response after recovery.
- Request and incident journals survive container recreation, verify successfully, compact expired entities, and restore unfinished requests only as `orphaned`.
- Continuous tasks stop at execution/failure limits, pause on the consecutive-failure circuit, and retain no more than 100 safe run audits.
- Webhook status, test delivery, queue counters, and the bounded delivery history work without storing payloads or target URLs.
- History and incident filters page through stable cursors; incident drill-down returns no more than 100 retained related requests.
- Realtime `sync`, changed-domain `update`, cursor replay, and retention-gap `reset` work; hidden pages suspend non-critical polling.
- Viewer cannot mutate, Operator cannot download raw content, and Sensitive Data still requires explicit raw-download confirmation.
- Completed captures remain readable after upgrade; key status reports zero unresolved records.
- The prior image starts with its matching prior configuration and can serve health and management endpoints.
- Scaled 4K (3840x2160), MacBook Pro 14-inch (1512x982), low-height desktop (1280x720), and mobile (390x844) views have no blank canvas, overlap, clipped controls, or role leakage.

## Release assets

- README, operations guide, security policy, changelog, examples, and license are current.
- No live API key, management key, capture key, prompt, response, or unredacted upstream error is present.
- The image contains OCI version, revision, created, source, and license labels.
- The tag and image version are identical.
- The tag, `web/package.json`, lockfile root version, CHANGELOG section, Admin API v3, and image version are identical.
- CI and release builders use Go 1.25.x and `govulncheck` reports no reachable vulnerabilities.

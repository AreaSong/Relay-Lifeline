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
./scripts/ci-integration.sh
docker build \
  --build-arg VERSION="${VERSION}" \
  --build-arg REVISION="${REVISION}" \
  --build-arg BUILD_TIME="${BUILD_TIME}" \
  -t "transfer-lifeline:${VERSION}" .
./scripts/container-smoke.sh "transfer-lifeline:${VERSION}"
```

## Runtime gates

- `/healthz` returns `ok`; `/readyz` returns `ready`.
- `/admin/api/meta` reports the expected version, revision, build time, API version, and config schema.
- All diagnostic checks pass without issuing a model request.
- HTTP `4xx`/`5xx`, DNS, connection refusal, header timeout, body idle timeout, malformed JSON, incomplete SSE, cancellation, and queue saturation are covered.
- A failed request enters `waiting`, emits heartbeats, retries, and delivers one complete response after recovery.
- Request and incident journals survive container recreation, verify successfully, compact expired entities, and restore unfinished requests only as `orphaned`.
- Viewer cannot mutate, Operator cannot download raw content, and Sensitive Data still requires explicit raw-download confirmation.
- Completed captures remain readable after upgrade; key status reports zero unresolved records.
- The prior image starts with its matching prior configuration and can serve health and management endpoints.
- Desktop 1440x900 and mobile 390x844 views have no blank canvas, overlap, clipped controls, or role leakage.

## Release assets

- README, operations guide, security policy, changelog, examples, and license are current.
- No live API key, management key, capture key, prompt, response, or unredacted upstream error is present.
- The image contains OCI version, revision, created, source, and license labels.
- The tag and image version are identical.

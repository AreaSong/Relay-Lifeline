#!/usr/bin/env bash
set -euo pipefail

image="${1:-relay-lifeline:ci}"
expected_version="${2:-}"
expected_revision="${3:-}"
expected_build_time="${4:-}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
name="relay-lifeline-smoke-${RANDOM}"
cleanup() {
  status=$?
  if [[ "${status}" -ne 0 ]]; then
    docker logs "${name}" 2>&1 || true
  fi
  docker rm -f "${name}" >/dev/null 2>&1 || true
  rm -rf "${work}"
}
trap cleanup EXIT
mkdir -p "${work}/captures" "${work}/events"
chmod 0777 "${work}/captures" "${work}/events"
cp "${root}/config.docker.example.yaml" "${work}/config.yaml"
sed -i.bak '/^metrics-export:$/,$ s/^  enabled: false$/  enabled: true/' "${work}/config.yaml"
rm "${work}/config.yaml.bak"

docker run -d --name "${name}" -p 127.0.0.1::8318 \
  -e RELAY_LIFELINE_ADMIN_KEY=container-operator-key-000001 \
  -e RELAY_LIFELINE_SENSITIVE_KEY=container-sensitive-key-000001 \
  -e RELAY_LIFELINE_IMAGE_REF="${image}" \
  -v "${work}/config.yaml:/etc/relay-lifeline/config.yaml:ro" \
  -v "${work}/captures:/var/lib/relay-lifeline/captures" \
  -v "${work}/events:/var/lib/relay-lifeline/events" \
  "${image}" >/dev/null
port="$(docker port "${name}" 8318/tcp | sed 's/.*://')"

for _ in {1..60}; do
  if curl --max-time 2 -fsS "http://127.0.0.1:${port}/readyz" >/dev/null; then break; fi
  sleep 0.25
done
curl --max-time 3 -fsS "http://127.0.0.1:${port}/healthz" | grep -q '"status":"ok"'
curl --max-time 3 -fsS "http://127.0.0.1:${port}/readyz" | grep -q '"status":"ready"'
meta="$(curl --max-time 3 -fsS -H 'Authorization: Bearer container-operator-key-000001' \
  "http://127.0.0.1:${port}/admin/api/meta")"
grep -q '"adminApiVersion":"3"' <<<"${meta}"
grep -q '"configSchemaVersion":3' <<<"${meta}"
grep -Fq "\"imageRef\":\"${image}\"" <<<"${meta}"
if [[ -n "${expected_version}" ]]; then
  grep -Fq "\"version\":\"${expected_version}\"" <<<"${meta}"
  [[ "$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' "${image}")" == "${expected_version}" ]]
fi
if [[ -n "${expected_revision}" ]]; then
  grep -Fq "\"revision\":\"${expected_revision}\"" <<<"${meta}"
  [[ "$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "${image}")" == "${expected_revision}" ]]
fi
if [[ -n "${expected_build_time}" ]]; then
  grep -Fq "\"builtAt\":\"${expected_build_time}\"" <<<"${meta}"
  [[ "$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.created" }}' "${image}")" == "${expected_build_time}" ]]
fi
curl --max-time 3 -fsS -H 'Authorization: Bearer container-operator-key-000001' \
  "http://127.0.0.1:${port}/admin/api/config" | grep -q '"schemaVersion":3'
docker exec "${name}" wget -T 3 -q -O - http://127.0.0.1:8318/metrics | grep -q 'relay_lifeline_journal_healthy{journal="requests"} 1'

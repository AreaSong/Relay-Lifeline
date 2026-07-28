#!/usr/bin/env bash
set -euo pipefail

image="${1:-transfer-lifeline:ci}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
name="transfer-lifeline-smoke-${RANDOM}"
cleanup() {
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
curl --max-time 3 -fsS -H 'Authorization: Bearer container-operator-key-000001' \
  "http://127.0.0.1:${port}/admin/api/meta" | grep -q '"configSchemaVersion":2'
curl --max-time 3 -fsS -H 'Authorization: Bearer container-operator-key-000001' \
  "http://127.0.0.1:${port}/admin/api/config" | grep -q '"schemaVersion":2'
docker exec "${name}" wget -T 3 -q -O - http://127.0.0.1:8318/metrics | grep -q 'relay_lifeline_journal_healthy{journal="requests"} 1'

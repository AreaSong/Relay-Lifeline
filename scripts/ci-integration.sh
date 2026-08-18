#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
relay_pid=""
fault_pid=""
cleanup() {
  status=$?
  if [[ "${status}" -ne 0 ]]; then
    for log in relay.log fault.log; do
      if [[ -f "${work}/${log}" ]]; then
        echo "===== ${log} =====" >&2
        tail -n 100 "${work}/${log}" >&2
      fi
    done
  fi
  if [[ -n "${relay_pid}" ]]; then kill "${relay_pid}" 2>/dev/null || true; fi
  if [[ -n "${fault_pid}" ]]; then kill "${fault_pid}" 2>/dev/null || true; fi
  rm -rf "${work}"
}
trap cleanup EXIT

build_flags=()
if [[ "$(uname -s)" == "Darwin" ]]; then
  build_flags=(-ldflags=-linkmode=external)
fi
go build "${build_flags[@]}" -o "${work}/relay-lifeline" "${root}/cmd/relay-lifeline"
go build "${build_flags[@]}" -o "${work}/fault-upstream" "${root}/cmd/fault-upstream"
cp "${root}/config.example.yaml" "${work}/config.yaml"
sed -i.bak \
  -e 's|127.0.0.1:8318|127.0.0.1:18318|' \
  -e 's|http://cli-proxy-api:8317|http://127.0.0.1:18317|' \
  -e 's|min-interval: "60s"|min-interval: "100ms"|' \
  -e 's|max-interval: "120s"|max-interval: "100ms"|' \
  -e "s|/var/lib/relay-lifeline/captures|${work}/captures|g" \
  -e "s|/var/lib/relay-lifeline/events|${work}/events|g" \
  "${work}/config.yaml"
rm "${work}/config.yaml.bak"

"${work}/relay-lifeline" -config "${work}/config.yaml" -config-validate
cp "${work}/config.yaml" "${work}/config-v2.yaml"
sed -i.bak \
  -e 's/schema-version: 3/schema-version: 2/' \
  -e '/max-response-body:/d' \
  -e '/max-total-cache:/d' \
  "${work}/config-v2.yaml"
rm "${work}/config-v2.yaml.bak"
"${work}/relay-lifeline" -config "${work}/config-v2.yaml" -config-migrate
grep -q 'schema-version: 3' "${work}/config-v2.yaml"
grep -q 'max-response-body: 512MiB' "${work}/config-v2.yaml"
cp "${work}/config.yaml" "${work}/config-v1.yaml"
sed -i.bak \
  -e 's/schema-version: 3/schema-version: 1/' \
  -e '/max-response-body:/d' \
  -e '/max-total-cache:/d' \
  "${work}/config-v1.yaml"
rm "${work}/config-v1.yaml.bak"
"${work}/relay-lifeline" -config "${work}/config-v1.yaml" -config-migrate
"${work}/relay-lifeline" -config "${work}/config-v1.yaml" -recovery-check > "${work}/recovery.json"
"${work}/relay-lifeline" -journal-verify "${work}/events/not-created.jsonl"

"${work}/fault-upstream" -listen 127.0.0.1:18317 -sequence 401,429,503,invalid-json,truncated-json,success > "${work}/fault.log" 2>&1 &
fault_pid=$!
RELAY_LIFELINE_ADMIN_KEY=integration-operator-key-0001 \
RELAY_LIFELINE_SENSITIVE_KEY=integration-sensitive-key-0001 \
  "${work}/relay-lifeline" -config "${work}/config.yaml" > "${work}/relay.log" 2>&1 &
relay_pid=$!

for _ in {1..50}; do
  if curl -fsS http://127.0.0.1:18318/readyz >/dev/null; then break; fi
  sleep 0.1
done
curl -fsS http://127.0.0.1:18318/readyz | grep -q '"status":"ready"'
curl -fsS -H 'Content-Type: application/json' \
  -d '{"model":"fault-drill","input":"integration","stream":false}' \
  http://127.0.0.1:18318/v1/responses | grep -q '"status":"completed"'
grep -q 'request=6 scenario=success' "${work}/fault.log"
"${work}/relay-lifeline" -journal-verify "${work}/events/requests.jsonl"

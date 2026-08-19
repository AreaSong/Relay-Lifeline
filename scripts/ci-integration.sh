#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
relay_pid=""
fault_pid=""
fault_pid_secondary=""
client_pid=""
cleanup() {
  status=$?
  if [[ "${status}" -ne 0 ]]; then
	for log in "${work}"/*.log; do
		if [[ -f "${log}" ]]; then
			echo "===== $(basename "${log}") =====" >&2
			tail -n 100 "${log}" >&2
		fi
	done
  fi
	if [[ -n "${client_pid}" ]]; then kill "${client_pid}" 2>/dev/null || true; fi
  if [[ -n "${relay_pid}" ]]; then kill "${relay_pid}" 2>/dev/null || true; fi
  if [[ -n "${fault_pid}" ]]; then kill "${fault_pid}" 2>/dev/null || true; fi
	if [[ -n "${fault_pid_secondary}" ]]; then kill "${fault_pid_secondary}" 2>/dev/null || true; fi
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

migrate_schema() {
  schema="$1"
  fixture="${work}/config-v${schema}.yaml"
  output="${work}/migrate-v${schema}.log"
  cp "${work}/config.yaml" "${fixture}"
  sed -i.bak -e "s/schema-version: 5/schema-version: ${schema}/" "${fixture}"
  rm "${fixture}.bak"
  if [[ "${schema}" -le 2 ]]; then
    sed -i.bak -e '/max-response-body:/d' -e '/max-total-cache:/d' "${fixture}"
    rm "${fixture}.bak"
  fi
  "${work}/relay-lifeline" -config "${fixture}" -config-migrate | tee "${output}"
  "${work}/relay-lifeline" -config "${fixture}" -config-validate
  grep -q 'schema-version: 5' "${fixture}"
  grep -q 'max-response-body: 512MiB' "${fixture}"
  grep -q 'local-access-enabled: true' "${fixture}"
  grep -q 'session-max-lifetime: 8h' "${fixture}"
  backup="$(sed -n 's/.* backup=//p' "${output}")"
  test -f "${backup}"
  grep -q "schema-version: ${schema}" "${backup}"
}

for schema in 1 2 3 4; do
  migrate_schema "${schema}"
done

"${work}/relay-lifeline" -config "${work}/config-v1.yaml" -recovery-check > "${work}/recovery.json"
"${work}/relay-lifeline" -journal-verify "${work}/events/not-created.jsonl"

stop_runtime() {
  if [[ -n "${client_pid}" ]]; then kill "${client_pid}" 2>/dev/null || true; wait "${client_pid}" 2>/dev/null || true; client_pid=""; fi
  if [[ -n "${relay_pid}" ]]; then kill "${relay_pid}" 2>/dev/null || true; wait "${relay_pid}" 2>/dev/null || true; relay_pid=""; fi
  if [[ -n "${fault_pid}" ]]; then kill "${fault_pid}" 2>/dev/null || true; wait "${fault_pid}" 2>/dev/null || true; fault_pid=""; fi
  if [[ -n "${fault_pid_secondary}" ]]; then kill "${fault_pid_secondary}" 2>/dev/null || true; wait "${fault_pid_secondary}" 2>/dev/null || true; fault_pid_secondary=""; fi
}

scenario_config() {
  name="$1"
  target="${work}/${name}.yaml"
  cp "${work}/config.yaml" "${target}"
  sed -i.bak \
    -e 's|response-header-timeout: "30s"|response-header-timeout: "200ms"|' \
    -e 's|response-body-idle-timeout: "90s"|response-body-idle-timeout: "200ms"|' \
    -e 's|max-attempts: 0|max-attempts: 8|' \
    -e 's|max-elapsed: "0s"|max-elapsed: "5s"|' \
    -e 's|honor-retry-after: true|honor-retry-after: false|' \
    -e 's|heartbeat-interval: "15s"|heartbeat-interval: "50ms"|' \
    -e 's|recovery-spacing: "2s"|recovery-spacing: "0s"|' \
    -e "s|${work}/captures|${work}/${name}-captures|g" \
    -e "s|${work}/events|${work}/${name}-events|g" \
    "${target}"
  rm "${target}.bak"
  echo "${target}"
}

append_pool() {
  target="$1"
  primary_url="$2"
  primary_domain="$3"
  secondary_url="$4"
  secondary_domain="$5"
  cat >> "${target}" <<EOF

upstreams:
  strategy: "weighted-priority"
  targets:
    - id: "primary"
      base-url: "${primary_url}"
      priority: 0
      weight: 1
      max-active: 0
      idempotency-domain: "${primary_domain}"
    - id: "secondary"
      base-url: "${secondary_url}"
      priority: 1
      weight: 1
      max-active: 0
      idempotency-domain: "${secondary_domain}"
  health:
    mode: "passive"
  circuit:
    enabled: true
    minimum-requests: 1
    failure-percent: 1
    open-duration: "5s"
    half-open-max: 1
EOF
}

start_fault_primary() {
  name="$1"
  sequence="$2"
  shift 2
  "${work}/fault-upstream" -listen 127.0.0.1:18317 -name primary -events "${work}/${name}-primary.jsonl" -sequence "${sequence}" "$@" > "${work}/${name}-primary.log" 2>&1 &
  fault_pid=$!
  sleep 0.1
  kill -0 "${fault_pid}"
}

start_fault_secondary() {
  name="$1"
  sequence="$2"
  shift 2
  "${work}/fault-upstream" -listen 127.0.0.1:18319 -name secondary -events "${work}/${name}-secondary.jsonl" -sequence "${sequence}" "$@" > "${work}/${name}-secondary.log" 2>&1 &
  fault_pid_secondary=$!
  sleep 0.1
  kill -0 "${fault_pid_secondary}"
}

start_relay() {
  name="$1"
  config_path="$2"
  RELAY_LIFELINE_ADMIN_KEY=integration-operator-key-0001 \
  RELAY_LIFELINE_SENSITIVE_KEY=integration-sensitive-key-0001 \
    "${work}/relay-lifeline" -config "${config_path}" > "${work}/${name}-relay.log" 2>&1 &
  relay_pid=$!
  for _ in {1..100}; do
    if curl -fsS http://127.0.0.1:18318/readyz >/dev/null 2>&1; then
      return
    fi
    if ! kill -0 "${relay_pid}" 2>/dev/null; then
      return 1
    fi
    sleep 0.1
  done
  return 1
}

admin_status() {
  curl -fsS -H 'Authorization: Bearer integration-operator-key-0001' http://127.0.0.1:18318/admin/api/status
}

wait_for_active_zero() {
  for _ in {1..50}; do
    if admin_status | grep -q '"active":0'; then return; fi
    sleep 0.1
  done
  return 1
}

name="baseline"
cfg="$(scenario_config "${name}")"
start_fault_primary "${name}" '401,429,503,invalid-json,truncated-json,success'
start_relay "${name}" "${cfg}"
curl -fsS -H 'Content-Type: application/json' -d '{"model":"fault-drill","input":"integration","stream":false}' http://127.0.0.1:18318/v1/responses > "${work}/${name}-response.json"
grep -q '"status":"completed"' "${work}/${name}-response.json"
test "$(wc -l < "${work}/${name}-primary.jsonl" | tr -d ' ')" = 6
grep -q '"scenario":"success"' "${work}/${name}-primary.jsonl"
stop_runtime
"${work}/relay-lifeline" -journal-verify "${work}/${name}-events/requests.jsonl"

name="stream-faults"
cfg="$(scenario_config "${name}")"
# 本场景验证流式故障恢复，不验证 uncertain delivery 人工闭环；显式允许
# 已写出请求在响应头超时后继续重试。
sed -i.bak -e 's|allow-uncertain-retry: false|allow-uncertain-retry: true|' "${cfg}"
rm "${cfg}.bak"
start_fault_primary "${name}" 'timeout,truncated-sse,stalled-sse,success-sse' -delay 5s
start_relay "${name}" "${cfg}"
curl -fsS --max-time 10 -H 'Content-Type: application/json' -d '{"model":"fault-drill","input":"integration","stream":true}' http://127.0.0.1:18318/v1/responses > "${work}/${name}-response.sse"
grep -q 'response.completed' "${work}/${name}-response.sse"
if grep -Eq 'partial|stalled' "${work}/${name}-response.sse"; then exit 1; fi
test "$(wc -l < "${work}/${name}-primary.jsonl" | tr -d ' ')" = 4
grep -q '"scenario":"timeout".*"contextCanceled":true' "${work}/${name}-primary.jsonl"
grep -q '"scenario":"stalled-sse".*"contextCanceled":true' "${work}/${name}-primary.jsonl"
stop_runtime

name="slow-io"
cfg="$(scenario_config "${name}")"
start_fault_primary "${name}" 'slow-upload,slow-download,slow-sse' -chunk-delay 20ms -read-chunk 8
start_relay "${name}" "${cfg}"
curl -fsS -H 'Content-Type: application/json' -d '{"model":"fault-drill","input":"slow upload payload","stream":false}' http://127.0.0.1:18318/v1/responses | grep -q '"status":"completed"'
curl -fsS -H 'Content-Type: application/json' -d '{"model":"fault-drill","input":"slow download","stream":false}' http://127.0.0.1:18318/v1/responses | grep -q '"status":"completed"'
curl -fsS -H 'Content-Type: application/json' -d '{"model":"fault-drill","input":"slow sse","stream":true}' http://127.0.0.1:18318/v1/responses | grep -q 'response.completed'
test "$(wc -l < "${work}/${name}-primary.jsonl" | tr -d ' ')" = 3
stop_runtime

name="client-cancel"
cfg="$(scenario_config "${name}")"
sed -i.bak -e 's|response-body-idle-timeout: "200ms"|response-body-idle-timeout: "10s"|' "${cfg}"
rm "${cfg}.bak"
start_fault_primary "${name}" 'stalled-sse' -delay 10s
start_relay "${name}" "${cfg}"
if curl -fsS --max-time 1 -H 'Content-Type: application/json' -d '{"model":"fault-drill","input":"cancel","stream":true}' http://127.0.0.1:18318/v1/responses > "${work}/${name}-response.sse" 2>/dev/null; then exit 1; fi
for _ in {1..30}; do
  if grep -q '"contextCanceled":true' "${work}/${name}-primary.jsonl" 2>/dev/null; then break; fi
  sleep 0.1
done
grep -q '"contextCanceled":true' "${work}/${name}-primary.jsonl"
wait_for_active_zero
stop_runtime

name="queue-saturation"
cfg="$(scenario_config "${name}")"
sed -i.bak \
  -e 's|max-active: 8|max-active: 1|' \
  -e 's|max-waiting: 100|max-waiting: 0|' \
  -e 's|response-body-idle-timeout: "200ms"|response-body-idle-timeout: "10s"|' \
  "${cfg}"
rm "${cfg}.bak"
start_fault_primary "${name}" 'stalled-sse' -delay 10s
start_relay "${name}" "${cfg}"
curl -sS --max-time 2 -H 'Content-Type: application/json' -d '{"model":"fault-drill","input":"first","stream":true}' http://127.0.0.1:18318/v1/responses > "${work}/${name}-first.sse" 2>/dev/null &
client_pid=$!
for _ in {1..30}; do
  if grep -q 'request=1 scenario=stalled-sse' "${work}/${name}-primary.log"; then break; fi
  sleep 0.1
done
curl -sS --max-time 2 -H 'Accept-Language: en-US' -H 'Content-Type: application/json' -d '{"model":"fault-drill","input":"second","stream":false}' http://127.0.0.1:18318/v1/responses > "${work}/${name}-second.json"
grep -q 'The waiting queue is full' "${work}/${name}-second.json"
kill "${client_pid}" 2>/dev/null || true
wait "${client_pid}" 2>/dev/null || true
client_pid=""
wait_for_active_zero
stop_runtime
test "$(wc -l < "${work}/${name}-primary.jsonl" | tr -d ' ')" = 1

run_failover() {
  name="$1"
  primary_url="$2"
  primary_domain="$3"
  secondary_domain="$4"
  allow_cross="$5"
  key="$6"
  expect_success="$7"
  cfg="$(scenario_config "${name}")"
  append_pool "${cfg}" "${primary_url}" "${primary_domain}" 'http://127.0.0.1:18319' "${secondary_domain}"
  if [[ "${allow_cross}" == "true" ]]; then
    sed -i.bak -e 's|allow-cross-domain-failover: false|allow-cross-domain-failover: true|' "${cfg}"
    rm "${cfg}.bak"
  fi
  if [[ "${primary_url}" == 'http://127.0.0.1:18317' ]]; then
	# failover 场景单独验证幂等域边界，因此先允许 uncertain attempt
	# 进入下一次目标选择；生产默认仍保持人工确认。
	sed -i.bak -e 's|allow-uncertain-retry: false|allow-uncertain-retry: true|' "${cfg}"
	rm "${cfg}.bak"
    start_fault_primary "${name}" 'timeout' -delay 5s
  fi
  start_fault_secondary "${name}" 'success'
  start_relay "${name}" "${cfg}"
  headers=(-H 'Content-Type: application/json' -H 'Accept-Language: en-US')
  if [[ -n "${key}" ]]; then headers+=(-H "Idempotency-Key: ${key}"); fi
  curl -fsS --max-time 8 "${headers[@]}" -d '{"model":"fault-drill","input":"failover","stream":false}' http://127.0.0.1:18318/v1/responses > "${work}/${name}-response.json"
  if [[ "${expect_success}" == "true" ]]; then
    grep -q '"status":"completed"' "${work}/${name}-response.json"
    grep -q '"scenario":"success"' "${work}/${name}-secondary.jsonl"
    if [[ -n "${key}" ]]; then grep -q "\"idempotencyKey\":\"${key}\"" "${work}/${name}-secondary.jsonl"; fi
  else
    grep -Eq 'failover|safe|安全' "${work}/${name}-response.json"
    if [[ -s "${work}/${name}-secondary.jsonl" ]]; then exit 1; fi
  fi
  stop_runtime
}

run_failover 'connect-refused' 'http://127.0.0.1:18999' 'same' 'same' false '' true
run_failover 'same-domain-key' 'http://127.0.0.1:18317' 'same' 'same' false 'stable-key' true
run_failover 'same-domain-no-key' 'http://127.0.0.1:18317' 'same' 'same' false '' false
run_failover 'cross-domain-denied' 'http://127.0.0.1:18317' 'primary-domain' 'secondary-domain' false 'stable-cross-key' false
run_failover 'cross-domain-allowed' 'http://127.0.0.1:18317' 'primary-domain' 'secondary-domain' true 'stable-cross-key' true

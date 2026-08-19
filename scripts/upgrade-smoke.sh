#!/usr/bin/env bash
set -euo pipefail

previous_image="${1:?previous image is required}"
current_image="${2:?current image is required}"
previous_ref="${3:?previous git ref is required}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
previous_name="relay-lifeline-upgrade-previous-${RANDOM}"
current_name="relay-lifeline-upgrade-current-${RANDOM}"
cleanup() {
	status=$?
	if [[ "${status}" -ne 0 ]]; then
		docker logs "${previous_name}" 2>&1 || true
		docker logs "${current_name}" 2>&1 || true
	fi
	docker rm -f "${previous_name}" "${current_name}" >/dev/null 2>&1 || true
	rm -rf "${work}"
}
trap cleanup EXIT

mkdir -p "${work}/captures" "${work}/events"
chmod 0777 "${work}/captures" "${work}/events"
git -C "${root}" show "${previous_ref}:config.docker.example.yaml" > "${work}/config.yaml"
cp "${work}/config.yaml" "${work}/rollback.yaml"
chmod 0666 "${work}/config.yaml" "${work}/rollback.yaml"

ensure_image() {
	local image="$1"
	# CI always pulls; local mode is an explicit opt-in for offline archive checks.
	if [[ "${RELAY_LIFELINE_USE_LOCAL_IMAGES:-false}" == "true" ]]; then
		if ! docker image inspect "${image}" >/dev/null 2>&1; then
			echo "Local image is not available: ${image}" >&2
			return 1
		fi
		return 0
	fi
	docker pull "${image}"
}

ensure_image "${previous_image}"
ensure_image "${current_image}"

run_and_wait() {
	name="$1"
	image="$2"
	docker run -d --name "${name}" -p 127.0.0.1::8318 \
		-e RELAY_LIFELINE_ADMIN_KEY=upgrade-operator-key-000001 \
		-e RELAY_LIFELINE_SENSITIVE_KEY=upgrade-sensitive-key-000001 \
		-e RELAY_LIFELINE_IMAGE_REF="${image}" \
		-v "${work}/config.yaml:/etc/relay-lifeline/config.yaml:ro" \
		-v "${work}/captures:/var/lib/relay-lifeline/captures" \
		-v "${work}/events:/var/lib/relay-lifeline/events" \
		"${image}" >/dev/null
	port="$(docker port "${name}" 8318/tcp | sed 's/.*://')"
	for _ in {1..60}; do
		if curl --max-time 2 -fsS "http://127.0.0.1:${port}/readyz" >/dev/null 2>&1; then
			printf '%s\n' "${port}"
			return
		fi
		sleep 0.25
	done
	return 1
}

previous_port="$(run_and_wait "${previous_name}" "${previous_image}")"
curl --max-time 3 -fsS "http://127.0.0.1:${previous_port}/healthz" | grep -q '"status":"ok"'
curl --max-time 3 -fsS "http://127.0.0.1:${previous_port}/readyz" | grep -q '"status":"ready"'
docker rm -f "${previous_name}" >/dev/null

docker run --rm \
	-v "${work}/config.yaml:/etc/relay-lifeline/config.yaml:rw" \
	-v "${work}/captures:/var/lib/relay-lifeline/captures" \
	-v "${work}/events:/var/lib/relay-lifeline/events" \
	"${current_image}" -config /etc/relay-lifeline/config.yaml -config-migrate
grep -q '^schema-version: 5' "${work}/config.yaml"
find "${work}/captures" -name 'config-*.yaml' -type f -print -quit | grep -q .

current_port="$(run_and_wait "${current_name}" "${current_image}")"
current_meta="$(curl --max-time 3 -fsS -H 'Authorization: Bearer upgrade-operator-key-000001' "http://127.0.0.1:${current_port}/admin/api/meta")"
grep -q '"configSchemaVersion":5' <<<"${current_meta}"
curl --max-time 3 -fsS "http://127.0.0.1:${current_port}/healthz" | grep -q '"status":"ok"'
docker rm -f "${current_name}" >/dev/null

cp "${work}/rollback.yaml" "${work}/config.yaml"
chmod 0666 "${work}/config.yaml"
rollback_port="$(run_and_wait "${previous_name}" "${previous_image}")"
curl --max-time 3 -fsS "http://127.0.0.1:${rollback_port}/healthz" | grep -q '"status":"ok"'
curl --max-time 3 -fsS "http://127.0.0.1:${rollback_port}/readyz" | grep -q '"status":"ready"'

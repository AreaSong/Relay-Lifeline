#!/usr/bin/env bash
set -euo pipefail

archive="${1:?archive is required}"
version="${2:?version is required}"
verify_only="${3:-false}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

case "${archive}" in
	*.tar.gz)
		tar -tzf "${archive}" >/dev/null
		# Every archive must carry the executable and release documentation. Only
		# linux/amd64 is executed on the GitHub runner below.
		tar -tzf "${archive}" | grep -Eq '/relay-lifeline$'
		tar -tzf "${archive}" | grep -Eq '/README(\.zh-CN)?\.md$'
		tar -tzf "${archive}" | grep -Eq '/LICENSE$'
		if [[ "${verify_only}" == "true" ]]; then
			exit 0
		fi
		tar -xzf "${archive}" -C "${work}"
		binary="$(find "${work}" -type f -name relay-lifeline -perm -u+x -print -quit)"
		if [[ -z "${binary}" ]]; then
			echo "release archive does not contain an executable relay-lifeline" >&2
			exit 1
		fi
		"${binary}" -version | grep -Fq "${version} revision="
		cp "${root}/config.example.yaml" "${work}/config.yaml"
		"${binary}" -config "${work}/config.yaml" -config-validate
		cp "${work}/config.yaml" "${work}/schema4.yaml"
		sed -i.bak -e 's/schema-version: 5/schema-version: 4/' "${work}/schema4.yaml"
		rm "${work}/schema4.yaml.bak"
		"${binary}" -config "${work}/schema4.yaml" -config-migrate
		grep -q '^schema-version: 5' "${work}/schema4.yaml"
		;;
	*.zip)
		unzip -tq "${archive}"
		unzip -l "${archive}" | grep -Eq 'relay-lifeline-[^/]*/relay-lifeline(\.exe)?'
		unzip -l "${archive}" | grep -q 'relay-lifeline-.*README.md'
		unzip -l "${archive}" | grep -q 'relay-lifeline-.*LICENSE'
		;;
	*)
		echo "unsupported release archive: ${archive}" >&2
		exit 1
		;;
esac

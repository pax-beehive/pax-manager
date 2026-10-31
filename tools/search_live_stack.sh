#!/usr/bin/env bash
set -euo pipefail

search_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${search_root}"
search_action="${1:-test}"
search_project="${2:-pax-search-live-$(date +%s)-$$}"
if [[ ! "${search_project}" =~ ^pax-search-live-[a-z0-9-]+$ ]]; then
  printf 'Refusing to operate on a project outside the pax-search-live namespace.\n' >&2
  exit 2
fi
if [[ "${search_action}" != test && "${search_action}" != up && "${search_action}" != down ]]; then
  printf 'Usage: %s {test|up|down} [pax-search-live-project]\n' "$0" >&2
  exit 2
fi
for search_command in docker python3; do
  command -v "${search_command}" >/dev/null || { printf 'Missing command: %s\n' "${search_command}" >&2; exit 1; }
done
docker compose version >/dev/null
docker info >/dev/null 2>&1 || { printf 'Docker must be running.\n' >&2; exit 1; }
# The local receipt signing key is unrelated to production and never printed.
export SEARCH_LIVE_RECEIPT_SECRET="$(python3 -c 'import secrets; print(secrets.token_hex(32))')"
search_compose=(docker compose -f docker-compose.search-live.yml -p "${search_project}")
if [[ "${search_action}" == down ]]; then
  "${search_compose[@]}" down -v --remove-orphans
  exit
fi
search_logs="$(mktemp -d "${TMPDIR:-/tmp}/pax-search-live.XXXXXX")"
search_keep=false
cleanup() {
  search_exit=$?
  trap - EXIT
  if [[ "${search_keep}" != true ]]; then
    "${search_compose[@]}" logs --no-color >"${search_logs}/services.log" 2>&1 || true
    if ! "${search_compose[@]}" down -v --remove-orphans >"${search_logs}/cleanup.log" 2>&1; then
      printf 'Cleanup failed for %s. Logs: %s\n' "${search_project}" "${search_logs}" >&2
      search_exit=1
    fi
  fi
  if [[ "${search_exit}" != 0 ]]; then
    printf 'Local search stack failed. Logs: %s\n' "${search_logs}" >&2
  fi
  exit "${search_exit}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
printf 'Building and starting isolated Manager, PostgreSQL 16 and Elasticsearch...\n'
if ! "${search_compose[@]}" up --build -d --wait --wait-timeout 240 >"${search_logs}/startup.log" 2>&1; then
  tail -n 60 "${search_logs}/startup.log" >&2
  exit 1
fi
search_address="$("${search_compose[@]}" port manager 9879)"
if [[ ! "${search_address}" =~ ^127\.0\.0\.1:[0-9]+$ ]]; then
  printf 'Unexpected Manager endpoint; only loopback is allowed.\n' >&2
  exit 1
fi
search_arguments=(--base-url "http://${search_address}" --project "${search_project}")
if [[ "${search_action}" == up ]]; then search_arguments+=(--seed-only); fi
# Trace uses only the standard library and combines guard tests with real I/O.
search_trace=(-m trace --count --file "${search_logs}/coverage.dat" --coverdir "${search_logs}/coverage")
PYTHONDONTWRITEBYTECODE=1 python3 "${search_trace[@]}" --module unittest discover -s tools \
  -p test_search_live_acceptance.py >"${search_logs}/guard-tests.log" 2>&1 || {
  cat "${search_logs}/guard-tests.log" >&2
  exit 1
}
PYTHONDONTWRITEBYTECODE=1 python3 "${search_trace[@]}" tools/search_live_acceptance.py "${search_arguments[@]}"
printf 'Manager: http://%s\nProject: %s\nLogs: %s\n' "${search_address}" "${search_project}" "${search_logs}"
if [[ "${search_action}" == up ]]; then
  search_keep=true
  printf 'Fixture owner: local@example.local\nStop: ./tools/search_live_stack.sh down %s\n' "${search_project}"
else
  printf 'Removing only this run\047s containers and volumes.\n'
fi

#!/usr/bin/env bash
set -euo pipefail

search_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${search_root}"
for search_command in docker go python3; do
  command -v "${search_command}" >/dev/null || {
    printf 'Required command is unavailable: %s\n' "${search_command}" >&2
    exit 1
  }
done
docker compose version >/dev/null
docker info >/dev/null 2>&1 || { printf 'Docker must be running.\n' >&2; exit 1; }

search_project="pax-search-acceptance-$(date +%s)-$$"
search_logs="$(mktemp -d "${TMPDIR:-/tmp}/pax-search-acceptance.XXXXXX")"
search_compose=(docker compose -f docker-compose.search-test.yml -p "${search_project}")
export SEARCH_TEST_PORT=0 SEARCH_TEST_PG_PORT=0
export GOWORK=off GOCACHE="${GOCACHE:-/tmp/pax-manager-go-cache}"

cleanup() {
  search_exit=$?
  trap - EXIT
  if ! "${search_compose[@]}" down -v --remove-orphans >"${search_logs}/cleanup.log" 2>&1; then
    printf 'Cleanup failed; see %s/cleanup.log (project %s).\n' "${search_logs}" "${search_project}" >&2
    search_exit=1
  fi
  if [ "${search_exit}" -ne 0 ]; then
    printf 'Acceptance failed. Logs: %s\n' "${search_logs}" >&2
  fi
  exit "${search_exit}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

printf 'Starting isolated PostgreSQL and Elasticsearch on random loopback ports...\n'
if ! "${search_compose[@]}" up -d --wait --wait-timeout 240 postgres elasticsearch >"${search_logs}/startup.log" 2>&1; then
  tail -n 60 "${search_logs}/startup.log" >&2
  exit 1
fi
search_pg_address="$("${search_compose[@]}" port postgres 5432)"
search_es_address="$("${search_compose[@]}" port elasticsearch 9200)"
if [[ ! "${search_pg_address}" =~ ^127\.0\.0\.1:[0-9]+$ ]] || [[ ! "${search_es_address}" =~ ^127\.0\.0\.1:[0-9]+$ ]]; then
  printf 'Unexpected test endpoint; refusing to use an external database.\n' >&2
  exit 1
fi
export PAX_MANAGER_SEARCH_TEST_DATABASE_URL="postgres://pax:pax@${search_pg_address}/search_test?sslmode=disable"
export SEARCH_TEST_URL="http://${search_es_address}"

printf 'Running message, HTTP, real PG/ES, and summary acceptance checks...\n'
if ! go test -json -tags=integration -count=1 -timeout=180s \
  ./internal/manager/domain ./internal/manager/config ./internal/manager/search \
  ./internal/manager/storage ./internal/manager/userapi ./internal/manager \
  -run 'Test(CanonicalSearch|Search|Summary|Worker|Workspace|Elasticsearch|LoadSearch|ReadReceiptSecret)' \
  >"${search_logs}/go.jsonl" 2>"${search_logs}/go.stderr"; then
  tail -n 60 "${search_logs}/go.jsonl" >&2
  cat "${search_logs}/go.stderr" >&2
  exit 1
fi
python3 - "${search_logs}/go.jsonl" <<'PY'
import json
import sys

events = [json.loads(line) for line in open(sys.argv[1], encoding="utf-8") if line.startswith("{")]
passed = {item.get("Test") for item in events if item.get("Action") == "pass"}
skipped = [item.get("Test", item.get("Package")) for item in events if item.get("Action") == "skip"]
if skipped:
    raise SystemExit("Acceptance must not skip selected checks: " + ", ".join(skipped))
required = {
    "TestCanonicalSearchMessageGivenMultipartWhenRepartitionedThenIdentityAndHashStayStable": "Multipart assembly and stable full-body hash",
    "TestElasticsearchLiteralSearchVersionsAndPagination": "Real ES: Chinese substring, single character, literal input, pagination",
    "TestSearchPostgresGivenMultipartChangesWhenClaimedThenDurableVersionCannotBeLost": "Real PG: incremental parts, durable sync and version acknowledgements",
    "TestWorkspaceHTTPGivenMultipartWhenSearchedAndReadThenReturnsStableOwnerScopedReference": "HTTP routes, source reads and owner isolation",
    "TestSummaryPostgresGivenConcurrentRetriesThenExactlyOneVersionIsPublished": "Concurrent summary retries publish exactly one version",
    "TestWorkspaceMVPIntegrationGivenSourceChangesThenSearchAndSummaryHistoryStayConsistent": "Real PG + ES: summary v1/v2, exact old body, user isolation and deletion",
}
missing = set(required) - passed
if missing:
    raise SystemExit("Required acceptance checks did not pass: " + ", ".join(sorted(missing)))
for description in required.values():
    print("PASS " + description)
PY

if ! PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tools \
  -p 'test_secretary_search_mcp.py' -v >"${search_logs}/mcp.log" 2>&1; then
  cat "${search_logs}/mcp.log" >&2
  exit 1
fi
printf 'PASS Secretary MCP adapter and stdio protocol\n'
printf 'All acceptance checks passed. Logs: %s\n' "${search_logs}"
printf 'Removing only this run\047s test containers and volumes.\n'

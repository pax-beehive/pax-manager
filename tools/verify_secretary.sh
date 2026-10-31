#!/usr/bin/env bash
set -euo pipefail
umask 077

secretary_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${secretary_root}"
for secretary_command in docker go python3; do
  command -v "${secretary_command}" >/dev/null || { printf 'Missing command: %s\n' "${secretary_command}" >&2; exit 1; }
done
docker info >/dev/null 2>&1 || { printf 'Docker must be running.\n' >&2; exit 1; }
secretary_container="pax-secretary-acceptance-$(date +%s)-$$"
secretary_logs="$(mktemp -d "${TMPDIR:-/tmp}/pax-secretary-acceptance.XXXXXX")"
cleanup() {
  secretary_exit=$?
  trap - EXIT
  docker logs "${secretary_container}" >"${secretary_logs}/postgres.log" 2>&1 || true
  if docker container inspect "${secretary_container}" >/dev/null 2>&1; then
    if ! docker rm -f -v "${secretary_container}" >"${secretary_logs}/cleanup.log" 2>&1; then
      printf 'Cleanup failed for %s\n' "${secretary_container}" >&2
      secretary_exit=1
    fi
  fi
  printf 'Secretary acceptance logs: %s\n' "${secretary_logs}"
  exit "${secretary_exit}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
docker run -d --name "${secretary_container}" \
  -e POSTGRES_USER=pax -e POSTGRES_PASSWORD=pax -e POSTGRES_DB=secretary_test \
  -p 127.0.0.1::5432 postgres:16-alpine >"${secretary_logs}/startup.log"
secretary_ready=false
for secretary_attempt in {1..60}; do
  if docker exec "${secretary_container}" pg_isready -U pax -d secretary_test >/dev/null 2>&1; then
    secretary_ready=true
    break
  fi
  sleep 1
done
if [[ "${secretary_ready}" != true ]]; then
  printf 'Isolated PostgreSQL did not become ready.\n' >&2
  exit 1
fi
secretary_address="$(docker port "${secretary_container}" 5432/tcp)"
if [[ ! "${secretary_address}" =~ ^127\.0\.0\.1:[0-9]+$ ]]; then
  printf 'Refusing a database endpoint outside loopback.\n' >&2
  exit 1
fi
export PAX_MANAGER_SECRETARY_TEST_DATABASE_URL="postgres://pax:pax@${secretary_address}/secretary_test?sslmode=disable"
export PAX_NOTIFICATIONS_TEST_DATABASE_URL="${PAX_MANAGER_SECRETARY_TEST_DATABASE_URL}"
export GOWORK=off
export GOCACHE="${GOCACHE:-/tmp/pax-manager-go-cache}"
printf 'Verifying Secretary settings, real ACP execution, immutable summaries and PG checkpoints...\n'
if ! go test -json -tags=integration -count=1 -timeout=120s \
  ./internal/manager ./internal/manager/storage ./internal/manager/userapi \
  -run '^TestSecretary' >"${secretary_logs}/go.json" 2>&1; then
  tail -n 60 "${secretary_logs}/go.json" >&2
  exit 1
fi
if ! go test -json -count=1 ./internal/manager/secretary >"${secretary_logs}/worker.json" 2>&1; then
  tail -n 60 "${secretary_logs}/worker.json" >&2
  exit 1
fi
python3 - "${secretary_logs}" <<'PY'
import json
from pathlib import Path
import sys

passed, skipped = set(), set()
for name in ("go.json", "worker.json"):
    for line in (Path(sys.argv[1]) / name).read_text().splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("Test"):
            if event.get("Action") == "pass":
                passed.add(event["Test"])
            if event.get("Action") == "skip":
                skipped.add(event["Test"])
required = {
    "TestSecretaryHTTPGivenOwnerThenSettingsArePersistentAndDefaultOff",
    "TestSecretaryAgentGivenOwnedAgentThenRunsRealACPAndRecoversCompletedOutput",
    "TestSecretaryAgentGivenDisabledRunThenSendsNativeCancel",
    "TestSecretaryAutomationPostgresGivenOwnedAgentThenHTTPAndACPCommitFrozenVersions",
    "TestSecretaryPostgresGivenDisableWhenInFlightThenPublicationIsFencedAtomically",
    "TestSecretaryPostgresGivenPublicationThenCoverageCheckpointsAndVersionCommitTogether",
    "TestSecretaryProgressHTTPGivenOwnerThenReturnsGlobalCountsAndBoundedPage",
    "TestSecretaryProgressPostgresGivenLateDisabledPartsThenReportsExactCurrentCoverage",
    "TestSecretaryProgressPostgresGivenHiddenSourcesThenExcludesThemFromProgress",
    "TestSecretaryNotificationsGivenInternalExecutionThenSkipsOnlyNormalCompletionPush",
    "TestSecretaryNotificationsGivenInternalToolApprovalThenStillNotifiesActionableRequest",
    "TestSecretaryNotificationsGivenQueuedCompletionThenInternalMarkerFencesDelivery",
}
if skipped or required - passed:
    raise SystemExit(f"Acceptance incomplete: skipped={sorted(skipped)}, missing={sorted(required - passed)}")
print(f"PASS {len(passed)} Secretary checks, including actual HTTP/ACP/PostgreSQL; no skipped tests")
PY

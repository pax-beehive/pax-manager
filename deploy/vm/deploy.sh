#!/usr/bin/env bash
set -euo pipefail

IMAGE="${1:?image is required}"
DB_SECRET="${2:-pax-manager-database-url}"
DEEPSEEK_SECRET="${3:-deepseek_api_key}"
SESSION_ARTIFACT_GCS_BUCKET="${4:?session artifact GCS bucket is required}"
PAXD_ARTIFACT_SIGNING_SERVICE_ACCOUNT="${5:?artifact signing service account is required}"
REGION="${REGION:-us-west1}"

gcloud auth configure-docker "${REGION}-docker.pkg.dev" --quiet
DATABASE_URL="$(gcloud secrets versions access latest --secret="${DB_SECRET}")"
DEEPSEEK_API_KEY="$(gcloud secrets versions access latest --secret="${DEEPSEEK_SECRET}")"

docker pull "$IMAGE"
docker rm -f pax-manager >/dev/null 2>&1 || true
docker run -d \
  --name pax-manager \
  --restart=always \
  --env-file /etc/pax-manager/env \
  -e DATABASE_URL="${DATABASE_URL}" \
  -e TEAM_MEMEX_EXECUTOR=deepseek \
  -e DEEPSEEK_API_KEY="${DEEPSEEK_API_KEY}" \
  -e SESSION_ARTIFACT_GCS_BUCKET="${SESSION_ARTIFACT_GCS_BUCKET}" \
  -e PAXD_ARTIFACT_SIGNING_SERVICE_ACCOUNT="${PAXD_ARTIFACT_SIGNING_SERVICE_ACCOUNT}" \
  -v /cloudsql:/cloudsql \
  -p 9879:9879 \
  "$IMAGE"

for _ in $(seq 1 90); do
  if curl -fsS http://127.0.0.1:9879/health >/dev/null; then
    docker image prune -f
    exit 0
  fi
  sleep 2
done

docker ps -a --filter name=pax-manager
docker ps -a --filter name=pax-manager-cloud-sql-proxy
docker logs --tail=120 pax-manager || true
docker logs --tail=120 pax-manager-cloud-sql-proxy || true
exit 1

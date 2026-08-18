#!/usr/bin/env bash
set -euo pipefail

IMAGE="${1:?image is required}"
DB_SECRET="${2:-pax-manager-database-url}"
DEEPSEEK_SECRET="${3:-deepseek_api_key}"
OBJECT_STORAGE_BUCKET="${4:?object storage bucket is required}"
OBJECT_STORAGE_REGION="${5:-us-east-1}"
OBJECT_STORAGE_ENDPOINT="${6:-}"
OBJECT_STORAGE_PUBLIC_ENDPOINT="${7:-${OBJECT_STORAGE_ENDPOINT}}"
OBJECT_STORAGE_FORCE_PATH_STYLE="${8:-false}"
AWS_ACCESS_KEY_ID_SECRET="${9:?AWS access key ID secret name is required}"
AWS_SECRET_ACCESS_KEY_SECRET="${10:?AWS secret access key secret name is required}"
AWS_SESSION_TOKEN_SECRET="${11:-}"
REGION="${REGION:-us-west1}"

gcloud auth configure-docker "${REGION}-docker.pkg.dev" --quiet
DATABASE_URL="$(gcloud secrets versions access latest --secret="${DB_SECRET}")"
DEEPSEEK_API_KEY="$(gcloud secrets versions access latest --secret="${DEEPSEEK_SECRET}")"
AWS_ACCESS_KEY_ID="$(gcloud secrets versions access latest --secret="${AWS_ACCESS_KEY_ID_SECRET}")"
AWS_SECRET_ACCESS_KEY="$(
  gcloud secrets versions access latest --secret="${AWS_SECRET_ACCESS_KEY_SECRET}"
)"
AWS_SESSION_TOKEN_ARGS=()
if [[ -n "${AWS_SESSION_TOKEN_SECRET}" ]]; then
  AWS_SESSION_TOKEN="$(
    gcloud secrets versions access latest --secret="${AWS_SESSION_TOKEN_SECRET}"
  )"
  AWS_SESSION_TOKEN_ARGS=(-e AWS_SESSION_TOKEN="${AWS_SESSION_TOKEN}")
fi

docker pull "$IMAGE"
docker rm -f pax-manager >/dev/null 2>&1 || true
docker run -d \
  --name pax-manager \
  --restart=always \
  --env-file /etc/pax-manager/env \
  -e DATABASE_URL="${DATABASE_URL}" \
  -e TEAM_MEMEX_EXECUTOR=deepseek \
  -e DEEPSEEK_API_KEY="${DEEPSEEK_API_KEY}" \
  -e OBJECT_STORAGE_BUCKET="${OBJECT_STORAGE_BUCKET}" \
  -e OBJECT_STORAGE_REGION="${OBJECT_STORAGE_REGION}" \
  -e OBJECT_STORAGE_ENDPOINT="${OBJECT_STORAGE_ENDPOINT}" \
  -e OBJECT_STORAGE_PUBLIC_ENDPOINT="${OBJECT_STORAGE_PUBLIC_ENDPOINT}" \
  -e OBJECT_STORAGE_FORCE_PATH_STYLE="${OBJECT_STORAGE_FORCE_PATH_STYLE}" \
  -e AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID}" \
  -e AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY}" \
  "${AWS_SESSION_TOKEN_ARGS[@]}" \
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

#!/usr/bin/env bash
set -euo pipefail

IMAGE="${1:?image is required}"
DB_SECRET="${2:-pax-manager-database-url}"
REGION="${REGION:-us-west1}"

gcloud auth configure-docker "${REGION}-docker.pkg.dev" --quiet
DATABASE_URL="$(gcloud secrets versions access latest --secret="${DB_SECRET}")"

docker pull "$IMAGE"
docker rm -f pax-manager >/dev/null 2>&1 || true
docker run -d \
  --name pax-manager \
  --restart=always \
  --env-file /etc/pax-manager/env \
  -e DATABASE_URL="${DATABASE_URL}" \
  -v /cloudsql:/cloudsql \
  -p 9879:9879 \
  "$IMAGE"

for _ in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:9879/health >/dev/null; then
    docker image prune -f
    exit 0
  fi
  sleep 2
done

docker logs --tail=120 pax-manager || true
exit 1

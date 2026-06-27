#!/usr/bin/env bash
set -euo pipefail

CLOUD_SQL_INSTANCE="${1:?cloud sql instance connection name is required}"
DB_NAME="${DB_NAME:-paxdb}"
DB_USER="${DB_USER:-pax}"

if ! command -v docker >/dev/null 2>&1 || ! command -v curl >/dev/null 2>&1 || ! command -v pg_isready >/dev/null 2>&1; then
  apt-get update
  apt-get install -y docker.io curl postgresql-client
fi

systemctl enable --now docker

mkdir -p /cloudsql
chmod 0777 /cloudsql

docker pull gcr.io/cloud-sql-connectors/cloud-sql-proxy:2
docker rm -f pax-manager-cloud-sql-proxy >/dev/null 2>&1 || true
docker run -d \
  --name pax-manager-cloud-sql-proxy \
  --restart=always \
  -v /cloudsql:/cloudsql \
  gcr.io/cloud-sql-connectors/cloud-sql-proxy:2 \
  --unix-socket /cloudsql \
  "${CLOUD_SQL_INSTANCE}"

docker --version
docker logs --tail=40 pax-manager-cloud-sql-proxy || true

for attempt in $(seq 1 60); do
  if pg_isready -h "/cloudsql/${CLOUD_SQL_INSTANCE}" -p 5432 -U "${DB_USER}" -d "${DB_NAME}" >/dev/null 2>&1; then
    exit 0
  fi
  if [ "$attempt" = 1 ] || [ $((attempt % 10)) = 0 ]; then
    docker logs --tail=40 pax-manager-cloud-sql-proxy || true
  fi
  sleep 2
done

docker ps -a --filter name=pax-manager-cloud-sql-proxy
docker logs --tail=120 pax-manager-cloud-sql-proxy || true
exit 1

#!/usr/bin/env bash
set -euo pipefail

CLOUD_SQL_INSTANCE="${1:?cloud sql instance connection name is required}"

if ! command -v docker >/dev/null 2>&1 || ! command -v curl >/dev/null 2>&1; then
  apt-get update
  apt-get install -y docker.io curl
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
